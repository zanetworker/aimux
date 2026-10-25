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
	pid     int
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
func (f *fakeRunner) PID() int             { return f.pid }
func (f *fakeRunner) Cancel()              { f.cancels.Add(1) }
func (f *fakeRunner) Events(int64) []Event { return []Event{{Seq: 7}} }
func (f *fakeRunner) Close() error         { f.closed.Store(true); return nil }

type fakeRunStarter struct {
	block   chan struct{} // when set, start waits for it to close
	mu      sync.Mutex
	runners []*fakeRunner
	fail    error
	promptF func(ctx context.Context, text string) (string, error)
}

func (s *fakeRunStarter) start(ctx context.Context, session, cwd string) (Runner, error) {
	if s.block != nil {
		<-s.block // the adapter finishes starting regardless of ctx
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	r := &fakeRunner{pid: 4242 + len(s.runners), session: session, promptF: s.promptF}
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
	// Idle adapter, no turn: nothing to cancel, and the adapter is not told
	// (a cancel it kept would stop the next, unrelated turn).
	if err := m.Cancel("a"); !errors.Is(err, ErrNotRunning) || fs.runners[0].cancels.Load() != 0 {
		t.Fatalf("cancel of an idle session: %v, cancels=%d; want ErrNotRunning and no adapter call", err, fs.runners[0].cancels.Load())
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

func TestManager_Owns(t *testing.T) {
	parents := map[int]int{5001: 5000, 5000: 4242, 6001: 1}
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start, ParentPID: func(pid int) int { return parents[pid] }})
	_, _ = send(m, "a") // adapter pid 4242
	for pid, want := range map[int]bool{4242: true, 5000: true, 5001: true, 6001: false, 0: false} {
		if got := m.Owns(pid); got != want {
			t.Errorf("Owns(%d) = %v, want %v", pid, got, want)
		}
	}
	m.Close()
	if m.Owns(5001) {
		t.Fatal("still owns a process after Close")
	}
}

func TestManager_CancelDuringStartDoesNotPrompt(t *testing.T) {
	var prompts atomic.Int32
	fs := &fakeRunStarter{block: make(chan struct{}), promptF: func(context.Context, string) (string, error) {
		prompts.Add(1)
		return "end_turn", nil
	}}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := m.Send(ctx, "a", "/w", "hi", func(Event) {}); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel() // Ctrl-C while the adapter is still starting
	close(fs.block)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if prompts.Load() != 0 {
		t.Fatal("the message was sent after the user interrupted")
	}
	if !fs.runners[0].closed.Load() || m.Running("a") {
		t.Fatal("the runner that finished starting after the interrupt was kept")
	}
}

func TestManager_CloseDuringStartClosesTheRunner(t *testing.T) {
	var prompts atomic.Int32
	fs := &fakeRunStarter{block: make(chan struct{}), promptF: func(context.Context, string) (string, error) {
		prompts.Add(1)
		return "end_turn", nil
	}}
	m := NewManager(ManagerOptions{Start: fs.start})
	done := make(chan error)
	go func() { _, err := m.Send(context.Background(), "a", "/w", "hi", func(Event) {}); done <- err }()
	time.Sleep(20 * time.Millisecond)
	// Shutdown while the adapter is starting. This fake start ignores
	// cancellation, so Close (which waits for starts) returns once it is
	// released.
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	time.Sleep(20 * time.Millisecond)
	close(fs.block)
	<-closed
	if err := <-done; err == nil {
		t.Fatal("Send succeeded on a closed manager")
	}
	if prompts.Load() != 0 || !fs.runners[0].closed.Load() {
		t.Fatalf("prompts=%d closed=%v; want no prompt and the late runner closed", prompts.Load(), fs.runners[0].closed.Load())
	}
}

// hangingStart is an adapter that never answers initialize or load.
func hangingStart(ctx context.Context, session, cwd string) (Runner, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestManager_StartTimeout(t *testing.T) {
	m := NewManager(ManagerOptions{Start: hangingStart, StartTimeout: 50 * time.Millisecond})
	defer m.Close()
	done := make(chan error)
	go func() { _, err := m.Send(context.Background(), "a", "/w", "hi", func(Event) {}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want a start deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an adapter that never starts kept the session busy")
	}
	if m.Running("a") {
		t.Fatal("the failed start is still tracked, so the session stays busy")
	}
}

func TestManager_CancelWhileStarting(t *testing.T) {
	m := NewManager(ManagerOptions{Start: hangingStart})
	defer m.Close()
	done := make(chan error)
	go func() { _, err := m.Send(context.Background(), "a", "/w", "hi", func(Event) {}); done <- err }()
	for !m.Running("a") {
		time.Sleep(time.Millisecond)
	}
	if err := m.Cancel("a"); err != nil {
		t.Fatalf("cancel during startup: %v, want it to abort the start", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel did not stop a start that hangs")
	}
}

// Shutting down while an adapter is starting must stop that start before
// Close returns; once the process exits, nothing else would close it.
func TestManager_CloseCancelsAndWaitsForStartingAdapters(t *testing.T) {
	m := NewManager(ManagerOptions{Start: hangingStart})
	done := make(chan error, 1)
	go func() { _, err := m.Send(context.Background(), "a", "/w", "hi", func(Event) {}); done <- err }()
	for !m.Running("a") {
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel the adapter that was starting")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Send succeeded although the manager shut down during startup")
		}
	default:
		t.Fatal("Close returned before the starting adapter was dealt with")
	}
}

// The user continues the session in a terminal between two remote messages.
// The cached adapter still holds the old conversation, so it must be
// replaced by one that reloads the session.
func TestManager_ReloadsWhenTheSessionChangedElsewhere(t *testing.T) {
	var mu sync.Mutex
	fp := "v1"
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start, Fingerprint: func(string) string { mu.Lock(); defer mu.Unlock(); return fp }})
	defer m.Close()
	_, _ = send(m, "a")
	_, _ = send(m, "a") // nothing changed: reuse
	if fs.count() != 1 {
		t.Fatalf("started %d adapters with no outside change, want 1", fs.count())
	}
	mu.Lock()
	fp = "v2" // turns added from a terminal
	mu.Unlock()
	if _, err := send(m, "a"); err != nil {
		t.Fatal(err)
	}
	if fs.count() != 2 || !fs.runners[0].closed.Load() {
		t.Fatalf("started %d, first closed=%v; want a fresh adapter that reloads the session", fs.count(), fs.runners[0].closed.Load())
	}
}

// A cancel that lands after the adapter is ready but before the prompt is
// sent must stop the turn, not reach an idle adapter and be lost.
func TestManager_CancelledBeforePromptDoesNotPrompt(t *testing.T) {
	var prompts atomic.Int32
	fs := &fakeRunStarter{promptF: func(context.Context, string) (string, error) {
		prompts.Add(1)
		return "end_turn", nil
	}}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	_, _ = send(m, "a") // leaves a cached idle adapter
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Send(ctx, "a", "/w", "second", func(Event) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompted %d times, want only the first turn", prompts.Load())
	}
	if !m.Running("a") || fs.runners[0].closed.Load() {
		t.Fatal("a turn cancelled before its prompt must not throw away the healthy adapter")
	}
}

func TestManager_CancelReachesTheAdapterDuringAPrompt(t *testing.T) {
	inPrompt := make(chan struct{})
	release := make(chan struct{})
	fs := &fakeRunStarter{promptF: func(context.Context, string) (string, error) {
		close(inPrompt)
		<-release
		return "cancelled", nil
	}}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	go func() { _, _ = send(m, "a") }()
	<-inPrompt
	if err := m.Cancel("a"); err != nil || fs.runners[0].cancels.Load() != 1 {
		t.Fatalf("cancel during a prompt: %v, cancels=%d; want it forwarded to the agent", err, fs.runners[0].cancels.Load())
	}
	close(release)
}

func TestManager_CancelBetweenClaimAndPromptStopsTheTurn(t *testing.T) {
	var prompts atomic.Int32
	fs := &fakeRunStarter{promptF: func(context.Context, string) (string, error) { prompts.Add(1); return "end_turn", nil }}
	var armed atomic.Bool
	gate := make(chan struct{})
	reached := make(chan struct{})
	m := NewManager(ManagerOptions{Start: fs.start, Fingerprint: func(string) string {
		// Armed for the second turn only: the reuse check runs between
		// claiming the cached run and prompting it.
		if armed.CompareAndSwap(true, false) {
			close(reached)
			<-gate
		}
		return ""
	}})
	defer m.Close()
	_, _ = send(m, "a")
	prompts.Store(0)
	armed.Store(true)
	done := make(chan error)
	go func() { _, err := send(m, "a"); done <- err }()
	<-reached
	if err := m.Cancel("a"); err != nil {
		t.Fatalf("cancel of an accepted turn: %v", err)
	}
	close(gate)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if prompts.Load() != 0 || fs.runners[0].cancels.Load() != 0 {
		t.Fatalf("prompts %d, adapter cancels %d; want the turn stopped before the agent", prompts.Load(), fs.runners[0].cancels.Load())
	}
	if _, err := send(m, "a"); err != nil || prompts.Load() != 1 {
		t.Fatalf("next turn: %v, prompts %d; the cancel must not leak into it", err, prompts.Load())
	}
}

// Shutdown during a prompt: Close and the failing Send both retire the same
// entry. The reservation must be released once, without a race or panic.
func TestRunEntry_RetireIsSafeConcurrently(t *testing.T) {
	for i := 0; i < 50; i++ {
		var releases atomic.Int32
		e := &runEntry{run: &fakeRunner{}, release: func() { releases.Add(1) }}
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); e.retire() }()
		}
		wg.Wait()
		if releases.Load() != 1 {
			t.Fatalf("released %d times, want once", releases.Load())
		}
	}
}

// Close may retire an entry while its Send is replacing a stale adapter;
// the two must not touch the entry's runner unsynchronized (run with -race).
func TestManager_CloseDuringReloadIsSafe(t *testing.T) {
	fs := &fakeRunStarter{}
	var m *Manager
	var calls atomic.Int32
	m = NewManager(ManagerOptions{Start: fs.start, Fingerprint: func(string) string {
		if calls.Add(1) == 2 { // the reload check on the second send
			go m.Close()
			time.Sleep(50 * time.Millisecond)
			return "v2"
		}
		return "v1"
	}})
	if _, err := send(m, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := send(m, "a"); err == nil {
		t.Fatal("send during shutdown succeeded, want an error")
	}
	if fs.count() != 1 || !fs.runners[0].closed.Load() {
		t.Fatalf("started %d, first closed=%v; want no new adapter and the old one closed", fs.count(), fs.runners[0].closed.Load())
	}
}
