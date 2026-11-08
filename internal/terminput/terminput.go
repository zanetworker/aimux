package terminput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/harness/claude"
)

const (
	idleRequired  = 120 * time.Second
	idemTTL       = 10 * time.Minute
	driverTimeout = 5 * time.Second
	// settle is the pause between typing text and pressing Enter: Claude
	// Code takes text and Enter as separate writes.
	settle = 300 * time.Millisecond
	// settleReads bounds the looks after typing: up to 1.5 s in all.
	settleReads = 5
)

// Keys other than digits that a driver can press.
const (
	KeyEscape    = harness.KeyEscape
	KeyEnter     = harness.KeyEnter
	KeySpace     = harness.KeySpace
	KeyBackspace = harness.KeyBackspace
	KeyClear     = harness.KeyClear
)

// clearRounds bounds the attempts to empty the box before Replace gives up.
const clearRounds = 3

// Target is the session's claude process and its tty, e.g. "/dev/ttys001".
type Target struct {
	PID int
	TTY string
}

// Pane is a driver's own handle, opaque to callers.
type Pane struct{ Driver, ID string }

// PaneRef is where a terminal shows one pane: its window, tab and position,
// as the terminal orders them.
type PaneRef struct {
	WindowID   int    // terminal's window id
	WindowName string // as the terminal shows it
	ActiveName string // name of the window's current session
	Tab        int    // 1-based position in the window
	TabName    string // name of the tab's current session
	Pane       int    // 1-based position in the tab
	Panes      int    // panes in the tab
	TTY        string
}

// Driver moves bytes for one terminal. Every safety check lives in Service.
type Driver interface {
	Name() string
	// Locate reports false when the pane is not this driver's.
	Locate(ctx context.Context, t Target) (Pane, bool, error)
	// Type types text without Enter; text for which PasteText is true goes
	// in as a bracketed paste. The service presses Enter itself, after
	// reading the screen again.
	Type(ctx context.Context, p Pane, text string) error
	Key(ctx context.Context, p Pane, k Key) error
	Screen(ctx context.Context, p Pane) (string, error)
}

// FocusReporter is a driver that can say whether its terminal is the
// frontmost app and which session has keyboard focus, by tty. Keystrokes
// reach only that session, so only it can mix with the owner's typing.
// It must never activate or focus anything.
type FocusReporter interface {
	Focused(ctx context.Context) (frontmost bool, tty string, err error)
}

// System answers questions about the machine.
type System interface {
	TTY(pid int) (string, error)
	ForegroundIsPID(tty string, pid int) (bool, error)
	// ForegroundCommand is the command name of the tty's foreground process
	// group leader, such as "claude" or "/usr/local/bin/node".
	ForegroundCommand(tty string) (string, error)
	IdleSeconds() (float64, error)
}

// Sessions maps a session id to the pid of the terminal process holding it,
// excluding sessions aimux itself owns.
type Sessions interface {
	TerminalPID(sessionID string) (int, bool)
}

// RegistryStatuses is Sessions that also reports a session's status in
// Claude's registry ("busy", "idle" or ""). Send then refuses a session the
// registry calls busy, as the chat queue's dispatcher does.
type RegistryStatuses interface {
	RegistryStatus(sessionID string) string
}

// Status is what a client needs to decide whether to offer a composer.
type Status struct {
	State   string // busy, idle or unknown
	CanType bool
	Reason  string // why CanType is false
	Prompt  *Prompt
	// Working is true while the screen shows Claude Code working on a turn;
	// Send then answers ErrBusy. CanType does not include it, so clients
	// without a queue keep their composer and get a fixed busy message.
	Working bool
	// BoxHasText: the input box holds text (a draft, or Claude Code's grey
	// suggestion; the screen does not say which). SendReplacing may clear it.
	BoxHasText bool
	// WorkingSeconds: how long the working line says the turn has run; 0
	// when it shows no time.
	WorkingSeconds int
}

type codeError struct{ code, msg string }

func (e *codeError) Error() string { return e.msg }

func newErr(code, msg string) error { return &codeError{code, msg} }

// Errors with stable codes.
var (
	ErrDisabled         = newErr("disabled", "terminal input is off")
	ErrNotInTerminal    = newErr("not_in_terminal", "session is not open in a terminal")
	ErrNotClaude        = newErr("not_claude", "the terminal is not running this session's claude")
	ErrMacInUse         = newErr("mac_in_use", "the owner is using this session on the Mac")
	ErrNoDriver         = newErr("no_terminal_driver", "no driver reaches this terminal")
	ErrAutomationDenied = newErr("automation_denied", "macOS denied automation access")
	ErrNotAtInput       = newErr("not_at_input", "the session is not waiting at its input box")
	ErrNoPrompt         = newErr("no_prompt", "no permission prompt is showing")
	ErrPromptChanged    = newErr("prompt_changed", "a different prompt is showing")
	ErrBadText          = newErr("bad_text", "the text holds control characters or starts with ! or /")
	ErrNotSubmitted     = newErr("not_submitted", "the text is in the input box but was not submitted")
	ErrNotCleared       = newErr("box_not_cleared", "the text in the input box could not be cleared; nothing was sent")
	ErrDraftInBox       = newErr("draft_in_box", "a draft left in the input box is kept (keep_drafts); nothing was sent")
	ErrBoxHasUnsent     = newErr("box_has_unsent", "Your earlier message is still in the box on your Mac; Retry it, or clear the box")
	// ErrUnsentUnconfirmed: the box shows a paste marker for the same text,
	// but it may be the owner's paste; a retry would only be refused again.
	ErrUnsentUnconfirmed = newErr("unsent_unconfirmed", "Your message may still be in the box on your Mac, but aimux can't confirm it's yours. Send or clear it at the Mac.")
	ErrBusy              = newErr("busy", "Claude is working on a turn in this session")
	// Submit's answers when the input box does not hold the message.
	ErrBoxEmpty = newErr("box_empty", "the input box is empty")
	ErrNotInBox = newErr("not_in_box", "the input box holds other text")
)

// CheckText returns ErrBadText when text holds a C0 control other than
// newline and tab, DEL or a C1 control, or when Claude Code would not take
// it as a message even with slash commands allowed (a leading "!", see
// claude.Composer.RefuseText). Control bytes would reach the pty as keys:
// CR submits, ESC[201~ ends a paste, Ctrl-Z suspends the harness.
func CheckText(text string) error {
	return checkText(claude.Composer{}, text, true)
}

// CheckMessage applies every text rule a message for Claude Code must pass
// before it is typed: CheckText, and no leading "/" unless allowSlash. It
// answers ErrBadText or nil.
func CheckMessage(text string, allowSlash bool) error {
	return checkText(claude.Composer{}, text, allowSlash)
}

// checkText is CheckMessage for any harness: the control-byte rule is the
// pty's, the rest is the harness's (RefuseText).
func checkText(c Composer, text string, allowCommands bool) error {
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			return ErrBadText
		}
	}
	if c.RefuseText(text, allowCommands) {
		return ErrBadText
	}
	return nil
}

// Code returns the stable code for err: "ok" for nil, the sentinel's code, or
// "error" for anything else.
func Code(err error) string {
	if err == nil {
		return "ok"
	}
	var ce *codeError
	if errors.As(err, &ce) {
		return ce.code
	}
	return "error"
}

// Service applies the safety checks and delegates byte-moving to drivers.
type Service struct {
	cfg      config.TerminalInputConfig
	sessions Sessions
	sys      System
	composer Composer // the harness's screen, process, text and key knowledge
	log      io.Writer
	drivers  []Driver
	now      func() time.Time
	timeout  time.Duration // per driver call
	sleep    func(ctx context.Context, d time.Duration) error
	goos     string // runtime.GOOS; terminal drivers exist for darwin only

	mu   sync.Mutex // serialises attempts and guards done
	done map[string]time.Time
	// left: per session, the text a send typed but did not submit; only that
	// text in the box may be submitted by a retry, never the owner's. Its own
	// lock, so Status never waits for an attempt.
	leftMu  sync.Mutex
	left    map[string]string
	leftGen int // counts records, so a screen read before one never drops it
	leftAt  map[string]time.Time
	// leftMarker: per session, the exact paste marker ("[Pasted text #3
	// +2 lines]") the box showed right after a send pasted left's text, with
	// the claude pid it was typed into and when. Only that marker stands for
	// aimux's text, and only while the same claude runs and the Mac had no
	// input since: the owner may clear the box and paste text of their own
	// that Claude Code shows with the same marker.
	leftMarker map[string]pasteMark
}

type pasteMark struct {
	marker string
	pid    int
	at     time.Time
}

// leftSettle is how long after typing a screen may not show the text yet.
const leftSettle = 2 * time.Second

// leftGeneration is taken before reading a screen that may drop a record.
func (s *Service) leftGeneration() int {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	return s.leftGen
}

func (s *Service) leftText(sessionID string) string {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	return s.left[sessionID]
}

// leftShownLocked reports whether scr's box shows the text a send left:
// its words, or exactly the paste marker recorded for it. leftMu is held.
func (s *Service) leftShownLocked(sessionID, scr string) bool {
	left := s.left[sessionID]
	if left == "" {
		return false
	}
	if s.composer.BoxShows(scr, left).Words {
		return true
	}
	m := s.leftMarker[sessionID].marker
	return m != "" && s.composer.Read(scr).PasteMarker == m
}

// leftShown is leftShownLocked taking leftMu.
func (s *Service) leftShown(sessionID, scr string) bool {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	return s.leftShownLocked(sessionID, scr)
}

// LeftInBox reports whether the input box holds a message a send typed but
// did not submit, as of the latest screen read: it answers from the record
// without reading the screen again. Any read (Status, Send) that shows the
// box without the message drops the record; within leftSettle of typing a
// read never does.
func (s *Service) LeftInBox(sessionID string) bool {
	return s.leftText(sessionID) != ""
}

// setLeftMarker records the paste marker scr's box shows for text, the
// send's own text just pasted into pid's claude, when the box is exactly one
// marker row.
func (s *Service) setLeftMarker(sessionID, text, scr string, pid int) {
	box := s.composer.Read(scr).PasteMarker
	if box == "" || !PasteText(text) {
		return
	}
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	if s.left[sessionID] == text {
		// Invariant: box is a real paste marker (Screen.PasteMarker is never
		// other text), so leftShownLocked and markerOK may compare the next
		// read's PasteMarker with it by plain equality.
		s.leftMarker[sessionID] = pasteMark{marker: box, pid: pid, at: s.now()}
	}
}

// markerOK reports whether the box scr shows stands for text a send left
// as its recorded paste marker: text is the record's, the box is exactly
// that marker, t runs the same claude, and the Mac has had no keyboard or
// mouse input since the marker was recorded. Anything else, an idle read
// error included, is false (fail safe).
func (s *Service) markerOK(sessionID, text, scr string, t Target) bool {
	s.leftMu.Lock()
	m, left := s.leftMarker[sessionID], s.left[sessionID]
	s.leftMu.Unlock()
	if m.marker == "" || left != text || s.composer.Read(scr).PasteMarker != m.marker || m.pid != t.PID {
		return false
	}
	idle, err := s.sys.IdleSeconds()
	return err == nil && idle >= elapsedSince(s.now(), m.at).Seconds()
}

// elapsedSince is the time from at to now by the longer of the monotonic
// and the wall clock: the monotonic clock stops while the Mac sleeps, and
// input during or after a sleep must never fall inside an undercount.
func elapsedSince(now, at time.Time) time.Duration {
	return longerElapsed(now.Sub(at), now.Round(0).Sub(at.Round(0)))
}

// longerElapsed is the longer of two readings of the same span.
func longerElapsed(mono, wall time.Duration) time.Duration {
	return max(mono, wall)
}

// forgetLeftUnlessShown drops the record of text a send left once a screen
// with no prompt shows the box without it (cleared, or sent, at the Mac):
// that text, typed again, is the owner's. gen is leftGeneration from before
// the screen was read; a record made since then stays.
func (s *Service) forgetLeftUnlessShown(sessionID, scr string, gen int) {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	left := s.left[sessionID]
	settling := s.now().Sub(s.leftAt[sessionID]) < leftSettle
	if left != "" && s.leftGen == gen && !settling && s.composer.Read(scr).Prompt == nil && !s.leftShownLocked(sessionID, scr) {
		delete(s.left, sessionID)
		delete(s.leftMarker, sessionID)
	}
}

func (s *Service) setLeft(sessionID, text string) {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	delete(s.leftMarker, sessionID) // a new record, or none: any marker is stale
	if text == "" {
		delete(s.left, sessionID)
	} else {
		s.left[sessionID] = text
		s.leftAt[sessionID] = s.now()
		s.leftGen++
	}
}

// NewService builds a Service for Claude Code sessions. log may be nil.
func NewService(cfg config.TerminalInputConfig, sessions Sessions, sys System, log io.Writer, drivers ...Driver) *Service {
	return NewServiceFor(cfg, sessions, sys, claude.Composer{}, log, drivers...)
}

// NewServiceFor is NewService for the harness that composer describes. A
// nil composer is a programming error and panics.
func NewServiceFor(cfg config.TerminalInputConfig, sessions Sessions, sys System, composer Composer, log io.Writer, drivers ...Driver) *Service {
	if composer == nil {
		panic("terminput: NewServiceFor needs a Composer")
	}
	return &Service{cfg: cfg, sessions: sessions, sys: sys, composer: composer, log: log, drivers: drivers, now: time.Now, timeout: driverTimeout, sleep: sleepCtx, goos: runtime.GOOS, done: map[string]time.Time{}, left: map[string]string{}, leftAt: map[string]time.Time{}, leftMarker: map[string]pasteMark{}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// foregroundOK reports ErrNotClaude unless the tty's foreground process
// group is t.PID's and its command is the harness's (IsAgentProcess). It runs in locate and again right before each write.
func (s *Service) foregroundOK(t Target) error {
	fg, err := s.sys.ForegroundIsPID(t.TTY, t.PID)
	if err != nil || !fg {
		return ErrNotClaude
	}
	cmd, err := s.sys.ForegroundCommand(t.TTY)
	if err != nil {
		return ErrNotClaude
	}
	if s.composer.IsAgentProcess(cmd) {
		return nil
	}
	return ErrNotClaude
}

// locate runs checks 1 to 5 and returns the driver, pane and target.
func (s *Service) locate(ctx context.Context, sessionID string) (Driver, Pane, Target, error) {
	if !s.cfg.Enabled {
		return nil, Pane{}, Target{}, ErrDisabled
	}
	pid, ok := s.sessions.TerminalPID(sessionID)
	if !ok {
		return nil, Pane{}, Target{}, ErrNotInTerminal
	}
	tty, err := s.sys.TTY(pid)
	if err != nil || tty == "" {
		return nil, Pane{}, Target{}, ErrNotInTerminal
	}
	t := Target{PID: pid, TTY: tty}
	if err := s.foregroundOK(t); err != nil {
		return nil, Pane{}, Target{}, err
	}
	if s.goos != "darwin" {
		// No driver off macOS; answer that before the idle probe (ioreg),
		// whose absence would otherwise read as mac_in_use.
		return nil, Pane{}, Target{}, ErrNoDriver
	}
	for _, d := range s.drivers {
		cctx, cancel := s.withTimeout(ctx)
		p, found, err := d.Locate(cctx, t)
		cancel()
		if err != nil {
			if errors.Is(err, ErrAutomationDenied) {
				return nil, Pane{}, Target{}, ErrAutomationDenied
			}
			return nil, Pane{}, Target{}, fmt.Errorf("%s: locate: %w", d.Name(), err)
		}
		if found {
			if err := s.inUse(ctx, d, t); err != nil {
				return nil, Pane{}, Target{}, err
			}
			return d, p, t, nil
		}
	}
	return nil, Pane{}, Target{}, ErrNoDriver
}

// inUse reports ErrMacInUse when the owner may be typing into t: the Mac
// was used in the last 2 minutes, d's terminal is in front and its focused
// session is t's tty. It fails closed: an idle or focus error, or a driver
// that cannot report focus, counts as in use. allow_while_in_use skips it.
func (s *Service) inUse(ctx context.Context, d Driver, t Target) error {
	if s.cfg.AllowWhileInUse {
		return nil
	}
	idle, err := s.sys.IdleSeconds()
	if err != nil {
		s.probeFailed(d, "idle", err)
		return ErrMacInUse
	}
	if idle >= idleRequired.Seconds() {
		return nil
	}
	fr, ok := d.(FocusReporter)
	if !ok {
		return ErrMacInUse
	}
	cctx, cancel := s.withTimeout(ctx)
	defer cancel()
	front, tty, err := fr.Focused(cctx)
	switch {
	case errors.Is(err, ErrAutomationDenied):
		return ErrAutomationDenied
	case err != nil:
		s.probeFailed(d, "focus", err)
		return ErrMacInUse
	case front && tty == t.TTY:
		return ErrMacInUse
	}
	return nil
}

// probeFailed logs a guard probe that failed, so a broken probe is visible
// rather than reading as the owner at the Mac. Probe errors carry no text.
func (s *Service) probeFailed(d Driver, check string, err error) {
	if s.log == nil {
		return
	}
	_, _ = fmt.Fprintf(s.log, "%s probe driver=%s check=%s error=%q\n", s.now().UTC().Format(time.RFC3339), d.Name(), check, err.Error())
}

func (s *Service) readScreen(ctx context.Context, d Driver, p Pane) (string, error) {
	cctx, cancel := s.withTimeout(ctx)
	defer cancel()
	scr, err := d.Screen(cctx, p)
	if err != nil {
		return "", fmt.Errorf("%s: read screen: %w", d.Name(), err)
	}
	return scr, nil
}

// Status reports state and whether Send would be accepted, without typing.
func (s *Service) Status(ctx context.Context, sessionID, registryStatus string) Status {
	st := Status{State: "unknown"}
	switch registryStatus {
	case "busy", "idle":
		st.State = registryStatus
	}
	d, p, _, err := s.locate(ctx, sessionID)
	if err != nil {
		st.Reason = Code(err)
		return st
	}
	gen := s.leftGeneration()
	scr, err := s.readScreen(ctx, d, p)
	if err != nil {
		st.Reason = Code(err)
		return st
	}
	v := s.composer.Read(scr)
	st.Prompt = v.Prompt
	st.Working = v.Working
	st.WorkingSeconds = v.WorkingSeconds
	// The text a send left waiting can be retried (Retry presses Enter).
	s.forgetLeftUnlessShown(sessionID, scr, gen)
	st.BoxHasText = v.BoxHasText
	// Text in the box (a grey suggestion or a draft) does not stop a message:
	// Send types over a suggestion and replaces or keeps a draft.
	if !v.InputEmpty && !st.BoxHasText && !s.leftShown(sessionID, scr) {
		st.Reason = Code(ErrNotAtInput)
		return st
	}
	st.CanType = true
	return st
}

func (s *Service) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

func (s *Service) remembered(sessionID, idemKey string) bool {
	if idemKey == "" {
		return false
	}
	now := s.now()
	for k, t := range s.done {
		if now.Sub(t) > idemTTL {
			delete(s.done, k)
		}
	}
	_, ok := s.done[sessionID+"\x00"+idemKey]
	return ok
}

func (s *Service) remember(sessionID, idemKey string) {
	if idemKey != "" {
		s.done[sessionID+"\x00"+idemKey] = s.now()
	}
}

func (s *Service) record(sessionID, what string, err error) {
	if s.log == nil {
		return
	}
	result := Code(err)
	var ns *notSubmittedErr
	if errors.As(err, &ns) {
		result += " " + ns.cause
	}
	_, _ = fmt.Fprintf(s.log, "%s %q %s %s\n", s.now().UTC().Format(time.RFC3339), sessionID, what, result)
}

// notSubmittedErr is ErrNotSubmitted with the check that stopped the send,
// for the attempt log (a fixed word, never text).
type notSubmittedErr struct{ cause string }

func (e *notSubmittedErr) Error() string { return ErrNotSubmitted.Error() }
func (e *notSubmittedErr) Unwrap() error { return ErrNotSubmitted }

func notSubmitted(cause string) error { return &notSubmittedErr{cause} }

// registryBusy reports whether Claude's registry, when the sessions source
// reads it, calls the session busy.
func (s *Service) registryBusy(sessionID string) bool {
	r, ok := s.sessions.(RegistryStatuses)
	return ok && r.RegistryStatus(sessionID) == "busy"
}

// Send types text into the session's pane when its input box is empty.
func (s *Service) Send(ctx context.Context, sessionID, text, idemKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.send(ctx, sessionID, text, idemKey, false, false)
	s.record(sessionID, fmt.Sprintf("text chars=%d", utf8.RuneCountInString(text)), err)
	return err
}

// SendNow is Send that is allowed while Claude works on a turn: the harness
// takes the message during the turn. Every other check is Send's, and the
// box is never touched mid-turn: a permission prompt is ErrNotAtInput, text
// in the box other than aimux's own unsent message is ErrDraftInBox (or
// ErrBoxHasUnsent, ErrUnsentUnconfirmed by Send's rules), nothing pressed.
// While Claude is not working it is Send.
func (s *Service) SendNow(ctx context.Context, sessionID, text, idemKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.send(ctx, sessionID, text, idemKey, false, true)
	s.record(sessionID, fmt.Sprintf("now text chars=%d", utf8.RuneCountInString(text)), err)
	return err
}

// SendReplacing is Send that replaces a draft left in the input box even
// with keep_drafts. Every other check is Send's: the box is never touched
// while the owner uses this session at the Mac, Claude works, or a prompt
// shows. ErrNotCleared: the box did not read empty after clearRounds;
// nothing was typed.
func (s *Service) SendReplacing(ctx context.Context, sessionID, text, idemKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.send(ctx, sessionID, text, idemKey, true, false)
	s.record(sessionID, fmt.Sprintf("replace text chars=%d", utf8.RuneCountInString(text)), err)
	return err
}

// makeRoom gets the input box scr shows ready for a message, with the keys
// the harness names (RoomKeys, ClearKeys). A box with text gets the probe
// key first: a box that then reads empty held only a suggestion, while a
// draft keeps its text and the probe is taken back out (Unprobe). A draft
// is then refused (keep) or cleared and checked again, up to clearRounds. Before each key, and so after every wait
// for the terminal, the screen must still show the box with text and safe
// (the in-use guard and the foreground check) must pass: the owner may have
// focused the pane, a shell taken it or a prompt come up meanwhile.
func (s *Service) makeRoom(ctx context.Context, d Driver, p Pane, scr string, keep bool, safe func() error) error {
	ready := func(scr string) error {
		if v := s.composer.Read(scr); !v.BoxHasText || v.Working || v.Prompt != nil {
			return ErrNotAtInput
		}
		return safe()
	}
	room := s.composer.RoomKeys()
	for round := 0; ; round++ {
		if s.composer.Read(scr).InputEmpty {
			return nil
		}
		if err := ready(scr); err != nil {
			return err
		}
		var err error
		if room.Probe != "" {
			if err := s.pressKey(ctx, d, p, room.Probe); err != nil {
				return err
			}
			if scr, err = s.settledScreen(ctx, d, p); err != nil {
				return err
			}
			if s.composer.Read(scr).InputEmpty {
				return nil // it was a suggestion; the message follows the probe
			}
			if err := ready(scr); err != nil {
				return err // the probe stays: no key goes into a box that is not safe
			}
			if room.Unprobe != "" {
				if err := s.pressKey(ctx, d, p, room.Unprobe); err != nil {
					return err
				}
			}
		}
		if keep {
			return ErrDraftInBox
		}
		if round == clearRounds {
			return ErrNotCleared
		}
		for _, k := range s.composer.ClearKeys(s.composer.Read(scr).BoxRows) {
			if err := safe(); err != nil {
				return err // each clear erases a row: never into a pane that is no longer safe
			}
			if err := s.pressKey(ctx, d, p, k); err != nil {
				return err
			}
		}
		if scr, err = s.settledScreen(ctx, d, p); err != nil {
			return err
		}
	}
}

func (s *Service) pressKey(ctx context.Context, d Driver, p Pane, k Key) error {
	cctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if err := d.Key(cctx, p, k); err != nil {
		return fmt.Errorf("%s: key %s: %w", d.Name(), k, err)
	}
	return nil
}

// settledScreen waits settle for the terminal to redraw, then reads it.
func (s *Service) settledScreen(ctx context.Context, d Driver, p Pane) (string, error) {
	if err := s.sleep(ctx, settle); err != nil {
		return "", ErrNotCleared
	}
	return s.readScreen(ctx, d, p)
}

func (s *Service) send(ctx context.Context, sessionID, text, idemKey string, replace, now bool) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if err := checkText(s.composer, text, s.cfg.AllowSlashCommands); err != nil {
		return err
	}
	// Safe before locate: only successes that passed every check are remembered.
	if s.remembered(sessionID, idemKey) && s.cfg.Enabled {
		return nil
	}
	d, p, t, err := s.locate(ctx, sessionID)
	if err != nil {
		return err
	}
	gen := s.leftGeneration()
	scr, err := s.readScreen(ctx, d, p)
	if err != nil {
		return err
	}
	v := s.composer.Read(scr)
	working := v.Working || s.registryBusy(sessionID)
	switch {
	case working && !now:
		return ErrBusy
	case now && v.Prompt != nil:
		return ErrNotAtInput
	}
	// A send that typed but did not press Enter (ErrNotSubmitted) left this
	// very text in the box (or the paste marker recorded for it, markerOK):
	// its retry presses Enter and types nothing. Other text never erases it
	// unless replacing: ErrBoxHasUnsent, nothing pressed. The same text on
	// a marker markerOK cannot confirm (Mac input since, another claude)
	// may be the owner's paste: ErrUnsentUnconfirmed, nothing pressed.
	s.forgetLeftUnlessShown(sessionID, scr, gen)
	shown := !v.InputEmpty && s.leftShown(sessionID, scr)
	same := shown && s.leftText(sessionID) == text
	waiting := same && (s.composer.BoxShows(scr, text).Words || s.markerOK(sessionID, text, scr, t))
	switch {
	case same && !waiting && !replace:
		return ErrUnsentUnconfirmed
	case shown && !waiting && !replace:
		return ErrBoxHasUnsent
	}
	if !v.InputEmpty && !waiting {
		if !v.BoxHasText {
			return ErrNotAtInput
		}
		if working {
			return ErrDraftInBox // Send now never clears or probes the box mid-turn
		}
		// Never press a key in a box the owner may be typing in at the Mac,
		// or once a shell has the pane: checked again before every key.
		safe := func() error {
			if err := s.inUse(ctx, d, t); err != nil {
				return err
			}
			return s.foregroundOK(t)
		}
		if err := s.makeRoom(ctx, d, p, scr, s.cfg.KeepDrafts && !replace, safe); err != nil {
			return err
		}
		s.setLeft(sessionID, "")
	}
	if err := s.foregroundOK(t); err != nil {
		return err // changed since locate: a shell may have the pane now
	}
	if !waiting {
		cctx, cancel := s.withTimeout(ctx)
		err = d.Type(cctx, p, text)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: type: %w", d.Name(), err)
		}
		s.setLeft(sessionID, text) // typed: until Enter, the box may hold it
		// A prompt may have appeared while typing (a background task), and
		// Enter would then answer it; the owner may have focused this session.
		// Look again first, a few times: a busy terminal can draw the typed
		// text later than settle.
		shown := false
		for range settleReads {
			if err := s.sleep(ctx, settle); err != nil {
				return notSubmitted("cancelled")
			}
			scr, err = s.readScreen(ctx, d, p)
			if err != nil {
				return err
			}
			if shown = s.composer.BoxShows(scr, text).Typed; shown || s.composer.Read(scr).Prompt != nil {
				break // shown, or a prompt took the screen: no point waiting
			}
		}
		if shown {
			s.setLeftMarker(sessionID, text, scr, t.PID) // a marker seen now stands for this text
		}
		if !shown {
			return notSubmitted("not_in_box")
		}
	}
	if err := s.inUse(ctx, d, t); err != nil {
		return notSubmitted("in_use") // the text is in the box either way
	}
	if err := s.foregroundOK(t); err != nil {
		return err
	}
	// The checks above take a moment, in which a permission prompt (routine
	// mid-turn) can appear, and Enter would approve its highlighted option:
	// look once more, right before Enter.
	scr, err = s.readScreen(ctx, d, p)
	if err != nil {
		return err
	}
	if s.composer.Read(scr).Prompt != nil {
		return notSubmitted("prompt")
	}
	m := s.composer.BoxShows(scr, text)
	// Just typed, any placeholder is the paste that just went in (Typed);
	// on the retry path only the text itself or aimux's recorded marker is.
	shows := m.Typed
	if waiting {
		shows = m.Shown || m.Words
	}
	if !shows && !s.markerOK(sessionID, text, scr, t) {
		return notSubmitted("not_in_box")
	}
	cctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if err := d.Key(cctx, p, KeyEnter); err != nil {
		return fmt.Errorf("%s: enter: %w", d.Name(), err)
	}
	s.setLeft(sessionID, "")
	s.remember(sessionID, idemKey)
	return nil
}

// Submit presses Enter for text a Send left in the input box (ErrNotSubmitted),
// only if the box visibly holds exactly that text (TextShownInBox), or, for
// pasted text a send left (leftText), exactly the paste marker that send saw
// in the box right after pasting, while the same claude runs and the Mac has
// had no input since (markerOK). Any other marker, or that one after Mac
// input, could stand for the owner's own paste and is ErrNotInBox. It types nothing. ErrBoxEmpty: the box is empty;
// ErrNotInBox: it holds something else; ErrNotAtInput: a prompt shows;
// ErrBusy: Claude is working. The other checks are Send's; the in-use
// guard runs again right before Enter.
func (s *Service) Submit(ctx context.Context, sessionID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.submit(ctx, sessionID, text)
	s.record(sessionID, fmt.Sprintf("submit chars=%d", utf8.RuneCountInString(text)), err)
	return err
}

func (s *Service) submit(ctx context.Context, sessionID, text string) error {
	d, p, t, err := s.locate(ctx, sessionID)
	if err != nil {
		return err
	}
	scr, err := s.readScreen(ctx, d, p)
	if err != nil {
		return err
	}
	v := s.composer.Read(scr)
	switch {
	case v.Working:
		return ErrBusy
	case v.Prompt != nil:
		return ErrNotAtInput
	case v.InputEmpty:
		return ErrBoxEmpty
	case !s.composer.BoxShows(scr, text).Shown && !s.markerOK(sessionID, text, scr, t):
		return ErrNotInBox
	}
	if err := s.inUse(ctx, d, t); err != nil {
		return err // the owner may have focused this session since locate
	}
	if err := s.foregroundOK(t); err != nil {
		return err
	}
	// As in send: a prompt drawn during the checks would take Enter.
	if scr, err = s.readScreen(ctx, d, p); err != nil {
		return err
	}
	if s.composer.Read(scr).Prompt != nil {
		return ErrNotAtInput
	}
	if !s.composer.BoxShows(scr, text).Shown && !s.markerOK(sessionID, text, scr, t) {
		return ErrNotInBox
	}
	cctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if err := d.Key(cctx, p, KeyEnter); err != nil {
		return fmt.Errorf("%s: enter: %w", d.Name(), err)
	}
	s.setLeft(sessionID, "")
	return nil
}

// Press presses option on the prompt identified by promptID, if that prompt
// is still the one showing.
func (s *Service) Press(ctx context.Context, sessionID, promptID string, option Key, idemKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.press(ctx, sessionID, promptID, option, idemKey)
	s.record(sessionID, fmt.Sprintf("key option=%q", string(option)), err)
	return err
}

var validKey = regexp.MustCompile(`^([1-9]|escape|enter)$`)

func (s *Service) press(ctx context.Context, sessionID, promptID string, option Key, idemKey string) error {
	if !validKey.MatchString(string(option)) {
		return ErrNoPrompt
	}
	// Safe before locate: only successes that passed every check are remembered.
	if s.remembered(sessionID, idemKey) && s.cfg.Enabled {
		return nil
	}
	d, p, t, err := s.locate(ctx, sessionID)
	if err != nil {
		return err
	}
	scr, err := s.readScreen(ctx, d, p)
	if err != nil {
		return err
	}
	pr := s.composer.Read(scr).Prompt
	if pr == nil {
		return ErrNoPrompt
	}
	if pr.ID != promptID || !hasOption(pr, option) {
		return ErrPromptChanged
	}
	if err := s.inUse(ctx, d, t); err != nil {
		return err // the owner may have focused this session since locate
	}
	if err := s.foregroundOK(t); err != nil {
		return err
	}
	cctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if err := d.Key(cctx, p, option); err != nil {
		return fmt.Errorf("%s: key: %w", d.Name(), err)
	}
	s.remember(sessionID, idemKey)
	return nil
}

func hasOption(p *Prompt, k Key) bool {
	for _, o := range p.Options {
		if o.Key == k {
			return true
		}
	}
	return false
}
