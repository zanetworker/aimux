package chatqueue

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
)

// Reasons a dispatcher pauses a queue with. Fixed lines: clients show them.
const (
	ReasonNoAnswer         = "No answer seen yet"
	ReasonUsedOnMac        = "You used this session on your Mac"
	ReasonClosed           = "This session closed on your Mac"
	ReasonInBox            = "Your message is in the input box on your Mac"
	ReasonClearBox         = "Clear the input box on your Mac, then resume"
	ReasonInTerminal       = "This session is open in a terminal on your Mac"
	ReasonBoxNotEmpty      = "The input box on your Mac isn't empty. Clear it, then resume."
	ReasonNotClaude        = "Claude Code isn't running in this session's terminal"
	ReasonAutomationDenied = "macOS isn't letting aimux type into iTerm2"
	ReasonNoDriver         = "Only sessions in iTerm2 can take queued messages"
	ReasonDisabled         = "Typing into terminal sessions is off on the Mac"
	ReasonNotRunning       = "Session not running"
	ReasonTurnFailed       = "Claude stopped with an error. Check the session, then resume."
	ReasonNoHistory        = "Couldn't read this session on your Mac"
	ReasonSendFailed       = "This message couldn't be sent. Check the session, then resume."
	ReasonReadFailed       = "Couldn't read the session on your Mac. Check iTerm2, then resume."
)

// NoteMacInUse is the waiting line (not a pause) while the guard holds the
// next item back.
const NoteMacInUse = "Waiting: you're using this session on your Mac"

// NoteWorking is the waiting line while Claude works in the session and
// the next item waits for it, so a working state that never ends shows.
const NoteWorking = "Waiting: Claude is working"

const (
	pollEvery = 2 * time.Second
	// answerWait is how long after Enter the item's history turn must show.
	answerWait = 2 * time.Minute
	// boxHeld is how long a non-empty input box may hold the next item back
	// before the queue pauses instead of waiting unseen.
	boxHeld = 2 * time.Minute
	// idleFor is how long a terminal session must stay idle, after the
	// item's turn showed, before its answer counts as finished: Claude Code
	// may show an idle screen for a moment before it starts working.
	idleFor = 2 * time.Second
)

// ErrNotFree and ErrNotRunning are what an ACP sender answers when the
// session cannot take a turn now (it waits) or at all (the queue pauses).
var (
	ErrNotFree    = errors.New("chatqueue: the session cannot take a turn yet")
	ErrNotRunning = errors.New("chatqueue: the session is not running")
	// ErrInTerminal: the session is open in a terminal although the
	// registry read said it was not; it waits a while, then pauses.
	ErrInTerminal = errors.New("chatqueue: the session is open in a terminal")
)

// Turn is a person's turn in a session's history: when it started and the
// user text Claude Code saved. Turns Claude Code writes itself (hook
// feedback, notifications) are left out by the reader.
type Turn struct {
	Time time.Time
	User string
}

// Terminal reaches sessions open in a terminal (terminput.Service plus
// Claude's registry and the session's history).
type Terminal interface {
	// Open reports whether a terminal process holds session, and its status
	// in Claude's registry ("busy", "idle" or "").
	Open(session string) (registryStatus string, ok bool)
	Status(ctx context.Context, session, registryStatus string) terminput.Status
	Send(ctx context.Context, session, text, key string) error
	// Submit presses Enter for text left in the input box (terminput.Service.Submit).
	Submit(ctx context.Context, session, text string) error
	// Turns returns the session's latest person turns.
	Turns(ctx context.Context, session string) ([]Turn, error)
}

// ACP runs turns for sessions not open in a terminal (the broker).
type ACP interface {
	Busy(session string) bool
	// Start begins a turn; the same key returns the same turn. Errors:
	// ErrNotFree, ErrInTerminal, ErrNotRunning, anything else.
	Start(session, text, key string) (turn string, err error)
	// TurnEnded reports whether turn is over, and whether it ended in error.
	TurnEnded(session, turn string) (ended, failed bool)
}

// Auditor takes audit lines (devicelogin.Audit).
type Auditor interface{ Write(devicelogin.AuditEntry) }

// Config is what a Dispatcher drives. Store is required; a nil ACP or
// Terminal leaves those sessions alone, as does TerminalEnabled false for
// sessions open in a terminal.
type Config struct {
	Store           *Store
	ACP             ACP
	Terminal        Terminal
	TerminalEnabled bool
	Audit           Auditor
	// Typed, if set, is told after the dispatcher typed into a terminal
	// session, so a cached status is dropped.
	Typed func(session string)
	// NoteChanged, if set, is told when a session's waiting line (Note)
	// changes. Called from Run's goroutine, outside the dispatcher's locks.
	NoteChanged func(session string)
	Now         func() time.Time                       // default time.Now
	After       func(d time.Duration) <-chan time.Time // default time.After
}

// Dispatcher sends each session's queued items into the session, one at a
// time: the next only once the answer to the one before has finished. One
// goroutine (Run) does all the work, so its per-session state needs no lock.
type Dispatcher struct {
	cfg  Config
	kick chan struct{}

	watches map[string]*watch // Run's goroutine only

	notesMu sync.Mutex
	notes   map[string]string
}

// watch is what the dispatcher remembers about one session with items.
type watch struct {
	running bool      // the queue's state at the last step, to see a Resume
	cursor  time.Time // history turns up to here are accounted for (terminal)
	flight  *flight   // the item sent whose answer is not finished
	// The first item held back by a non-empty input box, and since when.
	heldItem  string
	heldSince time.Time
	// idleSince: the session has read idle, unbroken, since then. A first
	// send needs idleFor of it, so it never joins a turn that just began.
	idleSince time.Time
}

// flight is an item the dispatcher typed or started.
type flight struct {
	item string // "" once the owner deleted it: still wait for its turn
	text string
	turn string // ACP: the turn it started; "" in a terminal
	// finished: its answer is over but removing the item was not saved yet.
	finished bool

	// Terminal only.
	inBox   bool      // typed (maybe) but Enter not pressed: wait for Resume
	entered time.Time // when Enter was pressed
	matched bool      // its history turn showed
	turnAt  time.Time // when, once matched
	// over: a later turn of the owner's started, so its answer ended.
	over      bool
	noAnswer  bool      // answerWait passed without its turn
	idleSince time.Time // idle since, after matched; zero while not idle
}

// New returns a Dispatcher; Run starts it.
func New(cfg Config) *Dispatcher {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	return &Dispatcher{cfg: cfg, kick: make(chan struct{}, 1), watches: map[string]*watch{}, notes: map[string]string{}}
}

// Run works until ctx ends: every 2 s, and at once after Kick.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		d.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-d.kick:
		case <-d.cfg.After(pollEvery):
		}
	}
}

// Kick makes Run look at the queues now: after a queue change or a turn's
// end. It never blocks.
func (d *Dispatcher) Kick() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Note is session's waiting line, "" when there is none.
func (d *Dispatcher) Note(session string) string {
	d.notesMu.Lock()
	defer d.notesMu.Unlock()
	return d.notes[session]
}

func (d *Dispatcher) setNote(session, note string) {
	d.notesMu.Lock()
	changed := d.notes[session] != note
	if note == "" {
		delete(d.notes, session)
	} else {
		d.notes[session] = note
	}
	d.notesMu.Unlock()
	if changed && d.cfg.NoteChanged != nil {
		d.cfg.NoteChanged(session)
	}
}

// step looks once at every session with items. A session left with none
// is forgotten, an in-flight item the owner deleted with it: there is no
// next item to keep out of its turn, and a later one waits for an idle
// session like any first item.
func (d *Dispatcher) step(ctx context.Context) {
	ids := d.cfg.Store.Sessions()
	for id := range d.watches {
		if !slices.Contains(ids, id) {
			delete(d.watches, id) // nothing left to send or watch
			d.setNote(id, "")
		}
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		d.stepSession(ctx, id)
	}
}

func (d *Dispatcher) stepSession(ctx context.Context, s string) {
	q := d.cfg.Store.Get(s)
	w, seen := d.watches[s]
	if !seen {
		w = &watch{}
		d.watches[s] = w
	}
	running := q.State == QueueRunning
	resumed := seen && running && !w.running
	w.running = running
	if !running || resumed {
		w.heldItem = "" // a hold counts from the latest Resume
	}

	f := w.flight
	if f != nil && f.item != "" && !slices.ContainsFunc(q.Items, func(it Item) bool { return it.ID == f.item }) {
		f.item = "" // the owner deleted it
	}
	if f == nil {
		if len(q.Items) > 0 && inFlight(q.Items[0]) {
			// In flight without this dispatcher having sent it: never guess.
			d.pause(s, pauseMaybeSent)
			return
		}
		if !running {
			return // nothing to send or watch until Resume
		}
		if _, ok := d.cfg.Store.Next(s); !ok {
			return
		}
	}

	registry, open := "", false
	if d.cfg.Terminal != nil {
		registry, open = d.cfg.Terminal.Open(s)
	}
	switch {
	case f != nil && f.turn != "":
		d.stepACP(s, q, w)
	case open || d.cfg.Store.Terminal(s) || f != nil:
		if !d.cfg.TerminalEnabled {
			if running {
				d.pause(s, pauseDisabled)
			}
			return
		}
		d.stepTerminal(ctx, s, q, w, registry, open, resumed)
	case d.cfg.ACP != nil:
		d.stepACP(s, q, w)
	}
}

// --- ACP ---

func (d *Dispatcher) stepACP(s string, q Queue, w *watch) {
	if f := w.flight; f != nil {
		if f.finished {
			d.finish(s, w)
			return
		}
		ended, failed := d.cfg.ACP.TurnEnded(s, f.turn)
		if !ended {
			return
		}
		if !d.finish(s, w) {
			return // retried next step; a failed turn pauses then
		}
		if failed {
			d.pause(s, pauseTurnFailed)
		}
		return
	}
	if q.State != QueueRunning || d.cfg.ACP.Busy(s) {
		return
	}
	it, ok := d.claim(s)
	if !ok {
		return
	}
	turn, err := d.cfg.ACP.Start(s, it.Text, sendKey(it))
	if err != nil {
		// Start fails before the turn begins: nothing was sent.
		_ = d.cfg.Store.Release(s, it.ID)
		if errors.Is(err, ErrNotFree) {
			w.heldItem = ""
			return
		}
		if errors.Is(err, ErrInTerminal) {
			d.hold(s, w, "acp:"+it.ID, d.cfg.Now(), pauseInTerminal)
			return
		}
		p := pauseSendFailed
		if errors.Is(err, ErrNotRunning) {
			p = pauseNotRunning
		}
		d.audit(devicelogin.ActionQueueSend, s, p.code)
		d.pause(s, p)
		return
	}
	d.audit(devicelogin.ActionQueueSend, s, "ok")
	w.flight = &flight{item: it.ID, text: it.Text, turn: turn}
	d.answering(s, w.flight)
}

// --- terminal ---

func (d *Dispatcher) stepTerminal(ctx context.Context, s string, q Queue, w *watch, registry string, open, resumed bool) {
	t := d.cfg.Terminal
	if f := w.flight; f != nil && f.finished {
		if !d.finish(s, w) {
			return
		}
	}
	if !open {
		if f := w.flight; f != nil {
			if f.inBox {
				_ = d.cfg.Store.Release(s, f.item) // claude exited: its box is gone
				w.flight = nil
			} else if !d.finish(s, w) { // sent; claude exited, so its answer is over
				return
			}
		}
		d.pause(s, pauseClosed)
		return
	}
	if err := d.cfg.Store.BindTerminal(s); err != nil {
		return // not saved: try again next step, before sending anything
	}

	if !d.readHistory(ctx, s, q, w, resumed) {
		return
	}
	now := d.cfg.Now()

	if f := w.flight; f != nil {
		if f.inBox {
			if f.item == "" {
				// The owner deleted it: its text is theirs to clear or send.
				// A leftover box now holds the next item back (not_at_input).
				w.flight = nil
				return
			}
			if q.State == QueueRunning {
				d.submit(ctx, s, w)
			}
			return
		}
		if f.over {
			d.finish(s, w)
			return
		}
		if !f.matched && !f.noAnswer && now.Sub(f.entered) >= answerWait {
			f.noAnswer = true
			if f.item != "" {
				d.pause(s, pauseNoAnswer)
				return
			}
		}
		// Its answer is finished once its turn showed and the session is
		// idle; after "No answer seen yet", once the owner resumed (or
		// deleted it) and the session is idle.
		acknowledged := f.noAnswer && (q.State == QueueRunning || f.item == "")
		if !f.matched && !acknowledged {
			return
		}
		st := t.Status(ctx, s, registry)
		if !settled(st, registry) {
			f.idleSince = time.Time{}
			st.Working = false // its own item's answer: the item says so already
			if d.notFree(ctx, s, w, st, "flight:"+f.item, now) && f.item == "" {
				w.flight = nil // the owner deleted it and the queue paused: nothing to wait for
			}
			return
		}
		w.heldItem = ""
		if f.idleSince.IsZero() {
			f.idleSince = now
		}
		if now.Sub(f.idleSince) >= idleFor {
			d.finish(s, w)
		}
		return
	}

	if q.State != QueueRunning {
		return
	}
	st := t.Status(ctx, s, registry)
	if !idle(st, registry) {
		w.idleSince = time.Time{}
		st.Working = st.Working || registry == "busy"
		if next, ok := d.cfg.Store.Next(s); ok {
			d.notFree(ctx, s, w, st, next.ID, now)
		}
		return
	}
	if w.idleSince.IsZero() {
		w.idleSince = now
	}
	if now.Sub(w.idleSince) < idleFor {
		return
	}
	w.idleSince = time.Time{}
	it, ok := d.claim(s)
	if !ok {
		return
	}
	err := t.Send(ctx, s, it.Text, sendKey(it))
	if d.cfg.Typed != nil {
		d.cfg.Typed(s)
	}
	code := terminput.Code(err)
	if err == nil {
		w.heldItem = ""
		d.setNote(s, "")
		d.audit(devicelogin.ActionQueueSend, s, "ok")
		w.flight = &flight{item: it.ID, text: it.Text, entered: now}
		d.answering(s, w.flight)
		return
	}
	p, pause := terminalPause(code)
	if !pause {
		// Refused before typing anything: still not free.
		_ = d.cfg.Store.Release(s, it.ID)
		// Status found no prompt just now; Send's not_at_input is a full box.
		d.notFree(ctx, s, w, terminput.Status{Reason: code}, it.ID, now)
		return
	}
	// Text may be in the box (or part of it): never typed again by itself.
	// Resume presses Enter only if the box holds exactly this text.
	d.setNote(s, "")
	d.audit(devicelogin.ActionQueueSend, s, code)
	w.flight = &flight{item: it.ID, text: it.Text, inBox: true}
	d.pause(s, p)
}

// notFree handles a session that cannot take text now: a pause-class
// reason pauses; mac_in_use and Working or busy show their waiting lines;
// a non-empty input box (not_at_input without a prompt) or a screen that
// cannot be read (Status error) holding the same thing back for more than
// boxHeld pauses, so nothing waits unseen. A prompt waits silently. key
// names what is held back. A step cut short (ctx done) does nothing: its
// reads answered errors that say nothing about the session. True: the
// queue was paused.
func (d *Dispatcher) notFree(ctx context.Context, s string, w *watch, st terminput.Status, key string, now time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	if st.Reason == codeError {
		d.setNote(s, "")
		return d.hold(s, w, key, now, pauseReadFailed)
	}
	if p, pause := terminalPause(st.Reason); pause {
		return d.pause(s, p)
	}
	note := ""
	switch {
	case st.Reason == terminput.Code(terminput.ErrMacInUse):
		note = NoteMacInUse
	case st.Working || st.Reason == terminput.Code(terminput.ErrBusy):
		note = NoteWorking
	}
	d.setNote(s, note)
	if st.Reason != terminput.Code(terminput.ErrNotAtInput) || st.Prompt != nil {
		w.heldItem = ""
		return false
	}
	return d.hold(s, w, key, now, pauseBoxNotEmpty)
}

// hold pauses with p once key has been held back, unbroken, for more than
// boxHeld. True: the queue was paused.
func (d *Dispatcher) hold(s string, w *watch, key string, now time.Time, p pauseReason) bool {
	if w.heldItem != key {
		w.heldItem, w.heldSince = key, now
		return false
	}
	if now.Sub(w.heldSince) <= boxHeld {
		return false
	}
	return d.pause(s, p) // clears the hold once saved; kept for the next step if not
}

// readHistory accounts for the session's new turns: the one of the item in
// flight, or one the queue did not send, which pauses the queue (unless
// the owner resumed just now, having seen it). False: stop this step.
func (d *Dispatcher) readHistory(ctx context.Context, s string, q Queue, w *watch, resumed bool) bool {
	turns, err := d.cfg.Terminal.Turns(ctx, s)
	if err != nil {
		d.pause(s, pauseNoHistory)
		return false
	}
	// Turns are told apart by time, not number: the history reader numbers
	// turns from the start of the part of the file it read, so numbers move.
	// The cursor lives in memory only, so after a restart a turn the owner
	// already resumed past can pause the queue once more (fail-safe).
	if w.cursor.IsZero() {
		// First look: a turn after the first item was queued is the owner's.
		w.cursor = d.cfg.Now()
		if len(q.Items) > 0 {
			w.cursor = q.Items[0].Created
		}
	}
	foreign := false
	cursor := w.cursor
	for _, t := range turns {
		// Strictly after: the cursor is the newest turn already handled.
		// Session files stamp turns to the millisecond, so two turns in one
		// second are both new; two with the same stamp are not expected.
		if !t.Time.After(cursor) {
			continue
		}
		f := w.flight
		if f != nil && !f.matched && SamePrompt(t.User, f.text) {
			f.matched, f.turnAt = true, t.Time
			if f.inBox {
				// The owner pressed Enter on it at the Mac.
				f.inBox, f.entered = false, d.cfg.Now()
				d.audit(devicelogin.ActionQueueSend, s, "ok")
				d.answering(s, f)
			}
			continue
		}
		if f != nil && f.matched {
			if t.Time.Equal(f.turnAt) {
				continue // its own turn, read again after an unsaved pause
			}
			if !midTurn(t.User) {
				f.over = true // Claude Code starts a turn only after the one before ended
			}
		}
		if !resumed {
			foreign = true
		}
	}
	if foreign && !d.pause(s, pauseUsedOnMac) {
		// Not saved: keep the cursor, so the next step sees the turn again.
		// (A matched flight stays matched; that turn is its own.)
		return false
	}
	for _, t := range turns {
		if t.Time.After(w.cursor) {
			w.cursor = t.Time
		}
	}
	return true
}

func (d *Dispatcher) submit(ctx context.Context, s string, w *watch) {
	f := w.flight
	err := d.cfg.Terminal.Submit(ctx, s, f.text)
	if d.cfg.Typed != nil {
		d.cfg.Typed(s)
	}
	code := terminput.Code(err)
	switch {
	case err == nil:
		d.setNote(s, "")
		d.audit(devicelogin.ActionQueueSend, s, "ok")
		f.inBox, f.entered = false, d.cfg.Now()
		d.answering(s, f)
	case errors.Is(err, terminput.ErrBoxEmpty):
		// The owner cleared the box as asked and resumed: send it afresh.
		w.flight = nil
		_ = d.cfg.Store.Release(s, f.item)
	case errors.Is(err, terminput.ErrNotInBox):
		d.pause(s, pauseClearBox)
	case code == codeError:
		d.pause(s, pauseSendFailed) // Enter may have failed: never guess
	default:
		// Submit answers not_at_input only for a prompt on screen: it
		// waits silently, as busy does; mac_in_use shows its line.
		st := terminput.Status{Reason: code}
		if errors.Is(err, terminput.ErrNotAtInput) {
			st.Prompt = &terminput.Prompt{}
		}
		d.notFree(ctx, s, w, st, "submit:"+f.item, d.cfg.Now())
	}
}

// settled: an answer is over: Claude is not working and shows no prompt.
// Text in the input box is the owner's own typing, not more of the answer.
func settled(st terminput.Status, registry string) bool {
	box := st.CanType || st.Reason == terminput.Code(terminput.ErrNotAtInput)
	return box && !st.Working && st.Prompt == nil && registry != "busy"
}

// midTurn: a message the owner typed while Claude worked, which Claude Code
// saves as a turn of its own but answers within the turn it was working on.
func midTurn(user string) bool { return strings.HasPrefix(strings.TrimSpace(user), workingPrefix) }

// idle: Claude waits at an empty input box, with no prompt, not working.
func idle(st terminput.Status, registry string) bool {
	return st.CanType && !st.Working && st.Prompt == nil && registry != "busy"
}

// --- shared steps ---

// claim marks the next item sending and returns it as it is now. A text the
// rules no longer allow has paused the queue.
func (d *Dispatcher) claim(s string) (Item, bool) {
	next, ok := d.cfg.Store.Next(s)
	if !ok {
		return Item{}, false
	}
	it, err := d.cfg.Store.MarkSending(s, next.ID)
	if errors.Is(err, ErrBadText) {
		d.audit(devicelogin.ActionQueuePaused, s, "not_allowed")
	}
	return it, err == nil
}

// answering records that f's item was sent. ErrNotFound: the owner deleted
// it meanwhile; f still waits for its turn so the next item never joins it.
func (d *Dispatcher) answering(s string, f *flight) {
	if err := d.cfg.Store.MarkAnswering(s, f.item); errors.Is(err, ErrNotFound) {
		f.item = ""
	}
}

// finish removes w's answered item and ends its flight. When the removal is
// not saved, the flight stays, marked finished, and the next step tries
// again: a cleared flight would leave the item answering, which reads as
// possibly sent. One the owner deleted, or no longer in flight, is left.
func (d *Dispatcher) finish(s string, w *watch) bool {
	f := w.flight
	if f.item != "" {
		if err := d.cfg.Store.Done(s, f.item); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrWrongState) {
			f.finished = true
			return false
		}
	}
	w.flight = nil
	return true
}

type pauseReason struct{ code, reason string }

var (
	pauseNoAnswer    = pauseReason{"no_answer", ReasonNoAnswer}
	pauseUsedOnMac   = pauseReason{"used_on_mac", ReasonUsedOnMac}
	pauseClosed      = pauseReason{"closed", ReasonClosed}
	pauseClearBox    = pauseReason{"clear_box", ReasonClearBox}
	pauseBoxNotEmpty = pauseReason{"box_not_empty", ReasonBoxNotEmpty}
	pauseInTerminal  = pauseReason{"in_terminal", ReasonInTerminal}
	pauseDisabled    = pauseReason{"disabled", ReasonDisabled}
	pauseNotRunning  = pauseReason{"not_running", ReasonNotRunning}
	pauseTurnFailed  = pauseReason{"turn_failed", ReasonTurnFailed}
	pauseNoHistory   = pauseReason{"no_history", ReasonNoHistory}
	pauseSendFailed  = pauseReason{"error", ReasonSendFailed}
	pauseReadFailed  = pauseReason{"read_failed", ReasonReadFailed}
	pauseMaybeSent   = pauseReason{"maybe_sent", ReasonMaybeSent}
)

// terminalPauses are the terminput codes that pause; the wait codes are
// absent. Any other code pauses with pauseSendFailed.
var terminalPauses = map[string]pauseReason{
	"not_claude":         {"not_claude", ReasonNotClaude},
	"automation_denied":  {"automation_denied", ReasonAutomationDenied},
	"no_terminal_driver": {"no_terminal_driver", ReasonNoDriver},
	"disabled":           pauseDisabled,
	"not_submitted":      {"not_submitted", ReasonInBox},
	"not_in_terminal":    pauseClosed,
}

// codeError is terminput's code for an error without a stable code: from
// Status, a screen or driver read that failed.
const codeError = "error"

// terminalWaits are not free yet: wait silently.
var terminalWaits = map[string]bool{"busy": true, "not_at_input": true, "mac_in_use": true}

func terminalPause(code string) (pauseReason, bool) {
	if terminalWaits[code] || code == "" {
		return pauseReason{}, false
	}
	if p, ok := terminalPauses[code]; ok {
		return p, true
	}
	return pauseSendFailed, true
}

// pause stops session's queue with p, auditing only a change. False: not
// saved (or no items to hold back); the caller keeps the cause for the
// next step.
func (d *Dispatcher) pause(s string, p pauseReason) bool {
	q := d.cfg.Store.Get(s)
	if len(q.Items) == 0 {
		return false // nothing to hold back; never store an empty paused queue
	}
	if q.State != QueuePaused || q.Reason != p.reason {
		if err := d.cfg.Store.Pause(s, p.reason); err != nil {
			return false
		}
		d.setNote(s, "")
		d.audit(devicelogin.ActionQueuePaused, s, p.code)
	}
	if w := d.watches[s]; w != nil {
		w.running = false // saved: a Resume before the next step is seen
		w.heldItem = ""
	}
	return true
}

func (d *Dispatcher) audit(action, s, result string) {
	if d.cfg.Audit != nil {
		d.cfg.Audit.Write(devicelogin.AuditEntry{Action: action, Session: s, Result: result})
	}
}

// sendKey is an item's idempotency key for the send path, so a repeated
// send of the same item is answered from the first.
func sendKey(it Item) string { return "queue-" + it.ID }
