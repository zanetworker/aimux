package remoteapp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/terminput"
)

// idleRunner is an adapter run that answers at once; its PID is given.
type idleRunner struct{ pid int }

func (r idleRunner) PID() int { return r.pid }
func (r idleRunner) Prompt(context.Context, string, func(acprun.Event)) (string, error) {
	return "end_turn", nil
}
func (r idleRunner) Cancel()                     {}
func (r idleRunner) Events(int64) []acprun.Event { return nil }
func (r idleRunner) Close() error                { return nil }

// cachedLive is a live-sessions read whose holders the test sets: a cache
// that may still list a process after it stopped holding the session.
type cachedLive struct {
	mu sync.Mutex
	hs []harness.Holder
}

func (c *cachedLive) set(hs ...harness.Holder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hs = hs
}

func (c *cachedLive) Holders(harness.Mode) ([]harness.Holder, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]harness.Holder(nil), c.hs...), nil
}

// deadPID is the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func handoffRig(t *testing.T, adapterPID int) (*remoteapi.Handoff, *cachedLive) {
	t.Helper()
	manager := acprun.NewManager(acprun.ManagerOptions{
		MaxRuns:     2,
		Fingerprint: func(string) string { return "" },
		Start: func(context.Context, string, string) (acprun.Runner, error) {
			return idleRunner{pid: adapterPID}, nil
		},
	})
	t.Cleanup(manager.Close)
	if _, err := manager.Send(context.Background(), "s1", t.TempDir(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	live := &cachedLive{}
	broker := remoteapi.NewBroker(remoteapi.BrokerDeps{}, remoteapi.BrokerOptions{})
	index := &remoteapi.LocalIndex{Broker: broker, Owned: manager.Owns, Running: manager.Running,
		Providers: []remoteapi.Provider{{Live: live}}}
	h := buildHandoff(config.TerminalInputConfig{Enabled: true}, []terminput.Driver{fakeITerm2{}}, handoffDeps{
		index: index, broker: broker, manager: manager,
		resume: func(agent.Agent) *exec.Cmd { return exec.Command("/bin/echo") },
		open:   func(context.Context, string, []string, string) error { return nil },
	})
	if h == nil {
		t.Fatal("no handoff")
	}
	return h, live
}

func stateOf(t *testing.T, h *remoteapi.Handoff, id string) string {
	t.Helper()
	st, err := h.State(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The idle run's agent still listed as the holder after Drop (a cached
// read) is not the terminal: only a new holder makes the session
// open_in_terminal.
func TestHandoffState_DroppedRunsHolderDoesNotCount(t *testing.T) {
	self := os.Getpid() // alive: only the exclusion keeps it out
	h, live := handoffRig(t, self)
	live.set(harness.Holder{PID: self, SessionID: "s1"})
	if got := stateOf(t, h, "s1"); got != remoteapi.StateIdle {
		t.Fatalf("before drop: %s, want idle (aimux's own run)", got)
	}
	if err := h.Drop("s1"); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, h, "s1"); got != remoteapi.StateIdle {
		t.Fatalf("after drop with the stale holder: %s, want idle", got)
	}
	live.set(harness.Holder{PID: self, SessionID: "s1"}, harness.Holder{PID: os.Getppid(), SessionID: "s1"})
	if got := stateOf(t, h, "s1"); got != remoteapi.StateOpenInTerminal {
		t.Fatalf("with a new holder: %s, want open_in_terminal", got)
	}
}

// A holder whose process is gone is not open in a terminal.
func TestHandoffState_DeadHolderDoesNotCount(t *testing.T) {
	h, live := handoffRig(t, deadPID(t))
	live.set(harness.Holder{PID: deadPID(t), SessionID: "s2"})
	if got := stateOf(t, h, "s2"); got != remoteapi.StateIdle {
		t.Fatalf("dead holder: %s, want idle", got)
	}
	live.set(harness.Holder{PID: os.Getppid(), SessionID: "s2"})
	if got := stateOf(t, h, "s2"); got != remoteapi.StateOpenInTerminal {
		t.Fatalf("live holder: %s, want open_in_terminal", got)
	}
}

// targetRig is the handoff's Target over a seeded transcript and resume.
func targetRig(t *testing.T, resume func(agent.Agent) *exec.Cmd) func() error {
	t.Helper()
	home := t.TempDir()
	seedClaudeSession(t, home, "s1", t.TempDir(), "fix the build")
	manager := acprun.NewManager(acprun.ManagerOptions{Fingerprint: func(string) string { return "" }})
	t.Cleanup(manager.Close)
	index := &remoteapi.LocalIndex{Providers: []remoteapi.Provider{{Store: &provider.Claude{ProjectsDir: filepath.Join(home, ".claude", "projects")}}}}
	d := handoffDeps{index: index, manager: manager, resume: resume}
	return func() error { _, _, _, err := d.target(context.Background(), "s1"); return err }
}

// A resume command that failed to resolve its binary, or names it by a
// relative path, never reaches the shell line.
func TestHandoffTarget_RefusesUnresolvedBinary(t *testing.T) {
	for name, resume := range map[string]func(agent.Agent) *exec.Cmd{
		"not found": func(a agent.Agent) *exec.Cmd {
			return exec.Command("aimux-no-such-binary-xyz", "--resume", a.SessionID) // #nosec G204 -- built, never run
		},
		"relative": func(a agent.Agent) *exec.Cmd { return exec.Command("bin/claude", "--resume", a.SessionID) }, // #nosec G204 -- built, never run
	} {
		if err := targetRig(t, resume)(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	err := targetRig(t, func(a agent.Agent) *exec.Cmd { return exec.Command("/bin/echo", "--resume", a.SessionID) })() // #nosec G204 -- built, never run
	if err != nil {
		t.Fatalf("absolute binary: %v", err)
	}
}
