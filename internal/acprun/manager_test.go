package acprun

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRunner struct {
	session string
	closed  atomic.Bool
	promptF func(ctx context.Context, text string) (string, error)
	cancels atomic.Int32
}

func (f *fakeRunner) Prompt(ctx context.Context, text string, onEvent func(Event)) (string, error) {
	if f.promptF != nil {
		return f.promptF(ctx, text)
	}
	onEvent(Event{Seq: 1, Kind: KindUpdate})
	return "end_turn", nil
}
func (f *fakeRunner) Cancel()              { f.cancels.Add(1) }
func (f *fakeRunner) Events(int64) []Event { return []Event{{Seq: 7}} }
func (f *fakeRunner) Close() error         { f.closed.Store(true); return nil }

type fakeRunStarter struct {
	mu      sync.Mutex
	runners []*fakeRunner
	fail    error
	promptF func(ctx context.Context, text string) (string, error)
}

func (s *fakeRunStarter) start(ctx context.Context, session, cwd string) (Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	r := &fakeRunner{session: session, promptF: s.promptF}
	s.runners = append(s.runners, r)
	return r, nil
}

func (s *fakeRunStarter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runners)
}

func send(m *Manager, session string) (string, error) {
	return m.Send(context.Background(), session, "/w", "hi", func(Event) {})
}

func TestManager_ReusesRunForSameSession(t *testing.T) {
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	for i := 0; i < 3; i++ {
		if stop, err := send(m, "a"); err != nil || stop != "end_turn" {
			t.Fatalf("send %d: %q %v", i, stop, err)
		}
	}
	if fs.count() != 1 {
		t.Fatalf("started %d runs for one session, want 1", fs.count())
	}
}

func TestManager_RefusesSecondMessageWhileBusy(t *testing.T) {
	release := make(chan struct{})
	fs := &fakeRunStarter{promptF: func(ctx context.Context, text string) (string, error) { <-release; return "end_turn", nil }}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	done := make(chan error)
	go func() { _, err := send(m, "a"); done <- err }()
	for fs.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := send(m, "a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second send: %v, want ErrBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := send(m, "a"); err != nil {
		t.Fatalf("send after the turn ended: %v", err)
	}
}

func TestManager_StartFailureIsNotCached(t *testing.T) {
	fs := &fakeRunStarter{fail: errors.New("npx: not found")}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	if _, err := send(m, "a"); err == nil {
		t.Fatal("send with a failing start succeeded")
	}
	fs.fail = nil
	if _, err := send(m, "a"); err != nil {
		t.Fatalf("send after the start failure cleared: %v", err)
	}
}

func TestManager_FailedPromptDropsRun(t *testing.T) {
	fs := &fakeRunStarter{promptF: func(context.Context, string) (string, error) { return "", errors.New("adapter exited") }}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	if _, err := send(m, "a"); err == nil {
		t.Fatal("failed prompt reported success")
	}
	if !fs.runners[0].closed.Load() {
		t.Fatal("run with a failed prompt was not closed")
	}
	fs.promptF = nil
	if _, err := send(m, "a"); err != nil {
		t.Fatal(err)
	}
	if fs.count() != 2 {
		t.Fatalf("started %d runs, want a fresh one after the failure", fs.count())
	}
}

func TestManager_EvictsLeastRecentlyUsedIdleRun(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	tick := func() { mu.Lock(); now = now.Add(time.Minute); mu.Unlock() }
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start, MaxRuns: 2, Now: clock})
	defer m.Close()

	_, _ = send(m, "a")
	tick()
	_, _ = send(m, "b")
	tick()
	_, _ = send(m, "a") // a is now more recent than b
	tick()
	if _, err := send(m, "c"); err != nil {
		t.Fatal(err)
	}
	if !fs.runners[1].closed.Load() || fs.runners[0].closed.Load() {
		t.Fatalf("evicted a=%v b=%v; want b (least recently used) evicted", fs.runners[0].closed.Load(), fs.runners[1].closed.Load())
	}
	if got := m.Active(); len(got) != 2 {
		t.Fatalf("active runs %v, want 2", got)
	}
}

func TestManager_AllRunsBusyRefusesNewSession(t *testing.T) {
	release := make(chan struct{})
	fs := &fakeRunStarter{promptF: func(ctx context.Context, text string) (string, error) { <-release; return "end_turn", nil }}
	m := NewManager(ManagerOptions{Start: fs.start, MaxRuns: 1})
	defer m.Close()
	go func() { _, _ = send(m, "a") }()
	for fs.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := send(m, "b"); !errors.Is(err, ErrTooManyRuns) {
		t.Fatalf("got %v, want ErrTooManyRuns", err)
	}
	close(release)
}

func TestManager_ReapClosesIdleRuns(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start, IdleTimeout: 5 * time.Minute, Now: clock})
	defer m.Close()
	_, _ = send(m, "old")
	mu.Lock()
	now = now.Add(10 * time.Minute)
	mu.Unlock()
	_, _ = send(m, "new")
	if n := m.Reap(); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if !fs.runners[0].closed.Load() || fs.runners[1].closed.Load() {
		t.Fatal("reap closed the wrong run")
	}
}

func TestManager_CancelAndEvents(t *testing.T) {
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	if err := m.Cancel("a"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel without a run: %v, want ErrNotRunning", err)
	}
	if _, err := m.Events("a", 0); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("events without a run: %v, want ErrNotRunning", err)
	}
	_, _ = send(m, "a")
	if err := m.Cancel("a"); err != nil || fs.runners[0].cancels.Load() != 1 {
		t.Fatalf("cancel: %v, cancels=%d", err, fs.runners[0].cancels.Load())
	}
	if ev, err := m.Events("a", 0); err != nil || len(ev) != 1 || ev[0].Seq != 7 {
		t.Fatalf("events: %v %v", ev, err)
	}
}

func TestManager_CloseClosesRunsAndRefuses(t *testing.T) {
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start})
	_, _ = send(m, "a")
	m.Close()
	if !fs.runners[0].closed.Load() {
		t.Fatal("run not closed by Manager.Close")
	}
	if _, err := send(m, "a"); err == nil {
		t.Fatal("send after Close succeeded")
	}
}
