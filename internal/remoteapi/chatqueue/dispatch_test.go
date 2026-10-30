package chatqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
)

// fakeTerm is the terminal side: what Claude's registry, the screen and the
// session's history say, and what was typed. It never touches a terminal.
type fakeTerm struct {
	mu        sync.Mutex
	open      map[string]string // session -> registry status; absent: claude exited
	status    terminput.Status
	sendErr   error
	sends     []string
	onSend    func()
	submitErr error
	submits   []string
	turns     []Turn
	turnsErr  error
	calls     int // Status and Turns calls: polling
	onStatus  func()
}

func (f *fakeTerm) Open(session string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.open[session]
	return st, ok
}

func (f *fakeTerm) Status(_ context.Context, _, _ string) terminput.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.onStatus != nil {
		f.onStatus()
	}
	return f.status
}

func (f *fakeTerm) Send(_ context.Context, _, text, _ string) error {
	f.mu.Lock()
	f.sends = append(f.sends, text)
	hook, err := f.onSend, f.sendErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeTerm) Submit(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, text)
	return f.submitErr
}

func (f *fakeTerm) Turns(context.Context, string) ([]Turn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]Turn(nil), f.turns...), f.turnsErr
}

// fakeACP is the broker: turns start running and end when the test says.
type fakeACP struct {
	busy     bool
	startErr error
	starts   []string
	keys     []string
	running  map[string]bool
	failed   map[string]bool
	onStart  func()
}

func (a *fakeACP) Busy(string) bool { return a.busy }

func (a *fakeACP) Start(_, text, key string) (string, error) {
	if a.onStart != nil {
		a.onStart()
	}
	if a.startErr != nil {
		return "", a.startErr
	}
	a.starts = append(a.starts, text)
	a.keys = append(a.keys, key)
	turn := fmt.Sprintf("turn%d", len(a.starts))
	if a.running == nil {
		a.running = map[string]bool{}
	}
	a.running[turn] = true
	return turn, nil
}

func (a *fakeACP) TurnEnded(_, turn string) (bool, bool) { return !a.running[turn], a.failed[turn] }

func (a *fakeACP) end(turn string, failed bool) {
	delete(a.running, turn)
	if failed {
		if a.failed == nil {
			a.failed = map[string]bool{}
		}
		a.failed[turn] = true
	}
}

type fakeAudit struct{ entries []devicelogin.AuditEntry }

func (a *fakeAudit) Write(e devicelogin.AuditEntry) { a.entries = append(a.entries, e) }

type rig struct {
	t     *testing.T
	store *Store
	path  string
	term  *fakeTerm
	acp   *fakeACP
	audit *fakeAudit
	now   time.Time
	d     *Dispatcher
}

var free = terminput.Status{State: "idle", CanType: true}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, now: t0, term: &fakeTerm{open: map[string]string{}, status: free}, acp: &fakeACP{}, audit: &fakeAudit{}}
	r.path = filepath.Join(t.TempDir(), "remote-queue.json")
	r.store = openAt(t, r.path, false)
	r.d = r.dispatcher(true)
	return r
}

func (r *rig) dispatcher(terminalOn bool) *Dispatcher {
	return New(Config{
		Store: r.store, ACP: r.acp, Terminal: r.term, TerminalEnabled: terminalOn, Audit: r.audit,
		Now: func() time.Time { return r.now },
	})
}

func (r *rig) step() { r.t.Helper(); r.d.step(context.Background()) }

// settle steps twice, idleFor apart: what a first send needs from an idle
// session.
func (r *rig) settle() {
	r.t.Helper()
	r.step()
	r.now = r.now.Add(idleFor)
	r.step()
}

func (r *rig) add(session, text string) Item {
	r.t.Helper()
	it := mustAdd(r.t, r.store, session, text, "key-"+text)
	return it
}

func (r *rig) wantQueue(session, state, reason string) Queue {
	r.t.Helper()
	q := r.store.Get(session)
	if q.State != state || q.Reason != reason {
		r.t.Fatalf("queue = %s %q, want %s %q", q.State, q.Reason, state, reason)
	}
	return q
}

func (r *rig) wantFirst(session, text, state string) {
	r.t.Helper()
	q := r.store.Get(session)
	if len(q.Items) == 0 || q.Items[0].Text != text || q.Items[0].State != state {
		r.t.Fatalf("items = %+v, want first %q %s", q.Items, text, state)
	}
}

func (r *rig) wantSends(want ...string) {
	r.t.Helper()
	if got := strings.Join(r.term.sends, "|"); got != strings.Join(want, "|") {
		r.t.Fatalf("terminal sends = %q, want %q", r.term.sends, want)
	}
}

// openTerm puts session in a terminal, its history holding one turn of the
// owner's from before anything was queued.
func (r *rig) openTerm(session string) {
	r.term.open[session] = "idle"
	r.term.turns = []Turn{{Time: t0.Add(-time.Hour), User: "earlier"}}
}

func (r *rig) turn(user string) { r.term.turns = append(r.term.turns, Turn{Time: r.now, User: user}) }

// --- ACP (sessions started from the phone) ---

func TestACPSendsOneItemPerTurn(t *testing.T) {
	r := newRig(t)
	a := r.add("s", "first")
	r.add("s", "second")
	r.acp.busy = true // a turn the phone sent directly is running
	r.step()
	if len(r.acp.starts) != 0 {
		t.Fatalf("started %q while busy", r.acp.starts)
	}
	r.acp.busy = false
	r.step()
	if strings.Join(r.acp.starts, "|") != "first" || r.acp.keys[0] != "queue-"+a.ID {
		t.Fatalf("starts = %q keys %q", r.acp.starts, r.acp.keys)
	}
	r.wantFirst("s", "first", StateAnswering)
	for range 3 {
		r.step()
	}
	if len(r.acp.starts) != 1 {
		t.Fatalf("second item sent before the first's turn ended: %q", r.acp.starts)
	}
	r.acp.end("turn1", false)
	r.step()
	r.wantFirst("s", "second", StateWaiting)
	r.step()
	if strings.Join(r.acp.starts, "|") != "first|second" {
		t.Fatalf("starts = %q", r.acp.starts)
	}
	r.acp.end("turn2", false)
	r.step()
	if q := r.wantQueue("s", QueueRunning, ""); len(q.Items) != 0 {
		t.Fatalf("items left: %+v", q.Items)
	}
	// ACP queues never pause for idling between turns.
	r.now = r.now.Add(time.Hour)
	r.step()
	r.wantQueue("s", QueueRunning, "")
}

func TestACPTurnErrorPausesAfterDroppingTheItem(t *testing.T) {
	r := newRig(t)
	r.add("s", "first")
	r.add("s", "second")
	r.step()
	r.acp.end("turn1", true)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonTurnFailed)
	r.wantFirst("s", "second", StateWaiting) // the sent item is never sent again
	r.step()
	if len(r.acp.starts) != 1 {
		t.Fatalf("starts = %q", r.acp.starts)
	}
}

func TestACPStartErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string // "" waits
	}{
		"busy (not free yet)": {ErrNotFree, ""},
		"not running":         {ErrNotRunning, ReasonNotRunning},
		"other":               {errors.New("adapter died"), ReasonSendFailed},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.add("s", "first")
			r.acp.startErr = tc.err
			r.step()
			r.wantFirst("s", "first", StateWaiting) // nothing was sent
			if tc.reason == "" {
				r.wantQueue("s", QueueRunning, "")
				r.acp.startErr = nil
				r.step()
				if len(r.acp.starts) != 1 {
					t.Fatalf("starts = %q after the session freed", r.acp.starts)
				}
				return
			}
			r.wantQueue("s", QueuePaused, tc.reason)
		})
	}
}

func TestACPOwnerDeletesTheItemInFlight(t *testing.T) {
	r := newRig(t)
	a := r.add("s", "first")
	r.add("s", "second")
	r.acp.onStart = func() {
		// While the send is under way the owner pauses and deletes it.
		r.acp.onStart = nil
		_ = r.store.Pause("s", "owner")
		if err := r.store.Delete("s", a.ID); err != nil {
			t.Fatal(err)
		}
	}
	r.step()
	r.wantQueue("s", QueuePaused, "owner") // not paused again, not retried
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.step()
	r.step()
	if len(r.acp.starts) != 1 {
		t.Fatalf("second item joined the deleted item's turn: %q", r.acp.starts)
	}
	r.acp.end("turn1", false)
	r.step()
	r.step()
	if strings.Join(r.acp.starts, "|") != "first|second" {
		t.Fatalf("starts = %q", r.acp.starts)
	}
}

// --- iTerm2 (sessions open in a terminal) ---

func TestTerminalSendsOnlyWhenFree(t *testing.T) {
	prompt := &terminput.Prompt{ID: "p"}
	for name, tc := range map[string]struct {
		status   terminput.Status
		registry string
		note     string
	}{
		"working":       {terminput.Status{CanType: true, Working: true}, "idle", NoteWorking},
		"registry":      {free, "busy", NoteWorking},
		"prompt":        {terminput.Status{CanType: true, Prompt: prompt}, "idle", ""},
		"box not empty": {terminput.Status{Reason: "not_at_input"}, "idle", ""},
		"mac in use":    {terminput.Status{Reason: "mac_in_use"}, "idle", NoteMacInUse},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.openTerm("s")
			r.term.open["s"] = tc.registry
			r.term.status = tc.status
			r.add("s", "first")
			r.step()
			r.wantSends()
			r.wantQueue("s", QueueRunning, "")
			r.wantFirst("s", "first", StateWaiting)
			if got := r.d.Note("s"); got != tc.note {
				t.Fatalf("Note = %q, want %q", got, tc.note)
			}
			r.term.open["s"], r.term.status = "idle", free
			r.settle()
			r.wantSends("first")
			if r.d.Note("s") != "" {
				t.Fatalf("Note = %q after sending", r.d.Note("s"))
			}
		})
	}
}

func TestTerminalAnswerFinishesOnItsTurnThenIdle(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.add("s", "second")
	r.settle()
	r.wantSends("first")
	r.wantFirst("s", "first", StateAnswering)
	if !r.store.Terminal("s") {
		t.Fatal("the queue is not bound to the terminal")
	}

	r.now = r.now.Add(2 * time.Second)
	r.step() // idle screen, but no history turn yet: Claude may not have started
	r.wantFirst("s", "first", StateAnswering)

	r.turn("The user sent a new message while you were working:\nfirst")
	r.term.status = terminput.Status{CanType: true, Working: true}
	for range 3 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	r.wantFirst("s", "first", StateAnswering)

	r.term.status = free
	r.now = r.now.Add(2 * time.Second)
	r.step() // idle once: not yet
	r.wantFirst("s", "first", StateAnswering)
	r.now = r.now.Add(2 * time.Second)
	r.step()
	r.wantFirst("s", "second", StateWaiting)
	r.wantSends("first")
	r.settle()
	r.wantSends("first", "second")
	r.wantQueue("s", QueueRunning, "")
}

func TestTerminalNoAnswerInTwoMinutesPauses(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.add("s", "second")
	r.settle()
	r.now = r.now.Add(2*time.Minute - time.Second)
	r.step()
	r.wantQueue("s", QueueRunning, "")
	r.now = r.now.Add(time.Second)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonNoAnswer)
	r.wantFirst("s", "first", StateAnswering)

	// The owner checks the session and resumes: the item counts as answered
	// once the session is idle, and is never typed again.
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	r.wantSends("first", "second")
}

func TestTerminalOwnersOwnTurnPauses(t *testing.T) {
	t.Run("between items", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.add("s", "second")
		r.settle()
		r.now = r.now.Add(2 * time.Second)
		r.turn("first")
		r.step()
		r.now = r.now.Add(2 * time.Second)
		r.step()
		r.wantFirst("s", "second", StateWaiting)
		r.now = r.now.Add(time.Second)
		r.turn("something the owner typed at the Mac")
		r.step()
		r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
		r.wantSends("first")
		// Resume: the owner has seen it; the queue goes on.
		if err := r.store.Resume("s"); err != nil {
			t.Fatal(err)
		}
		r.settle()
		r.wantSends("first", "second")
	})
	t.Run("after queueing, before the first look", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.now = r.now.Add(time.Minute)
		r.turn("owner at the Mac")
		r.step()
		r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
		r.wantSends()
	})
	t.Run("an older turn is not the owner's new one", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.settle()
		r.wantSends("first")
	})
}

func TestTerminalSendErrorClasses(t *testing.T) {
	for code, reason := range map[string]string{
		"busy":               "",
		"not_at_input":       "",
		"mac_in_use":         "",
		"not_claude":         ReasonNotClaude,
		"automation_denied":  ReasonAutomationDenied,
		"no_terminal_driver": ReasonNoDriver,
		"disabled":           ReasonDisabled,
		"not_submitted":      ReasonInBox,
		"not_in_terminal":    ReasonClosed,
		"error":              ReasonSendFailed,
	} {
		t.Run(code, func(t *testing.T) {
			r := newRig(t)
			r.openTerm("s")
			r.add("s", "first")
			r.term.sendErr = errByCode(code)
			r.settle()
			r.wantSends("first")
			if reason == "" {
				r.wantQueue("s", QueueRunning, "")
				r.wantFirst("s", "first", StateWaiting)
				return
			}
			r.wantQueue("s", QueuePaused, reason)
			// Never retried by itself.
			for range 3 {
				r.now = r.now.Add(3 * time.Minute)
				r.step()
			}
			r.wantSends("first")
		})
	}
}

func errByCode(code string) error {
	for _, e := range []error{terminput.ErrBusy, terminput.ErrNotAtInput, terminput.ErrMacInUse, terminput.ErrNotClaude,
		terminput.ErrAutomationDenied, terminput.ErrNoDriver, terminput.ErrDisabled, terminput.ErrNotSubmitted, terminput.ErrNotInTerminal} {
		if terminput.Code(e) == code {
			return e
		}
	}
	return errors.New("iterm2: type: timed out")
}

func TestTerminalStatusErrorsPause(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "automation_denied"}
	r.step()
	r.wantSends()
	r.wantQueue("s", QueuePaused, ReasonAutomationDenied)
}

func TestTerminalNotSubmittedThenResume(t *testing.T) {
	setup := func(t *testing.T) *rig {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.add("s", "second")
		r.term.sendErr = terminput.ErrNotSubmitted
		r.settle()
		r.term.sendErr = nil
		r.wantQueue("s", QueuePaused, ReasonInBox)
		r.step()
		if len(r.term.submits) != 0 {
			t.Fatalf("pressed Enter while paused: %q", r.term.submits)
		}
		if err := r.store.Resume("s"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	t.Run("box holds the text", func(t *testing.T) {
		r := setup(t)
		r.step()
		if strings.Join(r.term.submits, "|") != "first" {
			t.Fatalf("submits = %q", r.term.submits)
		}
		r.wantFirst("s", "first", StateAnswering)
		r.wantSends("first") // typed once only
	})
	t.Run("box holds other text", func(t *testing.T) {
		r := setup(t)
		r.term.submitErr = terminput.ErrNotInBox
		r.step()
		r.wantQueue("s", QueuePaused, ReasonClearBox)
		r.wantSends("first")
	})
	t.Run("box cleared", func(t *testing.T) {
		r := setup(t)
		r.term.submitErr = terminput.ErrBoxEmpty
		r.step()
		r.wantFirst("s", "first", StateWaiting)
		r.settle()
		r.wantSends("first", "first") // the owner cleared it and resumed
	})
	t.Run("the owner pressed Enter at the Mac", func(t *testing.T) {
		r := setup(t)
		r.now = r.now.Add(time.Second)
		r.turn("first")
		r.step()
		if len(r.term.submits) != 0 {
			t.Fatalf("submits = %q", r.term.submits)
		}
		r.wantFirst("s", "first", StateAnswering)
		r.wantQueue("s", QueueRunning, "")
	})
}

func TestTerminalClaudeExited(t *testing.T) {
	t.Run("while answering", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.add("s", "second")
		r.settle()
		delete(r.term.open, "s")
		r.step()
		r.wantQueue("s", QueuePaused, ReasonClosed)
		r.wantFirst("s", "second", StateWaiting)
		r.step()
		if len(r.acp.starts) != 0 {
			t.Fatalf("queue moved to ACP by itself: %q", r.acp.starts)
		}
	})
	t.Run("while waiting, after a restart", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.term.status = terminput.Status{Reason: "mac_in_use"}
		r.step()
		r.store.Close()
		r.store = openAt(t, r.path, false)
		r.d = r.dispatcher(true)
		delete(r.term.open, "s")
		r.step()
		r.wantQueue("s", QueuePaused, ReasonClosed)
		if len(r.acp.starts) != 0 || len(r.term.sends) != 0 {
			t.Fatalf("sent: acp %q terminal %q", r.acp.starts, r.term.sends)
		}
		// Never to ACP, not even on Resume: still gone, paused again.
		if err := r.store.Resume("s"); err != nil {
			t.Fatal(err)
		}
		r.step()
		r.wantQueue("s", QueuePaused, ReasonClosed)
		if len(r.acp.starts) != 0 {
			t.Fatalf("starts = %q", r.acp.starts)
		}
		// Reopened in iTerm2: Resume sends it there.
		r.openTerm("s")
		r.term.status = free
		if err := r.store.Resume("s"); err != nil {
			t.Fatal(err)
		}
		r.settle()
		r.wantSends("first")
	})
}

func TestTerminalOwnerDeletesTheItemInFlight(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	a := r.add("s", "first")
	r.add("s", "second")
	r.term.onSend = func() {
		r.term.onSend = nil
		_ = r.store.Pause("s", "owner")
		if err := r.store.Delete("s", a.ID); err != nil {
			t.Fatal(err)
		}
	}
	r.settle()
	r.wantQueue("s", QueuePaused, "owner")
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(2 * time.Second)
	r.step()
	r.now = r.now.Add(2 * time.Second)
	r.step()
	r.wantSends("first") // its turn has not been seen: the next would join it
	r.turn("first")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	r.term.status = free
	for range 5 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	r.wantSends("first", "second")
	r.wantQueue("s", QueueRunning, "") // never paused for "You used this session"
}

func TestRestartMidSendNeverResends(t *testing.T) {
	for _, state := range []string{StateSending, StateAnswering} {
		r := newRig(t)
		r.openTerm("s")
		raw := fmt.Sprintf(`{"sessions":{"s":{"state":"running","items":[{"id":"a","text":"first","created":%q,"state":%q}],"terminal":true},
			"p":{"state":"running","items":[{"id":"b","text":"other","created":%q,"state":%q}]}}}`,
			t0.Format(time.RFC3339), state, t0.Format(time.RFC3339), state)
		if err := os.WriteFile(r.path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		r.store.Close()
		r.store = openAt(t, r.path, false)
		r.d = r.dispatcher(true)
		for range 3 {
			r.now = r.now.Add(3 * time.Minute)
			r.step()
		}
		if len(r.term.sends) != 0 || len(r.acp.starts) != 0 {
			t.Fatalf("%s: re-sent after restart: terminal %q acp %q", state, r.term.sends, r.acp.starts)
		}
		r.wantQueue("s", QueuePaused, ReasonMaybeSent)
		r.wantQueue("p", QueuePaused, ReasonMaybeSent)
	}
}

func TestTerminalInputOffPausesTerminalQueues(t *testing.T) {
	r := newRig(t)
	r.d = r.dispatcher(false)
	r.openTerm("s")
	r.openTerm("empty") // no items: never looked at
	r.add("s", "first")
	for range 3 {
		r.now = r.now.Add(time.Minute)
		r.step()
	}
	r.wantSends()
	r.wantQueue("s", QueuePaused, ReasonDisabled)
	r.wantFirst("s", "first", StateWaiting)
	r.wantQueue("empty", QueueRunning, "")
	if r.term.calls != 0 || len(r.acp.starts) != 0 || r.store.Terminal("s") {
		t.Fatalf("touched a terminal session: %d calls, acp %q", r.term.calls, r.acp.starts)
	}
	if got := r.store.Sessions(); len(got) != 1 || got[0] != "s" {
		t.Fatalf("Sessions = %v", got)
	}
}

func TestPausedQueuesAreNotPolled(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	_ = r.store.Pause("s", "owner")
	r.step()
	if r.term.calls != 0 {
		t.Fatalf("polled a paused queue: %d calls", r.term.calls)
	}
}

func TestAuditCarriesNoText(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "secret plans")
	r.add("s", "more secret plans")
	r.settle()
	r.now = r.now.Add(3 * time.Minute)
	r.step() // no answer: paused
	var actions []string
	for _, e := range r.audit.entries {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "answer seen") {
			t.Fatalf("audit line holds text or a reason line: %s", raw)
		}
		actions = append(actions, e.Action+":"+e.Result+":"+e.Session)
	}
	want := "queue_send:ok:s|queue_paused:no_answer:s"
	if got := strings.Join(actions, "|"); got != want {
		t.Fatalf("audit = %s, want %s", got, want)
	}
}

func TestRunStepsOnKickAndStops(t *testing.T) {
	r := newRig(t)
	ticks := make(chan time.Time)
	d := New(Config{Store: r.store, ACP: r.acp, Audit: r.audit, Now: func() time.Time { return r.now },
		After: func(time.Duration) <-chan time.Time { return ticks }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	r.add("s", "first")
	d.Kick()
	deadline := time.Now().Add(5 * time.Second)
	for r.store.Get("s").Items[0].State != StateAnswering {
		if time.Now().After(deadline) {
			t.Fatal("Kick did not send")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestTerminalDeletedAfterAPauseStillWaitsForItsTurn(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	a := r.add("s", "first")
	r.add("s", "second")
	r.settle()
	r.now = r.now.Add(time.Second)
	r.turn("the owner, at the Mac, before the item's turn showed")
	r.step()
	r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
	if err := r.store.Delete("s", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	r.wantSends("first") // the deleted item's turn has not shown: wait
	r.turn("first")
	for range 5 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	r.wantSends("first", "second")
}

func TestResumeAcknowledgesTurnsMadeWhilePaused(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "mac_in_use"}
	r.step() // watched, waiting
	_ = r.store.Pause("s", ReasonNotAllowed)
	r.now = r.now.Add(time.Minute)
	r.turn("the owner worked at the Mac while it was paused")
	r.step() // paused with nothing in flight: not read
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.term.status = free
	r.settle()
	r.wantQueue("s", QueueRunning, "")
	r.wantSends("first")
}

func TestTerminalBoxNotEmptyPausesAfterTwoMinutes(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "not_at_input"}
	r.step()
	r.now = r.now.Add(2 * time.Minute)
	r.step()
	r.wantQueue("s", QueueRunning, "") // two minutes exactly: still waiting
	r.now = r.now.Add(time.Second)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonBoxNotEmpty)
	r.wantFirst("s", "first", StateWaiting)
	r.wantSends()
}

func TestTerminalBoxNotEmptyCountsOnlyAnUnbrokenHold(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "not_at_input"}
	r.step()
	r.now = r.now.Add(time.Minute)
	r.term.status = terminput.Status{CanType: true, Working: true} // the owner's turn ran
	r.step()
	r.term.status = terminput.Status{Reason: "not_at_input"}
	r.now = r.now.Add(90 * time.Second)
	r.step()
	r.wantQueue("s", QueueRunning, "")
	// A Send refused with not_at_input holds it too.
	r.term.status, r.term.sendErr = free, terminput.ErrNotAtInput
	r.now = r.now.Add(31 * time.Second)
	r.step()
	r.wantQueue("s", QueueRunning, "")
	r.now = r.now.Add(90 * time.Second)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonBoxNotEmpty)
}

func TestOtherWaitsNeverPause(t *testing.T) {
	for _, st := range []terminput.Status{{Reason: "mac_in_use"}, {CanType: true, Working: true}, {Reason: "busy"}} {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.term.status = st
		for range 5 {
			r.step()
			r.now = r.now.Add(time.Hour)
		}
		r.wantQueue("s", QueueRunning, "")
		if st.Reason == "mac_in_use" && r.d.Note("s") != NoteMacInUse {
			t.Fatalf("Note = %q", r.d.Note("s"))
		}
	}
}

func TestTerminalBoxHoldStartsOverAfterAPause(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "not_at_input"}
	r.step()
	_ = r.store.Pause("s", ReasonNotAllowed)
	r.now = r.now.Add(time.Hour)
	r.step()
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.step()
	r.wantQueue("s", QueueRunning, "") // a fresh two minutes from the Resume
}

func TestResumeNeverSubmitsADeletedItem(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	a := r.add("s", "first")
	r.term.sendErr = terminput.ErrNotSubmitted
	r.settle()
	r.term.sendErr = nil
	r.wantQueue("s", QueuePaused, ReasonInBox)
	if err := r.store.Delete("s", a.ID); err != nil {
		t.Fatal(err)
	}
	r.add("s", "second")
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.term.status = terminput.Status{Reason: "not_at_input"} // the old text is still in the box
	for range 5 {
		r.step()
		r.now = r.now.Add(time.Minute)
	}
	if len(r.term.submits) != 0 {
		t.Fatalf("pressed Enter on the deleted item's text: %q", r.term.submits)
	}
	r.wantSends("first")
	r.wantQueue("s", QueuePaused, ReasonBoxNotEmpty)
}

func TestNoItemsNoHistoryReads(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	a := r.add("s", "first")
	r.step() // sent, answering
	_ = r.store.Pause("s", "owner")
	if err := r.store.Delete("s", a.ID); err != nil {
		t.Fatal(err)
	}
	calls := r.term.calls
	r.step()
	r.step()
	if r.term.calls != calls {
		t.Fatalf("polled a session with no items: %d more calls", r.term.calls-calls)
	}
}

func TestPromptWaitsSilentlyWithoutTheBoxPause(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "not_at_input", Prompt: &terminput.Prompt{ID: "p"}}
	for range 4 {
		r.step()
		r.now = r.now.Add(time.Minute)
	}
	r.wantQueue("s", QueueRunning, "")
	r.wantSends()
}

func TestAnsweredItemNeverStallsSilently(t *testing.T) {
	answered := func(t *testing.T) *rig {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.add("s", "second")
		r.settle()
		r.now = r.now.Add(time.Second)
		r.turn("first")
		return r
	}
	t.Run("a pause-class status pauses", func(t *testing.T) {
		r := answered(t)
		r.term.status = terminput.Status{Reason: "not_claude"}
		r.step()
		r.wantQueue("s", QueuePaused, ReasonNotClaude)
	})
	t.Run("a full box finishes the answer, then holds the next item two minutes", func(t *testing.T) {
		r := answered(t)
		r.term.status = terminput.Status{Reason: "not_at_input"}
		r.step()
		r.now = r.now.Add(idleFor)
		r.step()
		r.wantFirst("s", "second", StateWaiting)
		r.step()
		r.now = r.now.Add(2 * time.Minute)
		r.step()
		r.wantQueue("s", QueueRunning, "")
		r.now = r.now.Add(time.Second)
		r.step()
		r.wantQueue("s", QueuePaused, ReasonBoxNotEmpty)
		r.wantSends("first")
	})
	t.Run("a prompt waits", func(t *testing.T) {
		r := answered(t)
		r.term.status = terminput.Status{Reason: "not_at_input", Prompt: &terminput.Prompt{ID: "p"}}
		for range 4 {
			r.step()
			r.now = r.now.Add(time.Minute)
		}
		r.wantQueue("s", QueueRunning, "")
	})
}

func TestInBoxSubmitOutcomesAreVisible(t *testing.T) {
	inBox := func(t *testing.T, submitErr error) *rig {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.term.sendErr = terminput.ErrNotSubmitted
		r.settle()
		r.term.sendErr, r.term.submitErr = nil, submitErr
		if err := r.store.Resume("s"); err != nil {
			t.Fatal(err)
		}
		r.step()
		return r
	}
	r := inBox(t, terminput.ErrNotClaude)
	r.wantQueue("s", QueuePaused, ReasonNotClaude)
	r = inBox(t, terminput.ErrMacInUse)
	r.wantQueue("s", QueueRunning, "")
	if r.d.Note("s") != NoteMacInUse {
		t.Fatalf("Note = %q", r.d.Note("s"))
	}
}

func TestBindingClearsWithTheLastItem(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first") // keyed: its key is retired when it goes
	r.settle()
	r.now = r.now.Add(time.Second)
	r.turn("first")
	for range 3 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	if len(r.store.Get("s").Items) != 0 {
		t.Fatal("item not done")
	}
	if r.store.Terminal("s") {
		t.Fatal("an empty queue stays bound to the terminal")
	}
}

func TestResumeRightAfterADispatcherPauseIsSeen(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.now = r.now.Add(time.Second)
	r.turn("owner at the Mac")
	r.step()
	r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
	// Before the next step: the owner types once more, then resumes.
	r.now = r.now.Add(time.Second)
	r.turn("owner again")
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	r.wantQueue("s", QueueRunning, "")
	r.wantSends("first")
}

func TestTwoTurnsWithinOneSecondBothCount(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.step()
	r.term.turns = append(r.term.turns,
		Turn{Time: r.now.Add(200 * time.Millisecond), User: "first"},
		Turn{Time: r.now.Add(700 * time.Millisecond), User: "owner at the Mac"})
	r.step()
	r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
}

func TestACPOpenInTerminalWaitsTwoMinutesThenPauses(t *testing.T) {
	r := newRig(t)
	r.add("s", "first")
	r.acp.startErr = ErrInTerminal
	r.step()
	r.now = r.now.Add(2 * time.Minute)
	r.step()
	r.wantQueue("s", QueueRunning, "")
	r.now = r.now.Add(time.Second)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonInTerminal)
	r.wantFirst("s", "first", StateWaiting)
}

func TestLastItemsTurnErrorLeavesNoEmptyPausedQueue(t *testing.T) {
	r := newRig(t)
	r.add("s", "first")
	r.step()
	r.acp.end("turn1", true)
	r.step()
	if got := r.store.Sessions(); len(got) != 0 {
		t.Fatalf("Sessions = %v", got)
	}
	r.wantQueue("s", QueueRunning, "")
	r.add("s", "next")
	r.step()
	if len(r.acp.starts) != 2 {
		t.Fatalf("starts = %q: a later item waits behind an invisible pause", r.acp.starts)
	}
}

// failSaves makes the queue file's folder read-only, so every change the
// store tries to save fails, or writable again.
func (r *rig) failSaves(fail bool) {
	r.t.Helper()
	mode := os.FileMode(0o700)
	if fail {
		mode = 0o500
	}
	if err := os.Chmod(filepath.Dir(r.path), mode); err != nil { //nolint:gosec // a test folder: owner rwx or r-x
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = os.Chmod(filepath.Dir(r.path), 0o700) }) //nolint:gosec // a test folder: owner rwx
}

func TestFirstSendWaitsForASteadyIdle(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	a := r.add("s", "first")
	r.settle()
	r.wantSends("first")
	// The owner deletes the last item mid-answer, the dispatcher forgets the
	// session, and a new item comes at once; the session reads idle for a
	// moment before its turn shows work.
	_ = r.store.Pause("s", "owner")
	if err := r.store.Delete("s", a.ID); err != nil {
		t.Fatal(err)
	}
	r.step()
	r.add("s", "second")
	if err := r.store.Resume("s"); err != nil {
		t.Fatal(err)
	}
	r.step()
	r.wantSends("first") // idle once: not enough
	r.now = r.now.Add(time.Second)
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	r.term.status = free
	r.now = r.now.Add(time.Second)
	r.step()
	r.now = r.now.Add(idleFor - time.Millisecond)
	r.step()
	r.wantSends("first") // idle, but not yet for idleFor without a break
	r.now = r.now.Add(time.Millisecond)
	r.step()
	r.wantSends("first", "second")
}

func TestUnsavedPauseKeepsItsCause(t *testing.T) {
	t.Run("a foreign turn", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.now = r.now.Add(time.Second)
		r.turn("owner at the Mac")
		if err := r.store.BindTerminal("s"); err != nil {
			t.Fatal(err)
		}
		r.failSaves(true)
		r.step()
		r.failSaves(false)
		r.wantQueue("s", QueueRunning, "") // the pause was not saved
		r.step()
		r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
		r.wantSends()
	})
	t.Run("a full box", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		if err := r.store.BindTerminal("s"); err != nil {
			t.Fatal(err)
		}
		r.term.status = terminput.Status{Reason: "not_at_input"}
		r.step()
		r.now = r.now.Add(3 * time.Minute)
		r.failSaves(true)
		r.step()
		r.failSaves(false)
		r.wantQueue("s", QueueRunning, "")
		r.step() // still held for over two minutes: pauses now, not two minutes later
		r.wantQueue("s", QueuePaused, ReasonBoxNotEmpty)
	})
}

func TestEmptiedQueueStartsOverRunning(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.settle()
	r.now = r.now.Add(3 * time.Minute)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonNoAnswer)
	r.now = r.now.Add(time.Second)
	r.turn("first") // its answer shows after all, while paused
	for range 3 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	if len(r.store.Get("s").Items) != 0 {
		t.Fatal("item not done")
	}
	r.wantQueue("s", QueueRunning, "")
	r.add("s", "next")
	r.wantQueue("s", QueueRunning, "")
}

// NoteChanged hears each change of a session's waiting line, once: clients
// streaming the queue learn of it without reading the queue again.
func TestNoteChangedOnlyOnChange(t *testing.T) {
	r := newRig(t)
	var heard []string
	r.d = New(Config{
		Store: r.store, ACP: r.acp, Terminal: r.term, TerminalEnabled: true, Audit: r.audit,
		Now:         func() time.Time { return r.now },
		NoteChanged: func(session string) { heard = append(heard, session+"="+r.d.Note(session)) },
	})
	r.openTerm("s")
	r.term.status = terminput.Status{Reason: "mac_in_use"}
	r.add("s", "first")
	r.step()
	r.step() // the same line again: nothing new to hear
	if want := "s=" + NoteMacInUse; strings.Join(heard, "|") != want {
		t.Fatalf("heard %q, want %q", heard, want)
	}
	r.term.status = free
	r.settle()
	r.wantSends("first")
	if want := "s=" + NoteMacInUse + "|s="; strings.Join(heard, "|") != want {
		t.Fatalf("heard %q, want the line set then cleared: %q", heard, want)
	}
}

// A send refused with busy shows the working line, so a stuck working
// detection is visible rather than silent.
func TestTerminalBusySendShowsTheWorkingLine(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.sendErr = terminput.ErrBusy
	r.settle()
	r.wantQueue("s", QueueRunning, "")
	if got := r.d.Note("s"); got != NoteWorking {
		t.Fatalf("Note = %q, want %q", got, NoteWorking)
	}
}

// While its own item is answering, the working line is not shown: the
// item's state already says so.
func TestTerminalAnsweringShowsNoWorkingLine(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.settle()
	r.wantSends("first")
	r.turn("first")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	if got := r.d.Note("s"); got != "" {
		t.Fatalf("Note = %q while its own item answers", got)
	}
}

// A screen that cannot be read (Status error) holds the item, then pauses
// after 2 minutes with the read-failure reason, not "couldn't be sent".
func TestTerminalReadFailureHoldsThenPauses(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.term.status = terminput.Status{Reason: "error"}
	r.step()
	r.now = r.now.Add(2 * time.Minute)
	r.step()
	r.wantQueue("s", QueueRunning, "")
	r.now = r.now.Add(time.Second)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonReadFailed)
	r.wantFirst("s", "first", StateWaiting)
	r.wantSends()
}

// The same hold applies while waiting for an item's answer to finish.
func TestTerminalReadFailureWhileAnsweringHolds(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.add("s", "second")
	r.settle()
	r.turn("first")
	r.term.status = terminput.Status{Reason: "error"}
	r.step()
	r.wantQueue("s", QueueRunning, "")
	r.now = r.now.Add(2*time.Minute + time.Second)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonReadFailed)
	r.wantSends("first")
}

// A step cut short (the server stopping) never pauses or holds on what a
// cancelled read answered.
func TestCancelledStepNeverPauses(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.term.status = terminput.Status{Reason: "automation_denied"}
	r.term.onStatus = cancel
	r.d.step(ctx)
	r.wantQueue("s", QueueRunning, "")
	if w := r.d.watches["s"]; w != nil && w.heldItem != "" {
		t.Fatalf("hold started on a cancelled step: %q", w.heldItem)
	}
}

// The owner's own turn after the item's answer proves that answer over: the
// item finishes although the screen never reads idle (the owner is at the
// Mac, then Claude works on the owner's turn), and the queue pauses only
// for the owner's turn.
func TestTerminalOwnersTurnAfterTheAnswerFinishesTheItem(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "a short question")
	r.settle()
	r.wantSends("a short question")
	r.now = r.now.Add(time.Second)
	r.turn("a short question")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	r.wantFirst("s", "a short question", StateAnswering)

	r.now = r.now.Add(time.Minute)
	r.term.status = terminput.Status{Reason: "mac_in_use"}
	r.step()
	r.turn("the owner's own next question")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	if q := r.store.Get("s"); len(q.Items) != 0 {
		t.Fatalf("items = %+v, want the answered item done", q.Items)
	}
	r.wantSends("a short question")
	if got := r.audit.entries[len(r.audit.entries)-1]; got.Action != devicelogin.ActionQueuePaused || got.Result != "used_on_mac" {
		t.Fatalf("last audit = %+v, want queue_paused used_on_mac", got)
	}
}

// A message typed while Claude works joins the item's turn: not a new turn,
// so the item's answer is not over yet.
func TestTerminalMessageTypedWhileWorkingKeepsTheItemAnswering(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.settle()
	r.now = r.now.Add(time.Second)
	r.turn("first")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	r.now = r.now.Add(time.Second)
	r.turn(workingPrefix + "\nand one more thing")
	r.step()
	r.wantFirst("s", "first", StateAnswering)
}

// The owner typing into the box after the item's answer: Claude is not
// working, so the answer is over; the box is the owner's, not a reason to
// pause.
func TestTerminalOwnerTypingAfterTheAnswerFinishesTheItem(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.settle()
	r.now = r.now.Add(time.Second)
	r.turn("first")
	r.term.status = terminput.Status{Reason: "not_at_input"}
	r.step()
	r.wantFirst("s", "first", StateAnswering)
	r.now = r.now.Add(idleFor)
	r.step()
	if q := r.store.Get("s"); len(q.Items) != 0 {
		t.Fatalf("items = %+v, want the answered item done", q.Items)
	}
	for _, e := range r.audit.entries {
		if e.Action == devicelogin.ActionQueuePaused {
			t.Fatalf("audit = %+v, want no pause", r.audit.entries)
		}
	}
}

func TestUnsavedDoneIsRetriedNotPaused(t *testing.T) {
	t.Run("ACP", func(t *testing.T) {
		r := newRig(t)
		r.add("s", "first")
		r.add("s", "second")
		r.step()
		r.acp.end("turn1", false)
		r.failSaves(true)
		r.step() // its turn ended, but removing the item was not saved
		r.failSaves(false)
		r.wantFirst("s", "first", StateAnswering)
		r.step()
		r.wantFirst("s", "second", StateWaiting)
		r.wantQueue("s", QueueRunning, "")
		r.step()
		if strings.Join(r.acp.starts, "|") != "first|second" {
			t.Fatalf("starts = %q", r.acp.starts)
		}
	})
	t.Run("terminal", func(t *testing.T) {
		r := newRig(t)
		r.openTerm("s")
		r.add("s", "first")
		r.add("s", "second")
		r.settle()
		r.wantSends("first")
		r.turn("first")
		r.term.status = free
		r.now = r.now.Add(2 * time.Second)
		r.step() // idle once
		r.now = r.now.Add(2 * time.Second)
		r.failSaves(true)
		r.step() // answered, but removing the item was not saved
		r.failSaves(false)
		r.wantFirst("s", "first", StateAnswering)
		r.now = r.now.Add(2 * time.Second)
		r.step()
		r.wantQueue("s", QueueRunning, "")
		r.wantFirst("s", "second", StateWaiting)
		r.settle()
		r.wantSends("first", "second")
	})
}

func TestUnsavedDoneOfAFailedTurnStillPauses(t *testing.T) {
	r := newRig(t)
	r.add("s", "first")
	r.add("s", "second")
	r.step()
	r.acp.end("turn1", true)
	r.failSaves(true)
	r.step() // the turn failed, and removing its item was not saved
	r.failSaves(false)
	r.step()
	r.wantQueue("s", QueuePaused, ReasonTurnFailed)
	r.wantFirst("s", "second", StateWaiting)
	r.step()
	if len(r.acp.starts) != 1 {
		t.Fatalf("sent after a failed turn without a Resume: %q", r.acp.starts)
	}
}
