package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/parts"
)

const testJobFile = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/the/owners/path</string>
	</dict>
	<key>Label</key>
	<string>com.aimux.model-proxy</string>
	<key>ProgramArguments</key>
	<array>
		<string>/p/aimux</string>
		<string>model</string>
		<string>proxy</string>
		<string>serve</string>
	</array>
</dict>
</plist>
`

// fileJob is a job kept in a real file whose "service" is started by Reload from what the file says, as a
// supervisor would: it comes up unless its arguments name an engine it cannot start with.
type fileJob struct {
	path string

	mu      sync.Mutex
	started *time.Time
	reloads int
	stops   int
	off     bool  // told not to come back
	refuse  error // a Reload or a Stop that fails outright
}

func (j *fileJob) Read() ([]byte, error)   { return os.ReadFile(j.path) } // #nosec G304 -- the test's own file
func (j *fileJob) Write(data []byte) error { return os.WriteFile(j.path, data, 0o600) }
func (j *fileJob) Reload() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.reloads++
	if j.refuse != nil {
		return j.refuse
	}
	data, err := os.ReadFile(j.path) // #nosec G304 -- the test's own file
	if err != nil {
		return err
	}
	j.started, j.off = nil, false
	if !strings.Contains(string(data), "broken=") { // an upstream called "broken" keeps the service from starting
		now := time.Now()
		j.started = &now
	}
	return nil
}

func (j *fileJob) Stop() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stops++
	if j.refuse != nil {
		return j.refuse
	}
	j.started, j.off = nil, true
	return nil
}

func (j *fileJob) KeptOff() (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.off, nil
}

func (j *fileJob) Loaded() (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.started != nil, nil
}

func (j *fileJob) probe() StatusProbe {
	return StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.started == nil {
			return ServiceStatus{Name: "model proxy", State: ServiceDown, Reason: "not running"}
		}
		return ServiceStatus{Name: "model proxy", State: ServiceOK, StartedAt: j.started}
	}}
}

func (j *fileJob) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.reloads
}

func testProxyService(t *testing.T, jobText string) (PartService, *fileJob) {
	t.Helper()
	dir := t.TempDir()
	job := &fileJob{path: filepath.Join(dir, "job.plist")}
	if jobText != "" {
		if err := os.WriteFile(job.path, []byte(jobText), 0o600); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Add(-time.Hour)
		job.started = &now
	}
	return PartService{Service: parts.MustService("model-proxy"), Dir: filepath.Join(dir, "services", "model-proxy"), Job: job, Probe: job.probe()}, job
}

func settings(t *testing.T, yaml string) parts.Settings {
	t.Helper()
	s, err := parts.Parse([]byte(yaml))
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v", err, s.Validate())
	}
	return s
}

const proxyOn = "parts:\n  inference-proxy:\n    enabled: true\n"

func TestPlanParts_NothingStatedIsNoPlan(t *testing.T) {
	service, _ := testProxyService(t, testJobFile)
	if plan := PlanParts([]PartService{service}, parts.Settings{}); len(plan) != 0 {
		t.Fatalf("plan = %+v; with no parts section aimux manages nothing", plan)
	}
	// A part the file does not name is not planned for, whatever else it names.
	if plan := PlanParts([]PartService{service}, settings(t, "parts:\n  mcp-proxy:\n    enabled: true\n")); len(plan) != 0 {
		t.Fatalf("plan = %+v; only the named part is touched", plan)
	}
}

func TestPlanParts_AdoptCostsNoRestart(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	plan := PlanParts([]PartService{service}, settings(t, proxyOn))
	if len(plan) != 1 || plan[0].Action != ActionAdopt || plan[0].Restarts || plan[0].Part != "inference-proxy" || plan[0].Service != "model-proxy" {
		t.Fatalf("plan = %+v", plan)
	}
	results, err := ApplyParts(context.Background(), []PartService{service}, settings(t, proxyOn), plan, ApplyOptions{})
	if err != nil || len(results) != 1 || results[0].Outcome != ApplyAdopted {
		t.Fatalf("%+v, %v", results, err)
	}
	if got, _ := job.Read(); string(got) != testJobFile || job.count() != 0 {
		t.Errorf("taking the job under management changed its file or restarted it (%d reloads)", job.count())
	}
	if before, err := os.ReadFile(filepath.Join(service.Dir, JobBeforeName)); err != nil || string(before) != testJobFile { // #nosec G304 -- the test's own folder
		t.Errorf("the job file as it was is not kept: %v", err)
	}
	// Once taken, there is nothing left to do.
	if plan := PlanParts([]PartService{service}, settings(t, proxyOn)); len(plan) != 1 || plan[0].Action != ActionNone {
		t.Errorf("second plan = %+v", plan)
	}
}

func TestApplyParts_ChangesTheArgumentsAndRestarts(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	want := settings(t, proxyOn+"    engines:\n      anthropic: {}\n")
	plan := PlanParts([]PartService{service}, want)
	if len(plan) != 1 || plan[0].Action != ActionChange || !plan[0].Restarts || !slices.Equal(plan[0].Was, []string{"model", "proxy", "serve"}) || !slices.Equal(plan[0].Now, []string{"model", "proxy", "serve", "--anthropic"}) {
		t.Fatalf("plan = %+v", plan)
	}
	results, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{Wait: 2 * time.Second})
	if err != nil || results[0].Outcome != ApplyChanged {
		t.Fatalf("%+v, %v", results, err)
	}
	got, _ := job.Read()
	if !strings.Contains(string(got), "<string>--anthropic</string>") || !strings.Contains(string(got), "/the/owners/path") || job.count() != 1 {
		t.Errorf("after the change (%d reloads):\n%s", job.count(), got)
	}
	if before, _ := os.ReadFile(filepath.Join(service.Dir, JobBeforeName)); string(before) != testJobFile { // #nosec G304 -- the test's own folder
		t.Error("the job file from before aimux first changed it is not kept")
	}
	// A second change keeps the first "before", which is the owner's own file.
	off := settings(t, proxyOn+"    engines:\n")
	plan = PlanParts([]PartService{service}, off)
	if _, err := ApplyParts(context.Background(), []PartService{service}, off, plan, ApplyOptions{Wait: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if got, _ := job.Read(); string(got) != testJobFile {
		t.Errorf("turning the engine off again did not give the file back as it was:\n%s", got)
	}
	if before, _ := os.ReadFile(filepath.Join(service.Dir, JobBeforeName)); string(before) != testJobFile { // #nosec G304 -- the test's own folder
		t.Error("the kept original was written over by a later change")
	}
}

func TestApplyParts_AnArgumentThatBreaksTheServiceIsPutBack(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	bad := settings(t, proxyOn+"    engines:\n      broken: {type: messages, url: https://gw.example}\n")
	plan := PlanParts([]PartService{service}, bad)
	results, err := ApplyParts(context.Background(), []PartService{service}, bad, plan, ApplyOptions{Wait: 600 * time.Millisecond})
	if err == nil || len(results) != 1 || results[0].Outcome != ApplyRolledBack {
		t.Fatalf("%+v, %v; want the change undone", results, err)
	}
	if got, _ := job.Read(); string(got) != testJobFile {
		t.Errorf("the old job file is not back:\n%s", got)
	}
	if row := service.Probe.Run(context.Background()); row.State != ServiceOK {
		t.Errorf("the service does not run again after the roll-back: %+v", row)
	}
	if !strings.Contains(results[0].Detail, "back") {
		t.Errorf("the result does not say the old job is back: %q", results[0].Detail)
	}
}

func TestApplyParts_AReloadThatFailsIsPutBackToo(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	want := settings(t, proxyOn+"    engines:\n      anthropic: {}\n")
	plan := PlanParts([]PartService{service}, want)
	job.refuse = errors.New("launchd said no")
	results, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{Wait: 300 * time.Millisecond})
	if err == nil || results[0].Outcome == ApplyChanged {
		t.Fatalf("%+v, %v", results, err)
	}
	if got, _ := job.Read(); string(got) != testJobFile {
		t.Errorf("the old job file is not back after a reload that failed:\n%s", got)
	}
}

func TestApplyParts_StopsWhenThePlanIsStale(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	want := settings(t, proxyOn+"    engines:\n      anthropic: {}\n")
	plan := PlanParts([]PartService{service}, want)
	// The job file changes between the plan being shown and being applied.
	changed := strings.Replace(testJobFile, "<string>serve</string>", "<string>serve</string>\n\t\t<string>--verbose</string>", 1)
	if err := job.Write([]byte(changed)); err != nil {
		t.Fatal(err)
	}
	results, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{})
	if !errors.Is(err, ErrStalePlan) || job.count() != 0 {
		t.Fatalf("%+v, %v, %d reloads; want it to stop and change nothing", results, err, job.count())
	}
	if got, _ := job.Read(); string(got) != changed {
		t.Error("a stale plan was applied anyway")
	}
}

func TestPlanParts_WhatItDoesNotDo(t *testing.T) {
	for name, tc := range map[string]struct {
		job, yaml, action, note string
	}{
		"no job to manage":            {"", proxyOn, ActionSkip, "no job"},
		"a job it cannot follow":      {strings.Replace(testJobFile, "<string>serve</string>", "<string>serve</string>\n\t\t<string>--</string>", 1), proxyOn, ActionSkip, "--"},
		"a job that starts something": {strings.Replace(testJobFile, "<string>serve</string>", "<string>status</string>", 1), proxyOn, ActionSkip, "leaves it alone"},
	} {
		service, job := testProxyService(t, tc.job)
		s := settings(t, tc.yaml)
		plan := PlanParts([]PartService{service}, s)
		if len(plan) != 1 || plan[0].Action != tc.action || !strings.Contains(plan[0].Note, tc.note) {
			t.Errorf("%s: plan = %+v", name, plan)
			continue
		}
		before, _ := job.Read()
		results, err := ApplyParts(context.Background(), []PartService{service}, s, plan, ApplyOptions{})
		after, _ := job.Read()
		if err != nil || results[0].Outcome != ApplySkipped || string(before) != string(after) || job.count() != 0 {
			t.Errorf("%s: applying a skip did something: %+v, %v", name, results, err)
		}
	}
}

func TestApplyParts_RefusesWhileAnUpgradeHoldsTheService(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	want := settings(t, proxyOn+"    engines:\n      anthropic: {}\n")
	plan := PlanParts([]PartService{service}, want)
	release, err := lockService(service.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{}); err == nil || job.count() != 0 {
		t.Fatalf("applied while the service was locked: %v", err)
	}
}

func TestApplyParts_StartsAJobThatIsRightAndNotLoaded(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	job.mu.Lock()
	job.started = nil // the file is there; nothing runs it
	job.mu.Unlock()
	want := settings(t, proxyOn)
	plan := PlanParts([]PartService{service}, want)
	if len(plan) != 1 || plan[0].Action != ActionStart || !plan[0].Restarts {
		t.Fatalf("plan = %+v", plan)
	}
	results, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{Wait: 2 * time.Second})
	if err != nil || results[0].Outcome != ApplyStarted {
		t.Fatalf("%+v, %v", results, err)
	}
	if got, _ := job.Read(); string(got) != testJobFile || job.count() != 1 {
		t.Errorf("starting changed the file, or did not start it once (%d reloads)", job.count())
	}
}

func TestApplyParts_AsksTheServiceWhereItWillBe(t *testing.T) {
	service, _ := testProxyService(t, testJobFile)
	var asked [][]string
	plain := service.Probe
	service.ProbeFor = func(args []string) StatusProbe {
		asked = append(asked, args)
		return plain
	}
	want := settings(t, proxyOn+"    port: 7400\n")
	plan := PlanParts([]PartService{service}, want)
	if _, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{Wait: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !slices.Contains(asked[0], "7400") {
		t.Errorf("the service was probed for %q; want it asked for where the new arguments put it", asked)
	}
	// After a change that does not come up, the probe is for the arguments it goes back to.
	asked = nil
	bad := settings(t, proxyOn+"    port: 7400\n    engines:\n      broken: {type: messages, url: https://gw.example}\n")
	plan = PlanParts([]PartService{service}, bad)
	_, _ = ApplyParts(context.Background(), []PartService{service}, bad, plan, ApplyOptions{Wait: 400 * time.Millisecond})
	if len(asked) != 2 || slices.Contains(asked[1], "broken=https://gw.example") || !slices.Contains(asked[1], "7400") {
		t.Errorf("probes after a roll-back: %q", asked)
	}
}

func TestApplyParts_AnInterruptStillPutsTheJobBack(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	bad := settings(t, proxyOn+"    engines:\n      broken: {type: messages, url: https://gw.example}\n")
	plan := PlanParts([]PartService{service}, bad)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel() // the owner gives up waiting
	}()
	results, err := ApplyParts(ctx, []PartService{service}, bad, plan, ApplyOptions{Wait: 30 * time.Second})
	if err == nil || results[0].Outcome != ApplyRolledBack {
		t.Fatalf("%+v, %v", results, err)
	}
	if got, _ := job.Read(); string(got) != testJobFile {
		t.Error("an interrupted apply left the changed job file in place")
	}
	if row := service.Probe.Run(context.Background()); row.State != ServiceOK {
		t.Error("an interrupted apply left the service down")
	}
}

func TestApplyParts_StopsWhenAnythingInTheJobFileChanged(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	want := settings(t, proxyOn+"    engines:\n      anthropic: {}\n")
	plan := PlanParts([]PartService{service}, want)
	// Not the arguments: the environment, which the owner was not shown.
	if err := job.Write([]byte(strings.Replace(testJobFile, "/the/owners/path", "/another/path", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{}); !errors.Is(err, ErrStalePlan) || job.count() != 0 {
		t.Fatalf("err = %v, %d reloads", err, job.count())
	}
}

// A job the supervisor holds whose service does not answer is not "running": it is started.
func TestPlanParts_AJobThatIsLoadedAndDoesNotAnswerIsStarted(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	service.Probe = StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		return ServiceStatus{Name: "model proxy", State: ServiceDown, Reason: "connection refused"}
	}}
	plan := PlanParts([]PartService{service}, settings(t, proxyOn))
	if loaded, _ := job.Loaded(); !loaded || len(plan) != 1 || plan[0].Action != ActionStart || !strings.Contains(plan[0].Note, "does not answer") {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestApplyParts_AnInterruptBeginsNothingFurther(t *testing.T) {
	first, _ := testProxyService(t, testJobFile)
	second, secondJob := testProxyService(t, testJobFile)
	second.Service = parts.MustService("mcp-proxy")
	secondJob.mu.Lock()
	secondJob.started = nil // it would be started
	secondJob.mu.Unlock()
	if err := secondJob.Write([]byte(strings.NewReplacer("model-proxy", "mcp-proxy", "<string>model</string>", "<string>mcp</string>").Replace(testJobFile))); err != nil {
		t.Fatal(err)
	}
	s := settings(t, proxyOn+"    engines:\n      anthropic: {}\n  mcp-proxy:\n    enabled: true\n")
	services := []PartService{first, second}
	plan := PlanParts(services, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, _ := ApplyParts(ctx, services, s, plan, ApplyOptions{Wait: time.Second})
	if len(results) != 2 || secondJob.count() != 0 || results[1].Outcome != ApplySkipped {
		t.Fatalf("after an interrupt: %+v (%d reloads of the second)", results, secondJob.count())
	}
}

// A service that runs an older build than the aimux changing its job may not know the arguments it is about to be
// given. It is upgraded first; the change is not tried and rolled back.
func TestPlanParts_AServiceOnAnotherBuildIsUpgradedFirst(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	started := time.Now().Add(-time.Hour)
	service.Probe = StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		return ServiceStatus{Name: "model proxy", State: ServiceOK, StartedAt: &started, Build: "v0.14.0-249-gaa18bfeb"}
	}}
	service.CLIBuild = "v0.14.0-276-g823bd6f3"
	want := settings(t, proxyOn+"    engines:\n      anthropic: {}\n")
	plan := PlanParts([]PartService{service}, want)
	if len(plan) != 1 || plan[0].Action != ActionSkip || !strings.Contains(plan[0].Note, "aimux upgrade model-proxy") || !strings.Contains(plan[0].Note, "249") {
		t.Fatalf("plan = %+v", plan)
	}
	if results, err := ApplyParts(context.Background(), []PartService{service}, want, plan, ApplyOptions{}); err != nil || results[0].Outcome != ApplySkipped || job.count() != 0 {
		t.Fatalf("%+v, %v", results, err)
	}
	// Taking it under management changes nothing in what it runs, so the build does not matter for that.
	if plan := PlanParts([]PartService{service}, settings(t, proxyOn)); plan[0].Action != ActionAdopt {
		t.Errorf("adopting a service on another build: %+v", plan)
	}
	// On the same build the change goes ahead.
	service.CLIBuild = "v0.14.0-249-gaa18bfeb"
	if plan := PlanParts([]PartService{service}, want); plan[0].Action != ActionChange {
		t.Errorf("the same build: %+v", plan)
	}
	// A service newer than this aimux, or a build with no number, is not held back.
	for _, cli := range []string{"v0.14.0-200-g0000000", "dev"} {
		service.CLIBuild = cli
		if plan := PlanParts([]PartService{service}, want); plan[0].Action != ActionChange {
			t.Errorf("an aimux of build %q: %+v", cli, plan)
		}
	}
	// A service that does not say its build, or does not answer, is not held back by this: the roll-back is there.
	service.Probe = StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		return ServiceStatus{Name: "model proxy", State: ServiceDown}
	}}
	service.CLIBuild = "v0.14.0-276-g823bd6f3"
	if plan := PlanParts([]PartService{service}, want); plan[0].Action != ActionChange {
		t.Errorf("a service that does not answer: %+v", plan)
	}
}

const proxyOff = "parts:\n  inference-proxy:\n    enabled: false\n"

func TestApplyParts_APartTurnedOffIsStoppedAndOnAgainStarted(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	off := settings(t, proxyOff)
	plan := PlanParts([]PartService{service}, off)
	if len(plan) != 1 || plan[0].Action != ActionStop || !plan[0].Restarts {
		t.Fatalf("plan = %+v", plan)
	}
	results, err := ApplyParts(context.Background(), []PartService{service}, off, plan, ApplyOptions{Wait: 2 * time.Second})
	if err != nil || results[0].Outcome != ApplyStopped {
		t.Fatalf("%+v, %v", results, err)
	}
	if got, _ := job.Read(); string(got) != testJobFile || job.stops != 1 || job.count() != 0 {
		t.Errorf("stopping changed the job file, or did not stop it once (%d stops, %d reloads)", job.stops, job.count())
	}
	if row := service.Probe.Run(context.Background()); row.State != ServiceDown {
		t.Errorf("after the stop the service still answers: %+v", row)
	}
	// Off and stopped: nothing left to do.
	if plan := PlanParts([]PartService{service}, off); len(plan) != 1 || plan[0].Action != ActionNone {
		t.Errorf("a second plan = %+v", plan)
	}
	// On again: its job is right and nothing runs it, so it is started.
	on := settings(t, proxyOn)
	plan = PlanParts([]PartService{service}, on)
	if plan[0].Action != ActionStart {
		t.Fatalf("plan to turn it on again = %+v", plan)
	}
	if results, err := ApplyParts(context.Background(), []PartService{service}, on, plan, ApplyOptions{Wait: 2 * time.Second}); err != nil || results[0].Outcome != ApplyStarted {
		t.Fatalf("%+v, %v", results, err)
	}
}

func TestApplyParts_AStopThatFailsIsSaid(t *testing.T) {
	service, job := testProxyService(t, testJobFile)
	off := settings(t, proxyOff)
	plan := PlanParts([]PartService{service}, off)
	job.refuse = errors.New("launchd said no")
	results, err := ApplyParts(context.Background(), []PartService{service}, off, plan, ApplyOptions{Wait: 300 * time.Millisecond})
	if err == nil || results[0].Outcome != ApplyFailed {
		t.Fatalf("%+v, %v", results, err)
	}
}

func TestPlanParts_TurningOffWhatIsNotThereOrHasItsOwnWay(t *testing.T) {
	// No job: nothing runs, so off is already so.
	service, _ := testProxyService(t, "")
	if plan := PlanParts([]PartService{service}, settings(t, proxyOff)); len(plan) != 1 || plan[0].Action != ActionNone {
		t.Errorf("off with no job: %+v", plan)
	}
	// A job that is not loaded and could come back at the next login is not "off" yet: it is marked, with nothing
	// to restart.
	idle, idleJob := testProxyService(t, testJobFile)
	idleJob.mu.Lock()
	idleJob.started = nil
	idleJob.mu.Unlock()
	if plan := PlanParts([]PartService{idle}, settings(t, proxyOff)); len(plan) != 1 || plan[0].Action != ActionStop || plan[0].Restarts {
		t.Errorf("off with a job that is not loaded and not kept off: %+v", plan)
	}
	// No job, and something answers all the same: started by hand, and not aimux's to stop.
	byHand, _ := testProxyService(t, "")
	byHand.Probe = StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		return ServiceStatus{Name: "model proxy", State: ServiceOK}
	}}
	if plan := PlanParts([]PartService{byHand}, settings(t, proxyOff)); len(plan) != 1 || plan[0].Action != ActionSkip || !strings.Contains(plan[0].Note, "where it was started") {
		t.Errorf("off with a proxy started by hand: %+v", plan)
	}
	// A job aimux cannot follow is not stopped: it could not be started again.
	odd, oddJob := testProxyService(t, strings.Replace(testJobFile, "<key>ProgramArguments</key>", "<key>Program</key>\n\t<string>/bin/x</string>\n\t<key>ProgramArguments</key>", 1))
	if plan := PlanParts([]PartService{odd}, settings(t, proxyOff)); len(plan) != 1 || plan[0].Action != ActionSkip || oddJob.stops != 0 {
		t.Errorf("off with a job that has a Program key: %+v", plan)
	}
	// The MCP proxy is not only a job: Claude Code's server entries point at it. Its own command turns it off.
	mcp, job := testProxyService(t, testJobFile)
	mcp.Service = parts.MustService("mcp-proxy")
	if err := job.Write([]byte(strings.NewReplacer("model-proxy", "mcp-proxy", "<string>model</string>", "<string>mcp</string>").Replace(testJobFile))); err != nil {
		t.Fatal(err)
	}
	plan := PlanParts([]PartService{mcp}, settings(t, "parts:\n  mcp-proxy:\n    enabled: false\n"))
	if len(plan) != 1 || plan[0].Action != ActionSkip || !strings.Contains(plan[0].Note, "aimux mcp proxy uninstall") {
		t.Errorf("the MCP proxy turned off: %+v", plan)
	}
	// Once that command has run, its job is gone and there is nothing left to do.
	gone, _ := testProxyService(t, "")
	gone.Service = parts.MustService("mcp-proxy")
	gone.Probe = StatusProbe{Name: "mcp proxy", Run: func(context.Context) ServiceStatus { return ServiceStatus{State: ServiceDown} }}
	if plan := PlanParts([]PartService{gone}, settings(t, "parts:\n  mcp-proxy:\n    enabled: false\n")); len(plan) != 1 || plan[0].Action != ActionNone {
		t.Errorf("the MCP proxy after its uninstall: %+v", plan)
	}
}

// With no job, a probe that reports "not installed" is nothing answering: off is already so.
func TestPlanParts_OffWithNoJobAndNothingInstalledIsAlreadyOff(t *testing.T) {
	service, _ := testProxyService(t, "")
	service.Probe = StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		return ServiceStatus{Name: "model proxy", State: ServiceNotInstalled}
	}}
	plan := PlanParts([]PartService{service}, settings(t, proxyOff))
	if len(plan) != 1 || plan[0].Action != ActionNone || plan[0].Note != "" {
		t.Fatalf("plan = %+v", plan)
	}
}

// A job stopped and kept off whose port still answers is not off: something else holds the port.
func TestPlanParts_OffWithAJobKeptOffAndAListenerIsNotOff(t *testing.T) {
	service, _ := testProxyService(t, testJobFile)
	off := settings(t, proxyOff)
	plan := PlanParts([]PartService{service}, off)
	if _, err := ApplyParts(context.Background(), []PartService{service}, off, plan, ApplyOptions{Wait: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	service.Probe = StatusProbe{Name: "model proxy", Run: func(context.Context) ServiceStatus {
		return ServiceStatus{Name: "model proxy", State: ServiceOK}
	}}
	plan = PlanParts([]PartService{service}, off)
	if len(plan) != 1 || plan[0].Action != ActionSkip || !strings.Contains(plan[0].Note, "still answers") {
		t.Fatalf("plan = %+v", plan)
	}
}
