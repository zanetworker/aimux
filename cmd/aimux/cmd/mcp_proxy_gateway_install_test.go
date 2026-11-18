package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// slowLaunchd is launchd as it behaves: bootout returns before the job is
// unloaded, and a bootstrap in that window fails with exit status 5.
type slowLaunchd struct {
	loaded, unloading bool
	lag               int
	calls             []string
}

func (l *slowLaunchd) run(_ string, args ...string) ([]byte, error) {
	l.calls = append(l.calls, args[0])
	if l.unloading {
		if l.lag--; l.lag <= 0 {
			l.unloading, l.loaded = false, false
		}
	}
	switch args[0] {
	case "print":
		if l.loaded {
			return []byte("state = running\n\tpid = " + launchdJobPID + "\n"), nil
		}
		return []byte("Could not find service"), errors.New("exit status 113")
	case "bootout":
		if !l.loaded {
			return []byte("No such process"), errors.New("exit status 3")
		}
		l.unloading = true
		return nil, nil
	case "bootstrap":
		if l.loaded {
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		}
		l.loaded = true
		return nil, nil
	}
	return nil, errors.New("unexpected launchctl " + args[0])
}

// A re-run from another binary points the job at it and restarts it, even
// while launchd still unloads the old one.
func TestInstallGatewayAgent_SwitchesBinaryAndRestarts(t *testing.T) {
	home := t.TempDir()
	l := &slowLaunchd{loaded: true, lag: 3}
	a := gatewayAgent(home)
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	a.Binary = "/old/aimux"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Dir(a.PlistPath), 0o700))
	must(os.WriteFile(a.PlistPath, a.Plist(), 0o600))

	a.Binary = "/new/aimux"
	var out bytes.Buffer
	must(installGatewayAgent(context.Background(), a, &out, freshWorker{}, time.Second))
	if got := mcpproxy.ReadPlistBinary(a.PlistPath); got != "/new/aimux" {
		t.Fatalf("plist program %q, want /new/aimux", got)
	}
	if !l.loaded {
		t.Fatalf("job not loaded after install (calls %v)", l.calls)
	}
	if !strings.Contains(out.String(), "was /old/aimux") {
		t.Fatalf("output does not name the old binary: %s", out.String())
	}

	// Same binary again: installed, no switch reported.
	out.Reset()
	must(installGatewayAgent(context.Background(), a, &out, freshWorker{}, time.Second))
	if strings.Contains(out.String(), "was ") || !strings.Contains(out.String(), "Installed com.aimux.tool-gateway") {
		t.Fatalf("output: %s", out.String())
	}
}

// launchdJobPID is the pid the fake launchd reports for the loaded job.
const launchdJobPID = "4242"

// freshWorker is the launchd job's worker, answering as started just now.
type freshWorker struct{}

func (freshWorker) Status(context.Context) (toolgateway.ControlStatus, error) {
	return toolgateway.ControlStatus{StartedAt: time.Now(), PID: 4242}, nil
}

// A new binary whose worker never answers (or only the old worker, started
// before the install) is undone: the old plist is back and loaded.
func TestInstallGatewayAgent_RollsBackAWorkerThatNeverAnswers(t *testing.T) {
	for name, st := range map[string]gatewayStatuser{
		"not running": fakeGatewayStatus{err: toolgateway.ErrGatewayNotRunning},
		"old worker":  fakeGatewayStatus{st: toolgateway.ControlStatus{StartedAt: time.Now().Add(-time.Hour)}},
	} {
		home := t.TempDir()
		l := &slowLaunchd{loaded: true, lag: 1}
		a := gatewayAgent(home)
		a.Run, a.Sleep = l.run, func(time.Duration) {}
		a.Binary = "/old/aimux"
		if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(a.PlistPath, a.Plist(), 0o644); err != nil { //nolint:gosec // a plist is 0644
			t.Fatal(err)
		}
		a.Binary = "/new/aimux"
		err := installGatewayAgent(context.Background(), a, &bytes.Buffer{}, st, time.Second)
		if err == nil || !strings.Contains(err.Error(), "/old/aimux") {
			t.Fatalf("%s: err %v, want the switch undone", name, err)
		}
		if got := mcpproxy.ReadPlistBinary(a.PlistPath); got != "/old/aimux" || !l.loaded {
			t.Fatalf("%s: plist program %q loaded %v, want the old one back and loaded", name, got, l.loaded)
		}
	}
}

// stallingWorker accepts the status call and never answers.
type stallingWorker struct{}

func (stallingWorker) Status(ctx context.Context) (toolgateway.ControlStatus, error) {
	<-ctx.Done()
	return toolgateway.ControlStatus{}, ctx.Err()
}

// The wait is one deadline: a worker that stalls on every status call does
// not stretch it by the per-call timeout.
func TestGatewayAnswers_OneDeadline(t *testing.T) {
	a := gatewayAgent(t.TempDir())
	a.Sleep = func(time.Duration) {}
	start := time.Now()
	if gatewayAnswers(context.Background(), stallingWorker{}, a, time.Now(), 300*time.Millisecond) == mcpproxy.Ready {
		t.Fatal("a stalled worker answered")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v for a 300ms budget", d)
	}
}

// A first install whose worker never answers is removed again: no
// KeepAlive job left restarting a broken worker at every login.
func TestInstallGatewayAgent_FirstInstallThatNeverAnswersIsRemoved(t *testing.T) {
	home := t.TempDir()
	l := &slowLaunchd{}
	a := gatewayAgent(home)
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	a.Binary = "/new/aimux"
	err := installGatewayAgent(context.Background(), a, &bytes.Buffer{}, fakeGatewayStatus{err: toolgateway.ErrGatewayNotRunning}, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("err %v, want the new job removed", err)
	}
	// The fake launchd unloads lazily, as launchd does: the last call is the
	// bootout that removes the job.
	if _, serr := os.Stat(a.PlistPath); !os.IsNotExist(serr) || l.calls[len(l.calls)-1] != "bootout" {
		t.Fatalf("plist stat %v, calls %v: want the plist gone and the job booted out", serr, l.calls)
	}
}

// After a rollback the old worker must answer before install says it runs
// again; when that could not be checked, install says only the plist is
// back.
func TestInstallGatewayAgent_RollbackReportsWhatIsTrue(t *testing.T) {
	home := t.TempDir()
	l := &slowLaunchd{loaded: true, lag: 1}
	a := gatewayAgent(home)
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	a.Binary = "/old/aimux"
	if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.PlistPath, a.Plist(), 0o644); err != nil { //nolint:gosec // a plist is 0644
		t.Fatal(err)
	}
	a.Binary = "/new/aimux"
	err := installGatewayAgent(context.Background(), a, &bytes.Buffer{}, fakeGatewayStatus{err: toolgateway.ErrGatewayNotRunning}, 300*time.Millisecond)
	// The first check used the whole shared deadline: the rollback is
	// reported as not verified, never as running again.
	if err == nil || strings.Contains(err.Error(), "runs /old/aimux again") || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("err %v", err)
	}
}

// The install check and the rollback re-check share one deadline: two
// calls together wait at most about wait.
func TestGatewayReadyWithin_OneDeadlineForBoth(t *testing.T) {
	a := gatewayAgent(t.TempDir())
	a.Sleep = func(time.Duration) {}
	ready := gatewayReadyWithin(context.Background(), stallingWorker{}, a, 400*time.Millisecond)
	start := time.Now()
	first := ready(time.Now())  // the install's check
	second := ready(time.Now()) // the rollback's re-check
	if first == mcpproxy.Ready || second == mcpproxy.Ready {
		t.Fatal("a stalled worker answered")
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("both checks took %v for a 400ms budget", d)
	}
}

// A worker answering on the socket that is not the launchd job's (a
// foreground gateway serve) does not make the install ready.
func TestGatewayAnswers_OnlyTheJobsWorker(t *testing.T) {
	l := &slowLaunchd{loaded: true}
	a := gatewayAgent(t.TempDir())
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	foreground := fakeGatewayStatus{st: toolgateway.ControlStatus{StartedAt: time.Now(), PID: 999}}
	if gatewayAnswers(context.Background(), foreground, a, time.Now().Add(-time.Minute), 300*time.Millisecond) == mcpproxy.Ready {
		t.Fatal("a foreground worker counted as the launchd job's")
	}
	if gatewayAnswers(context.Background(), freshWorker{}, a, time.Now().Add(-time.Minute), 300*time.Millisecond) != mcpproxy.Ready {
		t.Fatal("the job's own worker did not count")
	}
}

// An older worker (no pid in its status) answering after a rollback is
// reported as not verified, not as down.
func TestGatewayAnswers_LegacyWorkerIsUnknown(t *testing.T) {
	l := &slowLaunchd{loaded: true}
	a := gatewayAgent(t.TempDir())
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	legacy := fakeGatewayStatus{st: toolgateway.ControlStatus{StartedAt: time.Now()}}
	if got := gatewayAnswers(context.Background(), legacy, a, time.Now().Add(-time.Minute), 200*time.Millisecond); got != mcpproxy.ReadyUnknown {
		t.Fatalf("legacy worker: %v, want ReadyUnknown", got)
	}
	// No time left at all: not checked, so unknown.
	if got := gatewayAnswers(context.Background(), legacy, a, time.Now(), 0); got != mcpproxy.ReadyUnknown {
		t.Fatalf("no time: %v, want ReadyUnknown", got)
	}
}

// Ctrl-C during the wait stops it and still rolls back.
func TestInstallGatewayAgent_InterruptRollsBack(t *testing.T) {
	home := t.TempDir()
	l := &slowLaunchd{loaded: true, lag: 1}
	a := gatewayAgent(home)
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	a.Binary = "/old/aimux"
	if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.PlistPath, a.Plist(), 0o644); err != nil { //nolint:gosec // a plist is 0644
		t.Fatal(err)
	}
	a.Binary = "/new/aimux"
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	err := installGatewayAgent(ctx, a, &bytes.Buffer{}, stallingWorker{}, time.Minute)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v after %v, want a quick rollback", err, time.Since(start))
	}
	if got := mcpproxy.ReadPlistBinary(a.PlistPath); got != "/old/aimux" {
		t.Fatalf("plist program %q after the interrupt, want the old one", got)
	}
}

// A launchctl print that never returns cannot hold the wait past its
// deadline, nor past Ctrl-C.
func TestGatewayAnswers_StalledPIDLookupIsBounded(t *testing.T) {
	a := gatewayAgent(t.TempDir())
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	a.Run = func(string, ...string) ([]byte, error) { <-block; return nil, nil }
	a.Sleep = func(time.Duration) {}
	start := time.Now()
	if got := gatewayAnswers(context.Background(), freshWorker{}, a, time.Now().Add(-time.Minute), 300*time.Millisecond); got == mcpproxy.Ready {
		t.Fatal("ready without a pid from launchd")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v on a stalled launchctl for a 300ms budget", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start = time.Now()
	gatewayAnswers(ctx, freshWorker{}, a, time.Now().Add(-time.Minute), time.Minute)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Ctrl-C took %v to stop a stalled launchctl", d)
	}
}
