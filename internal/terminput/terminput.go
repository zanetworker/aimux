package terminput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/config"
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
	KeyEscape Key = "escape"
	KeyEnter  Key = "enter"
	// KeySpace tells Claude Code's grey suggestion from a draft: typing
	// dismisses a suggestion, so the box then reads empty; a draft keeps its
	// text and gains the space, which KeyBackspace takes back out.
	KeySpace     Key = "space"
	KeyBackspace Key = "backspace"
	// KeyClear empties the input box row the cursor is on (Ctrl+E, then
	// Ctrl+U); two per box row empty a draft from any cursor position.
	KeyClear Key = "clear"
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
	ErrBusy             = newErr("busy", "Claude is working on a turn in this session")
	// Submit's answers when the input box does not hold the message.
	ErrBoxEmpty = newErr("box_empty", "the input box is empty")
	ErrNotInBox = newErr("not_in_box", "the input box holds other text")
)

// CheckText returns ErrBadText when text holds a C0 control other than
// newline and tab, DEL or a C1 control, or starts with "!" after optional
// white space. Control bytes would reach the pty as keys: CR submits,
// ESC[201~ ends a paste, Ctrl-Z suspends claude. A leading "!" switches
// Claude Code to bash mode, where Enter runs a shell command unasked.
func CheckText(text string) error {
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			return ErrBadText
		}
	}
	if strings.HasPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), "!") {
		return ErrBadText
	}
	return nil
}

// CheckMessage applies every text rule a message must pass before it is
// typed: CheckText, and no leading "/" unless allowSlash. It answers
// ErrBadText or nil.
func CheckMessage(text string, allowSlash bool) error {
	if err := CheckText(text); err != nil {
		return err
	}
	if slashCommand(text) && !allowSlash {
		return ErrBadText
	}
	return nil
}

// slashCommand reports whether text starts with "/" after optional white
// space: Claude Code runs it as a slash command.
func slashCommand(text string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), "/")
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

// forgetLeftUnlessShown drops the record of text a send left once a screen
// with no prompt shows the box without it (cleared, or sent, at the Mac):
// that text, typed again, is the owner's. gen is leftGeneration from before
// the screen was read; a record made since then stays.
func (s *Service) forgetLeftUnlessShown(sessionID, scr string, gen int) {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	left := s.left[sessionID]
	settling := s.now().Sub(s.leftAt[sessionID]) < leftSettle
	if left != "" && s.leftGen == gen && !settling && ParsePrompt(scr) == nil && !WordsInBox(scr, left) {
		delete(s.left, sessionID)
	}
}

func (s *Service) setLeft(sessionID, text string) {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	if text == "" {
		delete(s.left, sessionID)
	} else {
		s.left[sessionID] = text
		s.leftAt[sessionID] = s.now()
		s.leftGen++
	}
}

// NewService builds a Service. log may be nil.
func NewService(cfg config.TerminalInputConfig, sessions Sessions, sys System, log io.Writer, drivers ...Driver) *Service {
	return &Service{cfg: cfg, sessions: sessions, sys: sys, log: log, drivers: drivers, now: time.Now, timeout: driverTimeout, sleep: sleepCtx, goos: runtime.GOOS, done: map[string]time.Time{}, left: map[string]string{}, leftAt: map[string]time.Time{}}
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
// group is t.PID's and its command is claude or node (Claude Code runs as
// either). It runs in locate and again right before each write.
func (s *Service) foregroundOK(t Target) error {
	fg, err := s.sys.ForegroundIsPID(t.TTY, t.PID)
	if err != nil || !fg {
		return ErrNotClaude
	}
	cmd, err := s.sys.ForegroundCommand(t.TTY)
	if err != nil {
		return ErrNotClaude
	}
	switch path.Base(strings.TrimSpace(cmd)) {
	case "claude", "node":
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
	st.Prompt = ParsePrompt(scr)
	st.Working = Working(scr)
	if st.Working {
		st.WorkingSeconds, _ = WorkingSeconds(scr)
	}
	// The text a send left waiting can be retried (Retry presses Enter).
	s.forgetLeftUnlessShown(sessionID, scr, gen)
	left := s.leftText(sessionID)
	st.BoxHasText = BoxHasText(scr)
	// Text in the box (a grey suggestion or a draft) does not stop a message:
	// Send types over a suggestion and replaces or keeps a draft.
	if !InputBoxEmpty(scr) && !st.BoxHasText && (left == "" || !WordsInBox(scr, left)) {
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
	err := s.send(ctx, sessionID, text, idemKey, false)
	s.record(sessionID, fmt.Sprintf("text chars=%d", utf8.RuneCountInString(text)), err)
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
	err := s.send(ctx, sessionID, text, idemKey, true)
	s.record(sessionID, fmt.Sprintf("replace text chars=%d", utf8.RuneCountInString(text)), err)
	return err
}

// makeRoom gets the input box scr shows ready for a message. A box with
// text gets a space first: Claude Code's grey suggestion vanishes when
// typed over, so the box then reads empty, while a draft keeps its text and
// the space is taken back out. A draft is then refused (keep) or cleared and
// checked again, up to clearRounds. Before each key, and so after every wait
// for the terminal, the screen must still show the box with text and safe
// (the in-use guard and the foreground check) must pass: the owner may have
// focused the pane, a shell taken it or a prompt come up meanwhile.
func (s *Service) makeRoom(ctx context.Context, d Driver, p Pane, scr string, keep bool, safe func() error) error {
	ready := func(scr string) error {
		if !BoxHasText(scr) || Working(scr) || ParsePrompt(scr) != nil {
			return ErrNotAtInput
		}
		return safe()
	}
	for round := 0; ; round++ {
		if InputBoxEmpty(scr) {
			return nil
		}
		if err := ready(scr); err != nil {
			return err
		}
		if err := s.pressKey(ctx, d, p, KeySpace); err != nil {
			return err
		}
		var err error
		if scr, err = s.settledScreen(ctx, d, p); err != nil {
			return err
		}
		if InputBoxEmpty(scr) {
			return nil // it was a suggestion; the message follows the space
		}
		if err := ready(scr); err != nil {
			return err // the space stays: no key goes into a box that is not safe
		}
		if err := s.pressKey(ctx, d, p, KeyBackspace); err != nil {
			return err
		}
		if keep {
			return ErrDraftInBox
		}
		if round == clearRounds {
			return ErrNotCleared
		}
		rows, _ := boxRows(scr)
		for range 2 * max(1, len(rows)) {
			if err := safe(); err != nil {
				return err // each clear erases a row: never into a pane that is no longer safe
			}
			if err := s.pressKey(ctx, d, p, KeyClear); err != nil {
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

func (s *Service) send(ctx context.Context, sessionID, text, idemKey string, replace bool) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if err := CheckMessage(text, s.cfg.AllowSlashCommands); err != nil {
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
	if Working(scr) || s.registryBusy(sessionID) {
		return ErrBusy
	}
	// A send that typed but did not press Enter (ErrNotSubmitted) left this
	// very text in the box: its retry presses Enter and types nothing.
	s.forgetLeftUnlessShown(sessionID, scr, gen)
	waiting := !InputBoxEmpty(scr) && s.leftText(sessionID) == text && WordsInBox(scr, text)
	if !InputBoxEmpty(scr) && !waiting {
		if !BoxHasText(scr) {
			return ErrNotAtInput
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
			if shown = TextInBox(scr, text); shown || ParsePrompt(scr) != nil {
				break // shown, or a prompt took the screen: no point waiting
			}
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
// only if the box visibly holds exactly that text (TextShownInBox): a paste
// marker could stand for anything, so it is not enough. It types nothing. ErrBoxEmpty: the box is empty;
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
	switch {
	case Working(scr):
		return ErrBusy
	case ParsePrompt(scr) != nil:
		return ErrNotAtInput
	case InputBoxEmpty(scr):
		return ErrBoxEmpty
	case !TextShownInBox(scr, text):
		return ErrNotInBox
	}
	if err := s.inUse(ctx, d, t); err != nil {
		return err // the owner may have focused this session since locate
	}
	if err := s.foregroundOK(t); err != nil {
		return err
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
	pr := ParsePrompt(scr)
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
