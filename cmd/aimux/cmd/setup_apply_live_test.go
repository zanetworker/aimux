//go:build launchdlive

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// A change of a job's arguments against the real launchd, on a job of the test's own: a real model proxy on a
// spare port is taken under management without a restart, given the Anthropic engine, then given a setting it
// cannot start with and put back. No installed service is touched.
// Run with: go test -tags launchdlive ./cmd/aimux/cmd/ -run TestSetupApplyLive -v
func TestSetupApplyLive_AgainstLaunchd(t *testing.T) {
	const label, addr, port = "com.aimux.apply-livetest", "127.0.0.1:7397", "7397"
	tmp := t.TempDir()
	program := filepath.Join(tmp, "aimux")
	if b, err := exec.Command("go", "build", "-ldflags", "-X main.version=apply-livetest", "-o", program, "../").CombinedOutput(); err != nil { // #nosec G204 -- fixed arguments
		t.Fatalf("build: %v\n%s", err, b)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	agent := mcpproxy.Agent{
		Label: label, PlistPath: filepath.Join(tmp, label+".plist"), Binary: program, LogDir: filepath.Join(tmp, "logs"),
		Args: []string{"model", "proxy", "serve", "--port", port}, Run: runLaunchctl, UID: os.Getuid(),
		Env: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home},
	}
	if err := os.MkdirAll(agent.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(home, ".aimux", "logs", "model-proxy", "requests-"+port+".jsonl")
	_, statErr := os.Stat(history)
	madeHere := errors.Is(statErr, os.ErrNotExist)
	t.Cleanup(func() {
		if t.Failed() {
			for _, name := range []string{"stdout.log", "stderr.log"} {
				b, _ := os.ReadFile(filepath.Join(agent.LogDir, name)) // #nosec G304 -- test temp file
				t.Logf("%s:\n%s", name, b)
			}
		}
		_ = agent.Uninstall()
		// A stop tells launchd to keep the job off; that mark is not left behind for the label.
		_, _ = runLaunchctl("launchctl", "enable", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
		if madeHere {
			_ = os.Remove(history)
		}
	})
	if err := agent.Install(); err != nil {
		t.Fatalf("install the test job: %v", err)
	}
	env := statusEnv{modelAddr: addr, plistDir: tmp}
	service := parts.MustService("model-proxy")
	service.Label = label
	svc := controller.PartService{
		Service: service, Dir: filepath.Join(tmp, "services", "model-proxy"),
		Job:   launchdPartJob{job: launchdJob{agent: agent}},
		Probe: env.probe("model proxy", addr, label, "", env.readModelStatus),
		// As the command builds it: the proxy is asked on the port its arguments give it.
		ProbeFor: func(args []string) controller.StatusProbe {
			at := env
			at.modelAddr = "127.0.0.1:" + strconv.Itoa(parts.ProxyPort(args, 7397))
			return at.probe("model proxy", at.modelAddr, label, "", at.readModelStatus)
		},
	}
	listening := addr
	engines := func() []string {
		resp, err := http.Get("http://" + listening + "/healthz")
		if err != nil {
			return nil
		}
		defer func() { _ = resp.Body.Close() }()
		var live struct {
			Engines []string `json:"engines"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&live)
		return live.Engines
	}
	deadline := time.Now().Add(20 * time.Second)
	for !slices.Equal(engines(), []string{"codex"}) {
		if time.Now().After(deadline) {
			t.Fatalf("the test job never answered (engines %v)", engines())
		}
		time.Sleep(200 * time.Millisecond)
	}
	original, _ := svc.Job.Read()
	started := func() time.Time { return *svc.Probe.Run(context.Background()).StartedAt }
	apply := func(yaml string) ([]controller.JobChange, []controller.JobResult, error) {
		s, err := parts.Parse([]byte(yaml))
		if err != nil || len(s.Validate()) != 0 {
			t.Fatalf("%v %v", err, s.Validate())
		}
		plan := controller.PlanParts([]controller.PartService{svc}, s)
		results, err := controller.ApplyParts(context.Background(), []controller.PartService{svc}, s, plan, controller.ApplyOptions{})
		return plan, results, err
	}

	// Taken under management: recorded, the file untouched, the process the same one.
	before := started()
	plan, results, err := apply("parts:\n  inference-proxy:\n    enabled: true\n")
	t.Logf("adopt: %+v %+v", plan, results)
	if now, _ := svc.Job.Read(); err != nil || results[0].Outcome != controller.ApplyAdopted || string(now) != string(original) || !started().Equal(before) {
		t.Fatalf("adopt: %v, %+v; the job file or the process changed", err, results)
	}

	// The Anthropic engine turned on: one argument more, a restart, and the proxy says it has the engine.
	plan, results, err = apply("parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n")
	t.Logf("anthropic on: %+v %+v", plan, results)
	if err != nil || results[0].Outcome != controller.ApplyChanged || !slices.Equal(engines(), []string{"codex", "anthropic"}) || !started().After(before) {
		t.Fatalf("anthropic on: %v, %+v, engines %v", err, results, engines())
	}
	withEngine, _ := svc.Job.Read()
	if args, _ := parts.JobArgs(withEngine); !slices.Equal(args[1:], []string{"model", "proxy", "serve", "--port", port, "--anthropic"}) {
		t.Fatalf("the job runs %q", args)
	}
	if !strings.Contains(string(withEngine), os.Getenv("PATH")) {
		t.Error("the job's environment did not survive the change")
	}

	// Another port it can listen on: the change holds, because it is asked where it now is.
	plan, results, err = apply("parts:\n  inference-proxy:\n    enabled: true\n    port: 7398\n    engines:\n      anthropic: {}\n")
	listening = "127.0.0.1:7398"
	t.Logf("another port: %+v %+v", plan, results)
	if err != nil || results[0].Outcome != controller.ApplyChanged || !slices.Equal(engines(), []string{"codex", "anthropic"}) {
		t.Fatalf("another port: %v, %+v, engines there %v", err, results, engines())
	}
	plan, results, err = apply("parts:\n  inference-proxy:\n    enabled: true\n    port: " + port + "\n    engines:\n      anthropic: {}\n")
	listening = addr
	if err != nil || results[0].Outcome != controller.ApplyChanged || !slices.Equal(engines(), []string{"codex", "anthropic"}) {
		t.Fatalf("back to the first port: %v, %+v, engines %v", err, results, engines())
	}
	withEngine, _ = svc.Job.Read()

	// A setting the proxy cannot start with (a port it may not listen on): the change is undone.
	brokenAt := time.Now()
	plan, results, err = apply("parts:\n  inference-proxy:\n    enabled: true\n    port: 1\n    engines:\n      anthropic: {}\n")
	t.Logf("broken port: %+v %+v (%v, after %v)", plan, results, err, time.Since(brokenAt).Round(time.Second))
	if err == nil || results[0].Outcome != controller.ApplyRolledBack {
		t.Fatalf("broken port: %v, %+v; want it rolled back", err, results)
	}
	if now, _ := svc.Job.Read(); string(now) != string(withEngine) || !slices.Equal(engines(), []string{"codex", "anthropic"}) {
		t.Fatalf("after the roll-back the job file or the running proxy is not what it was (engines %v)", engines())
	}

	// And off again: the file is back to the bytes it started with.
	if _, results, err = apply("parts:\n  inference-proxy:\n    enabled: true\n    engines:\n"); err != nil || results[0].Outcome != controller.ApplyChanged {
		t.Fatalf("engine off: %v, %+v", err, results)
	}
	if now, _ := svc.Job.Read(); string(now) != string(original) || !slices.Equal(engines(), []string{"codex"}) {
		t.Errorf("turning the engine off did not give the original job file back (engines %v)", engines())
	}
	if kept, err := os.ReadFile(filepath.Join(svc.Dir, controller.JobBeforeName)); err != nil || string(kept) != string(original) { // #nosec G304 -- test temp file
		t.Errorf("the job file from before is not kept: %v", err)
	}

	// The part turned off: launchd no longer has the job, nothing answers, and the file is untouched.
	plan, results, err = apply("parts:\n  inference-proxy:\n    enabled: false\n")
	t.Logf("off: %+v %+v", plan, results)
	if err != nil || results[0].Outcome != controller.ApplyStopped || engines() != nil {
		t.Fatalf("off: %v, %+v, still answers with %v", err, results, engines())
	}
	if loaded, _ := agent.Loaded(); loaded {
		t.Fatal("launchd still has the job after the part was turned off")
	}
	if now, _ := svc.Job.Read(); string(now) != string(original) {
		t.Error("stopping changed the job file")
	}
	// Launchd keeps it off: a plain load of the file is refused until it is turned on again.
	if out, loadErr := runLaunchctl("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), agent.PlistPath); loadErr == nil {
		t.Errorf("a job that was stopped could be loaded again behind aimux's back: %s", out)
		_, _ = runLaunchctl("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
	}
	// And on again: started from the same file, answering.
	plan, results, err = apply("parts:\n  inference-proxy:\n    enabled: true\n")
	t.Logf("on again: %+v %+v", plan, results)
	if err != nil || results[0].Outcome != controller.ApplyStarted || !slices.Equal(engines(), []string{"codex"}) {
		t.Fatalf("on again: %v, %+v, engines %v", err, results, engines())
	}
}
