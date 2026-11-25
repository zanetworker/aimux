//go:build launchdlive

package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// The whole upgrade against the real launchd, on a job of the test's own: a real model proxy on a spare port is
// moved to its own copy, found current, and given a program that cannot start. No installed service is touched.
// Run with: go test -tags launchdlive ./cmd/aimux/cmd/ -run TestUpgradeLive -v
func TestUpgradeLive_AgainstLaunchd(t *testing.T) {
	const label, addr, port = "com.aimux.upgrade-livetest", "127.0.0.1:7397", "7397"
	tmp := t.TempDir()
	build := func(version string) string {
		out := filepath.Join(tmp, "aimux-"+version)
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", out, "../") // #nosec G204 -- fixed arguments
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", version, err, b)
		}
		return out
	}
	first, second := build("livetest-1"), build("livetest-2")
	broken := filepath.Join(tmp, "broken")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { // #nosec G306 -- a program must be executable
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	agent := mcpproxy.Agent{
		Label: label, PlistPath: filepath.Join(tmp, label+".plist"), Binary: first, LogDir: filepath.Join(tmp, "logs"),
		Args: []string{"model", "proxy", "serve", "--port", port}, Run: runLaunchctl, UID: os.Getuid(),
		Env: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home},
	}
	if err := os.MkdirAll(agent.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The proxy writes a request history for its port under the real home; remove it only if this test made it.
	history := filepath.Join(home, ".aimux", "logs", "model-proxy", "requests-"+port+".jsonl")
	_, statErr := os.Stat(history)
	madeHere := errors.Is(statErr, os.ErrNotExist)
	t.Cleanup(func() {
		if t.Failed() {
			// The job's own output and launchd's view of it say why a service did not come back.
			for _, name := range []string{"stdout.log", "stderr.log"} {
				b, _ := os.ReadFile(filepath.Join(agent.LogDir, name)) // #nosec G304 -- test temp file
				t.Logf("%s:\n%s", name, b)
			}
			out, _ := runLaunchctl("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
			t.Logf("launchctl print:\n%s", out)
			if m := regexp.MustCompile(`pid = (\d+)`).FindSubmatch(out); m != nil {
				ps, _ := exec.Command("ps", "-o", "pid,stat,etime,command", "-p", string(m[1])).CombinedOutput() // #nosec G204 -- a pid launchd printed
				t.Logf("ps:\n%s", ps)
			}
		}
		_ = agent.Uninstall()
		if madeHere {
			_ = os.Remove(history)
		}
	})
	if err := agent.Install(); err != nil {
		t.Fatalf("install the test job: %v", err)
	}
	env := statusEnv{modelAddr: addr, plistDir: tmp}
	svc := controller.UpgradeService{
		ID: "model-proxy", Name: "model proxy", Dir: filepath.Join(tmp, "services", "model-proxy"),
		Job: launchdJob{agent: agent}, Probe: env.probe("model proxy", addr, label, "", env.readModelStatus),
	}
	running := func() string { return svc.Probe.Run(context.Background()).Build }
	deadline := time.Now().Add(20 * time.Second)
	for running() != "livetest-1" {
		if time.Now().After(deadline) {
			t.Fatalf("the test job never answered on its first build (answers %q)", running())
		}
		time.Sleep(200 * time.Millisecond)
	}
	upgrade := func(source, version string, wait time.Duration) controller.UpgradeResult {
		return controller.UpgradeServices(context.Background(), []controller.UpgradeService{svc},
			controller.UpgradeOptions{Source: source, Build: version, Wait: wait})[0]
	}
	own := filepath.Join(svc.Dir, controller.ServiceProgram)

	got := upgrade(second, "livetest-2", 0)
	t.Logf("first run: %+v", got)
	if got.Outcome != controller.UpgradeDone || got.From != "livetest-1" || got.MovedFrom != first || running() != "livetest-2" {
		t.Fatalf("first run %+v, running %q", got, running())
	}
	if p := mcpproxy.ReadPlistBinary(agent.PlistPath); p != own {
		t.Fatalf("the job runs %q, want %q", p, own)
	}

	if _, err := os.Stat(own + ".prev"); err != nil {
		t.Fatalf("the file the job ran before the move was not kept: %v", err)
	}

	got = upgrade(second, "livetest-2", 0)
	t.Logf("second run: %+v", got)
	if got.Outcome != controller.UpgradeCurrent {
		t.Fatalf("second run %+v, want it left alone", got)
	}

	// The default wait, as the command uses.
	brokenAt := time.Now()
	got = upgrade(broken, "broken", 0)
	t.Logf("broken build: %+v (after %v)", got, time.Since(brokenAt).Round(time.Second))
	if got.Outcome != controller.UpgradeFailed || !got.Restored || running() != "livetest-2" {
		t.Fatalf("broken build %+v, running %q; want the previous program back", got, running())
	}

	got = upgrade(first, "livetest-1", 0)
	t.Logf("in-place upgrade: %+v", got)
	if got.Outcome != controller.UpgradeDone || got.MovedFrom != "" || running() != "livetest-1" {
		t.Fatalf("in-place upgrade %+v, running %q", got, running())
	}
	if _, err := os.Stat(own + ".prev"); err != nil {
		t.Fatalf("the replaced build was not kept: %v", err)
	}
}
