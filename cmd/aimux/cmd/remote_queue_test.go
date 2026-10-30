package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/trace"
)

// recordingInput counts every call into the terminal; it would type.
type recordingInput struct {
	status, send, submit atomic.Int32
	inUse                bool // Status answers mac_in_use
}

func (r *recordingInput) Status(context.Context, string, string) terminput.Status {
	r.status.Add(1)
	if r.inUse {
		return terminput.Status{State: "idle", Reason: "mac_in_use"}
	}
	return terminput.Status{State: "idle", CanType: true}
}

func (r *recordingInput) Send(context.Context, string, string, string) error {
	r.send.Add(1)
	return nil
}

func (r *recordingInput) Submit(context.Context, string, string) error {
	r.submit.Add(1)
	return nil
}

// queueRig is the queue as remote serve wires it, for a session open in a
// terminal (held by this test's own process in Claude's registry).
func queueRig(t *testing.T, tcfg config.TerminalInputConfig) (*chatqueue.Store, *recordingInput, string) {
	t.Helper()
	return queueRigWith(t, tcfg, &recordingInput{}, nil)
}

func queueRigWith(t *testing.T, tcfg config.TerminalInputConfig, input *recordingInput, noteChanged func(string)) (*chatqueue.Store, *recordingInput, string) {
	t.Helper()
	home := t.TempDir()
	registry := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":"open","status":"idle"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(registry, "1.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := remoteapi.NewBroker(remoteapi.BrokerDeps{
		Check: func(string) error { return acprun.ErrOpenInTerminal },
	}, remoteapi.BrokerOptions{})
	projects := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(filepath.Join(projects, "-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "-w", "open.jsonl"), []byte(`{"cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := &remoteapi.LocalIndex{RegistryDir: registry, ProjectsDir: projects, Broker: broker,
		Discover: func(history.DiscoverOpts, string) ([]history.Session, error) {
			return []history.Session{{ID: "open", Provider: "claude", LastActive: time.Now()}}, nil
		},
		ParseTrace: func(string) ([]trace.Turn, error) { return nil, nil },
	}
	store, d, err := openChatQueue(home, tcfg, broker, index, input, nil, nil, noteChanged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	if _, err := store.Add("open", "run the tests", ""); err != nil {
		t.Fatal(err)
	}
	d.Kick()
	return store, input, home
}

// With terminal input off, remote serve's queue never reads or types into
// a terminal session: the queue pauses with its fixed reason instead.
func TestOpenChatQueue_TerminalInputOffNeverTypes(t *testing.T) {
	store, input, home := queueRig(t, config.TerminalInputConfig{Enabled: false})
	deadline := time.Now().Add(3 * time.Second)
	for store.Get("open").State != chatqueue.QueuePaused {
		if time.Now().After(deadline) {
			t.Fatalf("queue %+v, want it paused", store.Get("open"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if q := store.Get("open"); q.Reason != chatqueue.ReasonDisabled {
		t.Fatalf("reason %q, want %q", q.Reason, chatqueue.ReasonDisabled)
	}
	if n := input.status.Load() + input.send.Load() + input.submit.Load(); n != 0 {
		t.Fatalf("terminal input off: %d calls into the terminal (status %d, send %d, submit %d)",
			n, input.status.Load(), input.send.Load(), input.submit.Load())
	}
	fi, err := os.Stat(filepath.Join(home, ".aimux", "remote-queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("queue file mode %v, want 0600", fi.Mode().Perm())
	}
}

// The control: with terminal input on, the same queue reads the terminal.
func TestOpenChatQueue_TerminalInputOnReadsTheTerminal(t *testing.T) {
	_, input, _ := queueRig(t, config.TerminalInputConfig{Enabled: true})
	deadline := time.Now().Add(3 * time.Second)
	for input.status.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("terminal input on: the queue never read the terminal")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A queue file the server cannot read stops remote serve with its path.
func TestOpenChatQueue_UnreadableFileIsAnError(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".aimux", "remote-queue.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := openChatQueue(home, config.TerminalInputConfig{}, nil, nil, &recordingInput{}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err %v, want one naming %s", err, path)
	}
}

// The dispatcher's waiting line reaches the hook remote serve publishes
// queue events from.
func TestOpenChatQueue_NoteChangeReachesTheHook(t *testing.T) {
	heard := make(chan string, 8)
	queueRigWith(t, config.TerminalInputConfig{Enabled: true}, &recordingInput{inUse: true}, func(s string) { heard <- s })
	select {
	case s := <-heard:
		if s != "open" {
			t.Fatalf("note changed for %q, want open", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting line never reached the hook")
	}
}
