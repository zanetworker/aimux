package controller

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testLaunchd stands in for launchd with real processes: a job is a file naming the program to run, and a restart
// kills the running process and starts the program that file names. The "programs" are small shell scripts that
// write their build to a state file and stay up, so a test sees which file is really running after an upgrade.
type testLaunchd struct {
	t       *testing.T
	jobFile string // holds the program path; missing when the job is not installed
	state   string // the running program writes its build here
	// mu guards proc and exited: a health probe abandoned at its time limit can still be reading them.
	mu      sync.Mutex
	proc    *exec.Cmd
	exited  chan struct{}
	started time.Time // when the running process was started, as a service reports in its health answer
}

func newTestLaunchd(t *testing.T) *testLaunchd {
	t.Helper()
	dir := t.TempDir()
	l := &testLaunchd{t: t, jobFile: filepath.Join(dir, "job"), state: filepath.Join(dir, "state")}
	t.Cleanup(l.stop)
	return l
}

func (l *testLaunchd) Program() (string, error) {
	b, err := os.ReadFile(l.jobFile) //nolint:gosec // G703: a path under t.TempDir
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func (l *testLaunchd) Point(program string) error {
	if err := os.WriteFile(l.jobFile, []byte(program), 0o600); err != nil { //nolint:gosec // G703: a path under t.TempDir
		return err
	}
	return l.Restart()
}

func (l *testLaunchd) Restart() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopLocked()
	program, err := l.Program()
	if err != nil || program == "" {
		return errors.New("no job to restart")
	}
	cmd := exec.Command(program) // #nosec G204 -- a script the test wrote
	cmd.Env = append(os.Environ(), "STATE="+l.state)
	if err := cmd.Start(); err != nil {
		return err
	}
	l.proc, l.exited, l.started = cmd, make(chan struct{}), time.Now()
	go func(exited chan struct{}) { _ = cmd.Wait(); close(exited) }(l.exited)
	return nil
}

func (l *testLaunchd) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopLocked()
}

func (l *testLaunchd) stopLocked() {
	if l.proc == nil {
		return
	}
	_ = l.proc.Process.Kill()
	<-l.exited
	l.proc = nil
	_ = os.Remove(l.state) //nolint:gosec // G703: a path under t.TempDir
}

// alive reports whether the job's process runs, and when it was started. The start time is kept here and not
// read from the state file's modification time, which some filesystems round to before the restart began.
func (l *testLaunchd) alive() (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.proc == nil {
		return false, time.Time{}
	}
	select {
	case <-l.exited:
		return false, time.Time{}
	default:
		return true, l.started
	}
}

// probe reads what is really running: the build the process wrote, and when the process was started.
func (l *testLaunchd) probe(name string) StatusProbe {
	return StatusProbe{Name: name, Run: func(context.Context) ServiceStatus {
		row := ServiceStatus{Name: name, State: ServiceDown, Reason: "not running"}
		alive, started := l.alive()
		build, err := os.ReadFile(l.state) //nolint:gosec // G703: a path under t.TempDir
		if !alive || err != nil || len(build) == 0 {
			return row
		}
		return ServiceStatus{Name: name, State: ServiceOK, Build: strings.TrimSpace(string(build)), StartedAt: &started}
	}}
}

func (l *testLaunchd) runningBuild() string {
	return l.probe("x").Run(context.Background()).Build
}

// waitRunning blocks until the job's process has reported its build, so a test starts from a settled service.
func (l *testLaunchd) waitRunning(build string) {
	l.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for l.runningBuild() != build {
		if time.Now().After(deadline) {
			l.t.Fatalf("service never reported build %q (reports %q)", build, l.runningBuild())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// program writes a script that reports build and stays up. An empty build is a program that exits at once.
func program(t *testing.T, build string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aimux")
	script := "#!/bin/sh\nexit 1\n"
	if build != "" {
		script = "#!/bin/sh\necho " + build + " > \"$STATE\"\nexec sleep 600\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { // #nosec G306 -- a test program must be executable
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func upgradeOne(t *testing.T, l *testLaunchd, dir, source, build string, wait time.Duration) UpgradeResult {
	t.Helper()
	results := UpgradeServices(context.Background(), []UpgradeService{{
		ID: "model-proxy", Name: "model proxy", Dir: dir, Job: l, Probe: l.probe("model proxy"),
	}}, UpgradeOptions{Source: source, Build: build, Wait: wait})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	return results[0]
}

const (
	settle  = 10 * time.Second        // generous: a healthy service answers in milliseconds
	giveUp  = 1500 * time.Millisecond // a broken build is only known once the wait runs out
	ownName = "aimux"
)

// The first upgrade moves a job that runs the owner's CLI file to the service's own copy.
func TestUpgradeServices_FirstRunMovesTheJobToItsOwnCopy(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	if err := l.Point(cli); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	dir := filepath.Join(t.TempDir(), "services", "model-proxy")
	source := program(t, "196")

	got := upgradeOne(t, l, dir, source, "196", settle)

	own := filepath.Join(dir, ownName)
	want := UpgradeResult{ID: "model-proxy", Name: "model proxy", Outcome: UpgradeDone, From: "194", To: "196", MovedFrom: cli}
	if got != want {
		t.Fatalf("result %+v, want %+v", got, want)
	}
	if p, _ := l.Program(); p != own {
		t.Fatalf("job runs %q, want the service's own copy %q", p, own)
	}
	if l.runningBuild() != "196" {
		t.Fatalf("running build %q, want 196", l.runningBuild())
	}
	if readFile(t, own) != readFile(t, source) {
		t.Fatal("the service's copy is not the source program")
	}
	if info, err := os.Stat(own); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the service's copy is not executable: %v, %v", info, err)
	}
	if readFile(t, cli) != "#!/bin/sh\necho 194 > \"$STATE\"\nexec sleep 600\n" {
		t.Fatal("the owner's CLI file was changed")
	}
	// The file the job ran before is kept as the build to go back to.
	if readFile(t, own+".prev") != readFile(t, cli) {
		t.Fatal("the build the job ran before the move was not kept as the previous build")
	}
}

// A later upgrade keeps the build it replaces, and a service already on this build is left running.
func TestUpgradeServices_KeepsThePreviousBuildAndSkipsACurrentService(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	first, second := program(t, "196"), program(t, "197")
	if got := seedOwnCopy(t, l, dir, first, "196"); got.Outcome != UpgradeDone {
		t.Fatalf("seed: %+v", got)
	}

	got := upgradeOne(t, l, dir, second, "197", settle)
	if want := (UpgradeResult{ID: "model-proxy", Name: "model proxy", Outcome: UpgradeDone, From: "196", To: "197"}); got != want {
		t.Fatalf("result %+v, want %+v", got, want)
	}
	if l.runningBuild() != "197" || readFile(t, own+".prev") != readFile(t, first) {
		t.Fatalf("running %q, previous copy %q", l.runningBuild(), readFile(t, own+".prev"))
	}

	pid := l.proc.Process.Pid
	got = upgradeOne(t, l, dir, second, "197", settle)
	if want := (UpgradeResult{ID: "model-proxy", Name: "model proxy", Outcome: UpgradeCurrent, From: "197", To: "197"}); got != want {
		t.Fatalf("second run %+v, want %+v", got, want)
	}
	if l.proc.Process.Pid != pid {
		t.Fatal("a service already on this build was restarted")
	}
	if readFile(t, own+".prev") != readFile(t, first) {
		t.Fatal("skipping a current service changed its previous build")
	}
}

// seedOwnCopy puts a service on its own copy of source, as an earlier upgrade would have.
func seedOwnCopy(t *testing.T, l *testLaunchd, dir, source, build string) UpgradeResult {
	t.Helper()
	if err := l.Point(program(t, "old")); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("old")
	return upgradeOne(t, l, dir, source, build, settle)
}

// A build whose service does not come up is taken out again: the previous program runs, and the build kept for
// rollback is still the one from before.
func TestUpgradeServices_BrokenBuildRestoresTheRunningOne(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	older, current := program(t, "195"), program(t, "196")
	seedOwnCopy(t, l, dir, older, "195")
	if got := upgradeOne(t, l, dir, current, "196", settle); got.Outcome != UpgradeDone {
		t.Fatalf("setup: %+v", got)
	}

	got := upgradeOne(t, l, dir, program(t, ""), "197", giveUp)

	if got.Outcome != UpgradeFailed || !got.Restored || got.From != "196" || got.To != "197" {
		t.Fatalf("result %+v, want a failed upgrade from 196 to 197 with the previous build restored", got)
	}
	if !strings.Contains(got.Problem, "did not come up on build 197") {
		t.Fatalf("problem %q does not say what failed", got.Problem)
	}
	l.waitRunning("196")
	if readFile(t, own) != readFile(t, current) {
		t.Fatal("the broken build was left in place")
	}
	if readFile(t, own+".prev") != readFile(t, older) {
		t.Fatal("the failed upgrade overwrote the build kept for rollback")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".aimux-*")); len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}
}

// On a first run that fails, the job goes back to the file it ran before and nothing of ours stays.
func TestUpgradeServices_BrokenBuildOnAFirstRunPutsTheJobBack(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	if err := l.Point(cli); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	dir := filepath.Join(t.TempDir(), "model-proxy")

	got := upgradeOne(t, l, dir, program(t, ""), "196", giveUp)

	if got.Outcome != UpgradeFailed || !got.Restored {
		t.Fatalf("result %+v, want a failed upgrade with the previous program restored", got)
	}
	if p, _ := l.Program(); p != cli {
		t.Fatalf("job runs %q, want it back on %q", p, cli)
	}
	l.waitRunning("194")
	if _, err := os.Stat(filepath.Join(dir, ownName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the broken build stayed in the service's folder: %v", err)
	}
}

// A service that is down on the current build is started again, without a copy.
func TestUpgradeServices_RestartsACurrentServiceThatIsDown(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	source := program(t, "196")
	seedOwnCopy(t, l, dir, source, "196")
	l.stop()

	got := upgradeOne(t, l, dir, source, "196", settle)

	if want := (UpgradeResult{ID: "model-proxy", Name: "model proxy", Outcome: UpgradeRestarted, To: "196"}); got != want {
		t.Fatalf("result %+v, want %+v", got, want)
	}
	if l.runningBuild() != "196" {
		t.Fatalf("running build %q, want 196", l.runningBuild())
	}
}

// Running the kept previous build's own upgrade is the manual rollback: the two builds change places.
func TestUpgradeServices_RunFromThePreviousBuildSwapsBack(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	older, newer := program(t, "195"), program(t, "196")
	seedOwnCopy(t, l, dir, older, "195")
	upgradeOne(t, l, dir, newer, "196", settle)

	got := upgradeOne(t, l, dir, own+".prev", "195", settle)

	if got.Outcome != UpgradeDone || got.From != "196" || got.To != "195" {
		t.Fatalf("result %+v, want an upgrade from 196 to 195", got)
	}
	if l.runningBuild() != "195" || readFile(t, own) != readFile(t, older) || readFile(t, own+".prev") != readFile(t, newer) {
		t.Fatalf("running %q; the two builds did not change places", l.runningBuild())
	}
}

// A service without a launchd job is reported and nothing is written for it.
func TestUpgradeServices_NotInstalled(t *testing.T) {
	l := newTestLaunchd(t)
	dir := filepath.Join(t.TempDir(), "collector")

	got := upgradeOne(t, l, dir, program(t, "196"), "196", settle)

	if want := (UpgradeResult{ID: "model-proxy", Name: "model proxy", Outcome: UpgradeNotInstalled, To: "196"}); got != want {
		t.Fatalf("result %+v, want %+v", got, want)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a folder was made for a service that is not installed: %v", err)
	}
}

// A dry run says what would happen and touches neither the files nor the service.
func TestUpgradeServices_DryRunChangesNothing(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	if err := l.Point(cli); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	pid := l.proc.Process.Pid
	dir := filepath.Join(t.TempDir(), "model-proxy")

	results := UpgradeServices(context.Background(), []UpgradeService{{
		ID: "model-proxy", Name: "model proxy", Dir: dir, Job: l, Probe: l.probe("model proxy"),
	}}, UpgradeOptions{Source: program(t, "196"), Build: "196", Wait: settle, DryRun: true})

	want := UpgradeResult{ID: "model-proxy", Name: "model proxy", Outcome: UpgradeDone, From: "194", To: "196", MovedFrom: cli}
	if len(results) != 1 || results[0] != want {
		t.Fatalf("results %+v, want %+v", results, want)
	}
	if p, _ := l.Program(); p != cli || l.proc.Process.Pid != pid {
		t.Fatal("a dry run changed the job or restarted the service")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a dry run wrote to the service's folder: %v", err)
	}
}

// One service failing does not stop the next, and results keep the order given.
func TestUpgradeServices_ContinuesAfterAFailure(t *testing.T) {
	bad, good := newTestLaunchd(t), newTestLaunchd(t)
	for _, l := range []*testLaunchd{bad, good} {
		if err := l.Point(program(t, "194")); err != nil {
			t.Fatal(err)
		}
		l.waitRunning("194")
	}
	// A folder that cannot be made: its parent is a regular file.
	blocked := filepath.Join(program(t, "x"), "model-proxy")

	results := UpgradeServices(context.Background(), []UpgradeService{
		{ID: "model-proxy", Name: "model proxy", Dir: blocked, Job: bad, Probe: bad.probe("model proxy")},
		{ID: "collector", Name: "collector", Dir: filepath.Join(t.TempDir(), "collector"), Job: good, Probe: good.probe("collector")},
	}, UpgradeOptions{Source: program(t, "196"), Build: "196", Wait: settle})

	if len(results) != 2 || results[0].ID != "model-proxy" || results[1].ID != "collector" {
		t.Fatalf("results out of order: %+v", results)
	}
	if results[0].Outcome != UpgradeFailed || results[0].Problem == "" || results[0].Restored {
		t.Fatalf("first result %+v, want a failure before anything changed", results[0])
	}
	if bad.runningBuild() != "194" {
		t.Fatalf("a service whose copy could not be staged was disturbed: running %q", bad.runningBuild())
	}
	if results[1].Outcome != UpgradeDone || good.runningBuild() != "196" {
		t.Fatalf("second result %+v, running %q", results[1], good.runningBuild())
	}
}

// A service that comes back healthy on some other build has not been upgraded: the previous program returns.
func TestUpgradeServices_AnotherBuildAnsweringIsAFailure(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	if err := l.Point(cli); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")

	// The program installed reports 196, not the 197 the upgrade was told it is.
	got := upgradeOne(t, l, filepath.Join(t.TempDir(), "model-proxy"), program(t, "196"), "197", giveUp)

	if got.Outcome != UpgradeFailed || !got.Restored || !strings.Contains(got.Problem, "it answers on build 196") {
		t.Fatalf("result %+v, want a failure naming the build that answered, with the previous program restored", got)
	}
	l.waitRunning("194")
	if p, _ := l.Program(); p != cli {
		t.Fatalf("job runs %q, want it back on %q", p, cli)
	}
}

// When the file the job ran was already replaced by this same build, a failed move has no earlier build to return
// to, and says so instead of claiming a rollback.
func TestUpgradeServices_FirstRunWithNoEarlierBuildSaysSo(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	if err := l.Point(cli); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	// An install has since overwritten the file with the build about to be upgraded to, which does not start.
	broken := program(t, "")
	if err := os.WriteFile(cli, []byte(readFile(t, broken)), 0o755); err != nil { // #nosec G306 -- a test program must be executable
		t.Fatal(err)
	}

	got := upgradeOne(t, l, filepath.Join(t.TempDir(), "model-proxy"), broken, "196", giveUp)

	if got.Outcome != UpgradeFailed || got.Restored {
		t.Fatalf("result %+v, want a failure that does not claim the previous build is back", got)
	}
	if !strings.Contains(got.Problem, "no earlier build was left to return to") {
		t.Fatalf("problem %q does not say why nothing could be restored", got.Problem)
	}
	if p, _ := l.Program(); p != cli {
		t.Fatalf("job runs %q, want it back on the file it ran before, %q", p, cli)
	}
}

// Only one upgrade of a service runs at a time, and one is refused while something else holds the service.
func TestUpgradeServices_OneAtATime(t *testing.T) {
	l := newTestLaunchd(t)
	if err := l.Point(program(t, "194")); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	pid := l.proc.Process.Pid
	dir := filepath.Join(t.TempDir(), "model-proxy")
	source := program(t, "196")

	release, err := lockService(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := upgradeOne(t, l, dir, source, "196", settle)
	release()
	if got.Outcome != UpgradeFailed || !strings.Contains(got.Problem, "another upgrade of model proxy is running") || !strings.Contains(got.Problem, "nothing was changed") {
		t.Fatalf("result while locked %+v, want a refusal that changed nothing", got)
	}

	results := UpgradeServices(context.Background(), []UpgradeService{{
		ID: "model-proxy", Name: "model proxy", Dir: dir, Job: l, Probe: l.probe("model proxy"),
		Hold: func() (func(), error) { return nil, errors.New("a promotion of the model proxy is in progress") },
	}}, UpgradeOptions{Source: source, Build: "196", Wait: settle})
	if results[0].Outcome != UpgradeFailed || !strings.Contains(results[0].Problem, "a promotion of the model proxy is in progress; nothing was changed") {
		t.Fatalf("result while blocked %+v, want a refusal naming the promotion", results[0])
	}
	if l.proc.Process.Pid != pid || l.runningBuild() != "194" {
		t.Fatal("a refused upgrade disturbed the service")
	}

	if got := upgradeOne(t, l, dir, source, "196", settle); got.Outcome != UpgradeDone {
		t.Fatalf("after the lock was released: %+v", got)
	}
}

// An upgrade interrupted while it waits still puts the previous program back before it returns.
func TestUpgradeServices_InterruptedUpgradeRollsBack(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	current := program(t, "196")
	seedOwnCopy(t, l, dir, current, "196")

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	started := time.Now()
	results := UpgradeServices(ctx, []UpgradeService{{
		ID: "model-proxy", Name: "model proxy", Dir: dir, Job: l, Probe: l.probe("model proxy"),
	}}, UpgradeOptions{Source: program(t, ""), Build: "197", Wait: 30 * time.Second})

	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("an interrupted upgrade took %v; the interrupt was not noticed", took)
	}
	if got := results[0]; got.Outcome != UpgradeFailed || !got.Restored {
		t.Fatalf("result %+v, want a failure with the previous program restored", got)
	}
	if l.runningBuild() != "196" || readFile(t, filepath.Join(dir, ownName)) != readFile(t, current) {
		t.Fatalf("running %q; the interrupted upgrade left the new build in place", l.runningBuild())
	}
}

// An upgrade killed after the swap leaves the last working build aside and an unchecked one in place. The next
// upgrade puts the working one back first, so a failure returns to it and not to the unchecked build.
func TestUpgradeServices_LeftoverFromAKilledUpgrade(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	working := program(t, "195")
	seedOwnCopy(t, l, dir, working, "195")
	// What a kill leaves: the working build aside, an unchecked build at the program's path.
	if err := os.Rename(own, filepath.Join(dir, ".aimux-replaced")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte(readFile(t, program(t, "196"))), 0o755); err != nil { // #nosec G306 -- a test program must be executable
		t.Fatal(err)
	}

	got := upgradeOne(t, l, dir, program(t, ""), "197", giveUp)

	if got.Outcome != UpgradeFailed || !got.Restored {
		t.Fatalf("result %+v, want a failed upgrade with the previous program restored", got)
	}
	l.waitRunning("195")
	if readFile(t, own) != readFile(t, working) {
		t.Fatal("the failure returned to the unchecked build, not to the last working one")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".aimux-*")); len(leftovers) != 0 {
		t.Fatalf("files left behind: %v", leftovers)
	}
	if got := upgradeOne(t, l, dir, program(t, "198"), "198", settle); got.Outcome != UpgradeDone {
		t.Fatalf("the next upgrade: %+v", got)
	}
}

// After an interrupt no further service is touched.
func TestUpgradeServices_InterruptStopsBeforeTheNextService(t *testing.T) {
	l := newTestLaunchd(t)
	if err := l.Point(program(t, "194")); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	pid := l.proc.Process.Pid
	dir := filepath.Join(t.TempDir(), "collector")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := UpgradeServices(ctx, []UpgradeService{{
		ID: "collector", Name: "collector", Dir: dir, Job: l, Probe: l.probe("collector"),
	}}, UpgradeOptions{Source: program(t, "196"), Build: "196", Wait: settle})

	if got := results[0]; got.Outcome != UpgradeFailed || !strings.Contains(got.Problem, "interrupted before this service; nothing was changed") {
		t.Fatalf("result %+v, want the service left alone", got)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) || l.proc.Process.Pid != pid {
		t.Fatal("a service was touched after the interrupt")
	}
}

// A service that is degraded but already on this build keeps running: restarting it would end turns in flight.
func TestUpgradeServices_DegradedButCurrentIsLeftRunning(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	source := program(t, "196")
	seedOwnCopy(t, l, dir, source, "196")
	pid := l.proc.Process.Pid
	degraded := StatusProbe{Name: "model proxy", Run: func(ctx context.Context) ServiceStatus {
		row := l.probe("model proxy").Run(ctx)
		row.State, row.Reason = ServiceDegraded, "degraded"
		return row
	}}

	results := UpgradeServices(context.Background(), []UpgradeService{{
		ID: "model-proxy", Name: "model proxy", Dir: dir, Job: l, Probe: degraded,
	}}, UpgradeOptions{Source: source, Build: "196", Wait: settle})

	if results[0].Outcome != UpgradeCurrent || l.proc.Process.Pid != pid {
		t.Fatalf("result %+v; a degraded service already on this build was restarted", results[0])
	}
}

// A first move killed before its health check leaves the job on an unchecked build. The file the job ran before
// was written down first, so the next run can still return to it.
func TestUpgradeServices_FirstMoveKilledBeforeItsCheck(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	broken := program(t, "")
	// What the kill leaves: the unchecked build in the service's folder, the job pointing at it, and the note.
	if err := os.WriteFile(own, []byte(readFile(t, broken)), 0o755); err != nil { // #nosec G306 -- a test program must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".aimux-moved-from"), []byte(cli), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Point(own); err != nil {
		t.Fatal(err)
	}

	got := upgradeOne(t, l, dir, broken, "196", giveUp)

	if got.Outcome != UpgradeFailed || !got.Restored {
		t.Fatalf("result %+v, want a failure that returns to the file the job ran before the move", got)
	}
	if p, _ := l.Program(); p != cli {
		t.Fatalf("job runs %q, want %q", p, cli)
	}
	l.waitRunning("194")
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".aimux-*")); len(leftovers) != 0 {
		t.Fatalf("files left behind: %v", leftovers)
	}
}

// After putting back a build an interrupted upgrade left aside, the service is restarted even when the process
// still running reports the same build: it is the unchecked one.
func TestUpgradeServices_RestartsAfterRecoveringALeftover(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	working := program(t, "195")
	seedOwnCopy(t, l, dir, working, "195")
	if err := os.Rename(own, filepath.Join(dir, ".aimux-replaced")); err != nil {
		t.Fatal(err)
	}
	// An unchecked build that reports the same version as the working one, and is the one running.
	if err := os.WriteFile(own, []byte(readFile(t, working)+"# unchecked\n"), 0o755); err != nil { // #nosec G306 -- a test program must be executable
		t.Fatal(err)
	}
	if err := l.Restart(); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("195")
	pid := l.proc.Process.Pid

	got := upgradeOne(t, l, dir, working, "195", settle)

	if got.Outcome != UpgradeRestarted || l.proc.Process.Pid == pid {
		t.Fatalf("result %+v; the unchecked process was left running", got)
	}
	if readFile(t, own) != readFile(t, working) {
		t.Fatal("the working build was not put back")
	}
}

// An interrupt that arrives while the service is first asked how it is changes nothing.
func TestUpgradeServices_InterruptBeforeTheFirstChange(t *testing.T) {
	l := newTestLaunchd(t)
	if err := l.Point(program(t, "194")); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("194")
	pid := l.proc.Process.Pid
	dir := filepath.Join(t.TempDir(), "model-proxy")
	ctx, cancel := context.WithCancel(context.Background())
	interrupting := StatusProbe{Name: "model proxy", Run: func(ctx context.Context) ServiceStatus {
		cancel()
		return l.probe("model proxy").Run(ctx)
	}}

	results := UpgradeServices(ctx, []UpgradeService{{
		ID: "model-proxy", Name: "model proxy", Dir: dir, Job: l, Probe: interrupting,
	}}, UpgradeOptions{Source: program(t, "196"), Build: "196", Wait: settle})

	if got := results[0]; got.Outcome != UpgradeFailed || !strings.Contains(got.Problem, "interrupted; nothing was changed") {
		t.Fatalf("result %+v, want nothing changed", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ownName)); !errors.Is(err, os.ErrNotExist) || l.proc.Process.Pid != pid {
		t.Fatal("the service was changed after the interrupt")
	}
}

// When the replaced build cannot be kept, the upgrade still stands and nothing is left aside for the next run to
// mistake for an interrupted swap.
func TestUpgradeServices_NothingLeftAsideWhenThePreviousBuildCannotBeKept(t *testing.T) {
	l := newTestLaunchd(t)
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	seedOwnCopy(t, l, dir, program(t, "195"), "195")
	// A folder where the kept build would go, so it cannot be written.
	_ = os.Remove(own + ".prev")
	if err := os.MkdirAll(filepath.Join(own+".prev", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	newer := program(t, "196")

	got := upgradeOne(t, l, dir, newer, "196", settle)

	if got.Outcome != UpgradeDone || !strings.Contains(got.Problem, "was not kept") {
		t.Fatalf("result %+v, want the upgrade done with a note that the replaced build was not kept", got)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".aimux-*")); len(leftovers) != 0 {
		t.Fatalf("files left aside after a success: %v", leftovers)
	}
	if again := upgradeOne(t, l, dir, newer, "196", settle); again.Outcome != UpgradeCurrent || readFile(t, own) != readFile(t, newer) {
		t.Fatalf("the next run %+v; it must leave the verified build in place", again)
	}
}

// A first move that was killed and is retried with another build keeps the original program as the previous
// build, not the unchecked copy the killed move left.
func TestUpgradeServices_RetriedFirstMoveKeepsTheOriginalProgram(t *testing.T) {
	l := newTestLaunchd(t)
	cli := program(t, "194")
	dir := t.TempDir()
	own := filepath.Join(dir, ownName)
	if err := os.WriteFile(own, []byte(readFile(t, program(t, "195"))), 0o755); err != nil { // #nosec G306 -- a test program must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".aimux-moved-from"), []byte(cli), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Point(own); err != nil {
		t.Fatal(err)
	}
	l.waitRunning("195")

	got := upgradeOne(t, l, dir, program(t, "196"), "196", settle)

	if got.Outcome != UpgradeDone || l.runningBuild() != "196" {
		t.Fatalf("result %+v, running %q", got, l.runningBuild())
	}
	if readFile(t, own+".prev") != readFile(t, cli) {
		t.Fatal("the kept build is the unchecked copy, not the program the job ran before the move")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".aimux-*")); len(leftovers) != 0 {
		t.Fatalf("files left behind: %v", leftovers)
	}
}
