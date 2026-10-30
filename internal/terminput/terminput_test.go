package terminput

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
)

type fakeSessions map[string]int

func (f fakeSessions) TerminalPID(id string) (int, bool) { pid, ok := f[id]; return pid, ok }

type fakeSystem struct {
	tty    string
	ttyErr error
	fg     bool
	idle   float64
	cmd    string // the foreground process's command
	cmdErr error
	// fgFlipAt, if set, makes the fgFlipAt-th foreground check and later
	// ones answer false: the foreground changed between checks.
	fgFlipAt  int
	fgCalls   int
	idleCalls int
	idleErr   error
	// idles, if set, answers the idle checks in turn, the last repeating.
	idles []float64
}

func (f *fakeSystem) TTY(int) (string, error) { return f.tty, f.ttyErr }
func (f *fakeSystem) ForegroundIsPID(string, int) (bool, error) {
	f.fgCalls++
	if f.fgFlipAt > 0 && f.fgCalls >= f.fgFlipAt {
		return false, nil
	}
	return f.fg, nil
}
func (f *fakeSystem) ForegroundCommand(string) (string, error) { return f.cmd, f.cmdErr }
func (f *fakeSystem) IdleSeconds() (float64, error) {
	f.idleCalls++
	if n := len(f.idles); n > 0 {
		return f.idles[min(f.idleCalls, n)-1], f.idleErr
	}
	return f.idle, f.idleErr
}

type fakeDriver struct {
	locateOK  bool
	locateErr error
	screen    string
	screenErr error
	sent      []string
	keys      []Key
	targets   []Target
	// afterType, if set, is the screen once text is typed; otherwise the
	// typed text shows in the input box.
	afterType string
	// What Focused answers: iTerm2 in front, its focused session's tty.
	// focusTTYs, if set, answers the calls in turn, the last repeating.
	front      bool
	focusTTY   string
	focusTTYs  []string
	focusErr   error
	focusCalls int
}

func (d *fakeDriver) Focused(context.Context) (bool, string, error) {
	d.focusCalls++
	if n := len(d.focusTTYs); n > 0 {
		return d.front, d.focusTTYs[min(d.focusCalls, n)-1], d.focusErr
	}
	return d.front, d.focusTTY, d.focusErr
}

// noFocusDriver is a driver that cannot report focus.
type noFocusDriver struct{ d *fakeDriver }

func (n noFocusDriver) Name() string { return n.d.Name() }
func (n noFocusDriver) Locate(ctx context.Context, t Target) (Pane, bool, error) {
	return n.d.Locate(ctx, t)
}
func (n noFocusDriver) Type(ctx context.Context, p Pane, text string) error {
	return n.d.Type(ctx, p, text)
}
func (n noFocusDriver) Key(ctx context.Context, p Pane, k Key) error { return n.d.Key(ctx, p, k) }
func (n noFocusDriver) Screen(ctx context.Context, p Pane) (string, error) {
	return n.d.Screen(ctx, p)
}

func (d *fakeDriver) Name() string { return "fake" }
func (d *fakeDriver) Locate(_ context.Context, t Target) (Pane, bool, error) {
	d.targets = append(d.targets, t)
	return Pane{Driver: "fake", ID: "p1"}, d.locateOK, d.locateErr
}
func (d *fakeDriver) Type(_ context.Context, _ Pane, text string) error {
	d.sent = append(d.sent, text)
	if d.afterType != "" {
		d.screen = d.afterType
	} else {
		d.screen = boxWith(text)
	}
	return nil
}

func boxWith(text string) string {
	if PasteText(text) {
		text = "[Pasted text #1 +2 lines]"
	}
	return strings.Replace(idleScreen, "❯\n", "❯ "+text+"\n", 1)
}
func (d *fakeDriver) Key(_ context.Context, _ Pane, k Key) error {
	d.keys = append(d.keys, k)
	if k == KeyEnter && d.afterType == "" {
		d.screen = idleScreen // submitted: the box is empty again
	}
	return nil
}
func (d *fakeDriver) Screen(context.Context, Pane) (string, error) { return d.screen, d.screenErr }

type env struct {
	svc *Service
	sys *fakeSystem
	drv *fakeDriver
	log *bytes.Buffer
	now time.Time
}

const idleScreen = "⏺ Done.\n\n────────────────────────────────────────────────────────────────────────────────\n❯\n────────────────────────────────────────────────────────────────────────────────\n  [Model] │ demo\n"

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		sys: &fakeSystem{tty: "/dev/ttys001", fg: true, idle: 600, cmd: "claude"},
		drv: &fakeDriver{locateOK: true, screen: idleScreen},
		log: &bytes.Buffer{},
		now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
	}
	e.svc = NewService(config.TerminalInputConfig{Enabled: true}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
	e.svc.goos = "darwin"
	e.svc.now = func() time.Time { return e.now }
	e.svc.sleep = func(context.Context, time.Duration) error { return nil }
	e.svc.goos = "darwin"
	return e
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want code %q", code)
	}
	if got := Code(err); got != code {
		t.Fatalf("Code(%v) = %q, want %q", err, got, code)
	}
}

func TestSendDisabled(t *testing.T) {
	e := newEnv(t)
	e.svc = NewService(config.TerminalInputConfig{}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
	e.svc.goos = "darwin"
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "disabled")
	if len(e.drv.sent) != 0 {
		t.Fatal("typed while disabled")
	}
}

func TestSendNotInTerminal(t *testing.T) {
	e := newEnv(t)
	wantCode(t, e.svc.Send(context.Background(), "other", "hi", ""), "not_in_terminal")
	e.sys.ttyErr = errors.New("no such process")
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_in_terminal")
	e.sys.ttyErr, e.sys.tty = nil, ""
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_in_terminal")
}

func TestSendNotClaude(t *testing.T) {
	e := newEnv(t)
	e.sys.fg = false
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_claude")
	if len(e.drv.targets) != 0 {
		t.Fatal("driver consulted before foreground check passed")
	}
}

func TestSendMacInUse(t *testing.T) {
	e := newEnv(t)
	e.sys.idle = 119
	e.drv.front, e.drv.focusTTY = true, "/dev/ttys001" // the owner is in this session
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "mac_in_use")
	e.sys.idle = 120 // boundary: exactly 120 s is allowed
	if err := e.svc.Send(context.Background(), "s1", "hi", ""); err != nil {
		t.Fatalf("at 120s: %v", err)
	}
}

func TestSendMacInUseOverride(t *testing.T) {
	e := newEnv(t)
	e.svc = NewService(config.TerminalInputConfig{Enabled: true, AllowWhileInUse: true}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
	e.svc.goos = "darwin"
	e.sys.idle = 1
	if err := e.svc.Send(context.Background(), "s1", "hi", ""); err != nil {
		t.Fatalf("override: %v", err)
	}
}

func TestSendNoDriver(t *testing.T) {
	e := newEnv(t)
	e.drv.locateOK = false
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "no_terminal_driver")
	e.svc = NewService(config.TerminalInputConfig{Enabled: true}, fakeSessions{"s1": 42}, e.sys, e.log)
	e.svc.goos = "darwin"
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "no_terminal_driver")
}

func TestSendAutomationDenied(t *testing.T) {
	e := newEnv(t)
	e.drv.locateErr = ErrAutomationDenied
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "automation_denied")
	e.drv.locateErr = errors.New("osascript blew up")
	err := e.svc.Send(context.Background(), "s1", "hi", "")
	if err == nil || Code(err) == "ok" || Code(err) == "automation_denied" {
		t.Fatalf("other driver errors must stay distinct, got %v / %q", err, Code(err))
	}
}

func TestSendNotAtInputWhenPromptShowing(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_at_input")
	e.drv.screen = fixture(t, "draft-in-box.txt")
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_at_input")
	if len(e.drv.sent) != 0 {
		t.Fatal("typed over a prompt or draft")
	}
}

func TestSendScreenError(t *testing.T) {
	e := newEnv(t)
	e.drv.screenErr = errors.New("boom")
	err := e.svc.Send(context.Background(), "s1", "hi", "")
	if err == nil || len(e.drv.sent) != 0 {
		t.Fatalf("screen error must refuse typing, err=%v sent=%v", err, e.drv.sent)
	}
}

func TestSendTypesWhenBoxEmpty(t *testing.T) {
	e := newEnv(t)
	var slept time.Duration
	e.svc.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	if err := e.svc.Send(context.Background(), "s1", "hello\nworld", ""); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.sent) != 1 || e.drv.sent[0] != "hello\nworld" {
		t.Fatalf("sent = %q", e.drv.sent)
	}
	if len(e.drv.keys) != 1 || e.drv.keys[0] != KeyEnter || slept != 300*time.Millisecond {
		t.Fatalf("keys = %v after %v, want Enter after 300ms", e.drv.keys, slept)
	}
	if got := e.drv.targets[0]; got.PID != 42 || got.TTY != "/dev/ttys001" {
		t.Fatalf("target = %+v", got)
	}
}

func TestSendIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.svc.Send(ctx, "s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Send(ctx, "s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.sent) != 1 {
		t.Fatalf("typed %d times, want 1", len(e.drv.sent))
	}
	// other session / other key are distinct
	if err := e.svc.Send(ctx, "s1", "hi", "k2"); err != nil {
		t.Fatal(err)
	}
	// empty key is never remembered
	_ = e.svc.Send(ctx, "s1", "a", "")
	_ = e.svc.Send(ctx, "s1", "a", "")
	if len(e.drv.sent) != 4 {
		t.Fatalf("sent = %d, want 4", len(e.drv.sent))
	}
	// expiry: 10 minutes
	e.now = e.now.Add(10*time.Minute + time.Second)
	_ = e.svc.Send(ctx, "s1", "hi", "k1")
	if len(e.drv.sent) != 5 {
		t.Fatalf("expired key should type again, sent = %d", len(e.drv.sent))
	}
}

func TestSendFailureNotRemembered(t *testing.T) {
	e := newEnv(t)
	e.sys.fg = false
	_ = e.svc.Send(context.Background(), "s1", "hi", "k1")
	e.sys.fg = true
	if err := e.svc.Send(context.Background(), "s1", "hi", "k1"); err != nil || len(e.drv.sent) != 1 {
		t.Fatalf("failed attempt must not be remembered: err=%v sent=%d", err, len(e.drv.sent))
	}
}

func bashPromptID(t *testing.T, screen string) (string, *Prompt) {
	t.Helper()
	p := ParsePrompt(screen)
	if p == nil {
		t.Fatal("fixture has no prompt")
	}
	return p.ID, p
}

func TestPressMatchingPrompt(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	if err := e.svc.Press(context.Background(), "s1", id, "2", ""); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.keys) != 1 || e.drv.keys[0] != "2" {
		t.Fatalf("keys = %v", e.drv.keys)
	}
}

func TestPressPromptChanged(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-create-file.txt")
	wantCode(t, e.svc.Press(context.Background(), "s1", "stale-id", "1", ""), "prompt_changed")
	if len(e.drv.keys) != 0 {
		t.Fatal("pressed a key on a different prompt")
	}
}

func TestPressUnknownOption(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	wantCode(t, e.svc.Press(context.Background(), "s1", id, "9", ""), "prompt_changed")
	wantCode(t, e.svc.Press(context.Background(), "s1", id, "escape", ""), "prompt_changed")
	if len(e.drv.keys) != 0 {
		t.Fatal("pressed a key that is not an option")
	}
}

func TestPressNoPrompt(t *testing.T) {
	e := newEnv(t)
	wantCode(t, e.svc.Press(context.Background(), "s1", "x", "1", ""), "no_prompt")
}

func TestPressChecksRunFirst(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	e.sys.fg = false
	wantCode(t, e.svc.Press(context.Background(), "s1", id, "1", ""), "not_claude")
	e.sys.fg, e.sys.idle = true, 5
	e.drv.front, e.drv.focusTTY = true, "/dev/ttys001"
	wantCode(t, e.svc.Press(context.Background(), "s1", id, "1", ""), "mac_in_use")
	if len(e.drv.keys) != 0 {
		t.Fatalf("pressed %v into the focused session", e.drv.keys)
	}
}

func TestPressIdempotent(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	ctx := context.Background()
	if err := e.svc.Press(ctx, "s1", id, "1", "k"); err != nil {
		t.Fatal(err)
	}
	e.drv.screen = idleScreen // prompt gone; retry must still succeed silently
	if err := e.svc.Press(ctx, "s1", id, "1", "k"); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.keys) != 1 {
		t.Fatalf("keys = %v", e.drv.keys)
	}
}

func TestLogHasNoText(t *testing.T) {
	e := newEnv(t)
	_ = e.svc.Send(context.Background(), "s1", "secret-text-héllo", "")
	line := e.log.String()
	if strings.Contains(line, "secret-text") {
		t.Fatalf("log leaks text: %q", line)
	}
	for _, want := range []string{"2026-10-27T12:00:00Z", "s1", "text", "17", "ok"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q missing %q", line, want)
		}
	}
	e.log.Reset()
	e.sys.fg = false
	_ = e.svc.Send(context.Background(), "s1", "x", "")
	if !strings.Contains(e.log.String(), "not_claude") {
		t.Fatalf("log = %q", e.log.String())
	}
	e.log.Reset()
	e.sys.fg = true
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	_ = e.svc.Press(context.Background(), "s1", id, "2", "")
	if l := e.log.String(); !strings.Contains(l, "key") || !strings.Contains(l, "2") || strings.Count(l, "\n") != 1 {
		t.Fatalf("key log = %q", l)
	}
}

func TestStatusReasons(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	st := e.svc.Status(ctx, "s1", "idle")
	if st.State != "idle" || !st.CanType || st.Reason != "" || st.Prompt != nil {
		t.Fatalf("ok status = %+v", st)
	}
	if st := e.svc.Status(ctx, "s1", "busy"); st.State != "busy" || !st.CanType {
		t.Fatalf("busy = %+v", st)
	}
	if st := e.svc.Status(ctx, "s1", "weird"); st.State != "unknown" {
		t.Fatalf("state = %q", st.State)
	}
	if len(e.drv.sent) != 0 {
		t.Fatal("Status typed")
	}

	cases := []struct {
		name   string
		mutate func(*env)
		reason string
	}{
		{"not_claude", func(e *env) { e.sys.fg = false }, "not_claude"},
		{"mac_in_use", func(e *env) { e.sys.idle, e.drv.front, e.drv.focusTTY = 3, true, "/dev/ttys001" }, "mac_in_use"},
		{"no_driver", func(e *env) { e.drv.locateOK = false }, "no_terminal_driver"},
		{"automation", func(e *env) { e.drv.locateErr = ErrAutomationDenied }, "automation_denied"},
		{"draft", func(e *env) { e.drv.screen = fixture(t, "draft-in-box.txt") }, "not_at_input"},
	}
	for _, c := range cases {
		e := newEnv(t)
		c.mutate(e)
		st := e.svc.Status(ctx, "s1", "idle")
		if st.CanType || st.Reason != c.reason {
			t.Errorf("%s: status = %+v, want reason %q", c.name, st, c.reason)
		}
	}

	e = newEnv(t)
	e.svc = NewService(config.TerminalInputConfig{}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
	e.svc.goos = "darwin"
	if st := e.svc.Status(ctx, "s1", "idle"); st.CanType || st.Reason != "disabled" {
		t.Errorf("disabled: %+v", st)
	}
	e = newEnv(t)
	if st := e.svc.Status(ctx, "nope", "idle"); st.CanType || st.Reason != "not_in_terminal" {
		t.Errorf("not in terminal: %+v", st)
	}

	e = newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	st = e.svc.Status(ctx, "s1", "busy")
	if st.CanType || st.Reason != "not_at_input" || st.Prompt == nil || len(st.Prompt.Options) != 3 || st.Prompt.ID == "" {
		t.Errorf("prompt showing: %+v", st)
	}
}

func TestOpenLogMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "terminal-input.log")
	w, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
	w2, err := OpenLog(path) // appends
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w2.Write([]byte("y\n"))
	_ = w2.Close()
	b, _ := os.ReadFile(path) //nolint:gosec // temp test path
	if string(b) != "x\ny\n" {
		t.Fatalf("content = %q", b)
	}
}

func TestParseIdle(t *testing.T) {
	out := `    | |   "IOPlatformSerialNumber" = "X"
    | |   "HIDIdleTime" = 2500000000
`
	got, err := parseHIDIdle(out)
	if err != nil || got != 2.5 {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseHIDIdle("nothing"); err == nil {
		t.Fatal("want error when HIDIdleTime is missing")
	}
}

func TestParsePS(t *testing.T) {
	if got, err := parseTTY("ttys001\n"); err != nil || got != "/dev/ttys001" {
		t.Fatalf("tty = %q, %v", got, err)
	}
	for _, none := range []string{"??\n", "\n", "-\n"} {
		if got, _ := parseTTY(none); got != "" {
			t.Fatalf("parseTTY(%q) = %q, want empty", none, got)
		}
	}
	if n, err := parseInt("  1234 \n"); err != nil || n != 1234 {
		t.Fatalf("parseInt = %d, %v", n, err)
	}
	if _, err := parseInt("x"); err == nil {
		t.Fatal("want error")
	}
}

func TestOpenLogTightensExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil { //nolint:gosec // deliberately loose
		t.Fatal(err)
	}
	w, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

type blockingDriver struct{ fakeDriver }

func (d *blockingDriver) Screen(ctx context.Context, _ Pane) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestDriverCallTimesOut(t *testing.T) {
	e := newEnv(t)
	bd := &blockingDriver{fakeDriver{locateOK: true}}
	e.svc = NewService(config.TerminalInputConfig{Enabled: true}, fakeSessions{"s1": 42}, e.sys, e.log, bd)
	e.svc.goos = "darwin"
	e.svc.timeout = 50 * time.Millisecond
	start := time.Now()
	err := e.svc.Send(context.Background(), "s1", "hi", "")
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v, want timeout error", err, time.Since(start))
	}
	if len(bd.sent) != 0 {
		t.Fatal("typed after a timed-out screen read")
	}
	// the mutex was released: a second call also returns
	if err := e.svc.Send(context.Background(), "s1", "hi", ""); err == nil {
		t.Fatal("want error")
	}
}

func TestPressRejectsBadOption(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	for _, bad := range []Key{"", "0", "12", "1\n2026-01-01 forged", "ESCAPE"} {
		wantCode(t, e.svc.Press(context.Background(), "s1", id, bad, ""), "no_prompt")
	}
	if len(e.drv.keys) != 0 || strings.Count(e.log.String(), "\n") != 5 {
		t.Fatalf("keys=%v log=%q", e.drv.keys, e.log.String())
	}
	if strings.Contains(e.log.String(), "forged\n") {
		t.Fatalf("log injection: %q", e.log.String())
	}
}

func TestLogQuotesSessionID(t *testing.T) {
	e := newEnv(t)
	_ = e.svc.Send(context.Background(), "a\nb", "x", "")
	if strings.Count(e.log.String(), "\n") != 1 {
		t.Fatalf("log = %q", e.log.String())
	}
}

func TestConcurrentDuplicateSend(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.svc.Send(context.Background(), "s1", "hi", "same")
		}()
	}
	wg.Wait()
	if len(e.drv.sent) != 1 {
		t.Fatalf("typed %d times, want 1", len(e.drv.sent))
	}
}

func TestSendRejectsControlCharacters(t *testing.T) {
	for name, text := range map[string]string{
		"CR":     "a\rb",
		"ESC":    "two\nlines \x1b[201~\rrm -rf ~",
		"Ctrl-Z": "hi\x1a",
		"NUL":    "a\x00b",
		"DEL":    "a\x7fb",
		"CSI":    "a\u009bb",
		"C1 end": "a\u0080b\u009f",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			wantCode(t, e.svc.Send(context.Background(), "s1", text, "k"), "bad_text")
			if len(e.drv.sent) != 0 || len(e.drv.targets) != 0 {
				t.Fatalf("driver used for bad text: sent %q, targets %v", e.drv.sent, e.drv.targets)
			}
			if got := e.log.String(); !strings.Contains(got, " bad_text\n") || strings.Contains(got, text) {
				t.Fatalf("log %q: want the code and not the text", got)
			}
		})
	}
}

func TestSendAllowsNewlineTabAndUnicode(t *testing.T) {
	e := newEnv(t)
	text := "line one\n\tindented سلام 👋  é"
	if err := e.svc.Send(context.Background(), "s1", text, ""); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.sent) != 1 || e.drv.sent[0] != text {
		t.Fatalf("sent %q", e.drv.sent)
	}
}

func TestSendDisabledBeforeBadText(t *testing.T) {
	e := newEnv(t)
	e.svc = NewService(config.TerminalInputConfig{}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
	e.svc.goos = "darwin"
	wantCode(t, e.svc.Send(context.Background(), "s1", "a\rb", ""), "disabled")
}

func TestSendNoEnterWhenPromptAppears(t *testing.T) {
	e := newEnv(t)
	e.drv.afterType = fixture(t, "prompt-bash.txt") // a prompt took the screen while typing
	err := e.svc.Send(context.Background(), "s1", "hello", "k1")
	wantCode(t, err, "not_submitted")
	if len(e.drv.sent) != 1 || len(e.drv.keys) != 0 {
		t.Fatalf("sent %q keys %v, want the text typed and no Enter", e.drv.sent, e.drv.keys)
	}
	if !strings.Contains(e.log.String(), " not_submitted not_in_box\n") { // which check stopped it, never the text
		t.Fatalf("log = %q", e.log.String())
	}
	// not remembered: a retry with the same key reads the screen again
	e.drv.screen = idleScreen
	e.drv.afterType = ""
	if err := e.svc.Send(context.Background(), "s1", "hello", "k1"); err != nil || len(e.drv.keys) != 1 {
		t.Fatalf("retry: err %v keys %v", err, e.drv.keys)
	}
}

func TestSendNoEnterUnlessTextInBox(t *testing.T) {
	for name, after := range map[string]string{
		"box still empty": idleScreen,
		"other text":      boxWith("something else"),
		"bash mode":       strings.Replace(idleScreen, "❯\n", "! hello\n", 1),
		"screen gone":     "",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.drv.afterType = after
			if after == "" {
				e.drv.afterType = "\n"
			}
			wantCode(t, e.svc.Send(context.Background(), "s1", "hello", ""), "not_submitted")
			if len(e.drv.keys) != 0 {
				t.Fatalf("pressed %v", e.drv.keys)
			}
		})
	}
}

func TestSendSecondScreenReadError(t *testing.T) {
	e := newEnv(t)
	e.svc.sleep = func(context.Context, time.Duration) error { e.drv.screenErr = errors.New("boom"); return nil }
	if err := e.svc.Send(context.Background(), "s1", "hello", ""); err == nil || len(e.drv.keys) != 0 {
		t.Fatalf("err %v keys %v, want an error and no Enter", err, e.drv.keys)
	}
}

func TestSendSettleCancelled(t *testing.T) {
	e := newEnv(t)
	e.svc.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := e.svc.Send(context.Background(), "s1", "hello", ""); err == nil || len(e.drv.keys) != 0 {
		t.Fatalf("err %v keys %v, want an error and no Enter", err, e.drv.keys)
	}
}

func TestSendRefusesBashMode(t *testing.T) {
	for _, text := range []string{"!ls", "! rm -rf ~", "   !ls", "\t!ls", "\n!ls"} {
		e := newEnv(t)
		e.svc = NewService(config.TerminalInputConfig{Enabled: true, AllowSlashCommands: true}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
		e.svc.goos = "darwin"
		wantCode(t, e.svc.Send(context.Background(), "s1", text, ""), "bad_text")
		if len(e.drv.sent) != 0 || len(e.drv.targets) != 0 {
			t.Fatalf("%q reached the driver", text)
		}
	}
	e := newEnv(t)
	if err := e.svc.Send(context.Background(), "s1", "run it! now !ls", ""); err != nil || len(e.drv.sent) != 1 {
		t.Fatalf("a later ! is allowed: err %v sent %q", err, e.drv.sent)
	}
}

func TestSendSlashCommands(t *testing.T) {
	for _, text := range []string{"/clear", "  /model opus", "\n/compact"} {
		e := newEnv(t)
		wantCode(t, e.svc.Send(context.Background(), "s1", text, ""), "bad_text")
		if len(e.drv.sent) != 0 {
			t.Fatalf("%q typed with slash commands off", text)
		}
	}
	e := newEnv(t)
	if err := e.svc.Send(context.Background(), "s1", "use a/b and /tmp", ""); err != nil {
		t.Fatalf("a later / is allowed: %v", err)
	}
	e = newEnv(t)
	e.svc = NewService(config.TerminalInputConfig{Enabled: true, AllowSlashCommands: true}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
	e.svc.goos = "darwin"
	e.svc.sleep = func(context.Context, time.Duration) error { return nil }
	if err := e.svc.Send(context.Background(), "s1", "/compact", ""); err != nil || len(e.drv.sent) != 1 {
		t.Fatalf("allow_slash_commands: err %v sent %q", err, e.drv.sent)
	}
}

func TestCheckTextLeadingBang(t *testing.T) {
	if CheckText("  !x") != ErrBadText || CheckText("x!") != nil || CheckText("/x") != nil {
		t.Fatal("CheckText refuses a leading ! only; / is the service's rule")
	}
}

func TestSendRechecksForegroundBeforeTyping(t *testing.T) {
	e := newEnv(t)
	e.sys.fgFlipAt = 2 // true in locate, false once the screen was read
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_claude")
	if len(e.drv.sent) != 0 || e.sys.fgCalls != 2 {
		t.Fatalf("sent %q after %d checks, want nothing typed after 2", e.drv.sent, e.sys.fgCalls)
	}
}

func TestSendRechecksForegroundBeforeEnter(t *testing.T) {
	e := newEnv(t)
	e.sys.fgFlipAt = 3 // changed while the text settled
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_claude")
	if len(e.drv.sent) != 1 || len(e.drv.keys) != 0 {
		t.Fatalf("sent %q keys %v, want typed and no Enter", e.drv.sent, e.drv.keys)
	}
}

func TestPressRechecksForeground(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	e.sys.fgFlipAt = 2
	wantCode(t, e.svc.Press(context.Background(), "s1", id, "1", ""), "not_claude")
	if len(e.drv.keys) != 0 {
		t.Fatalf("pressed %v", e.drv.keys)
	}
}

func TestForegroundCommandMustBeClaudeOrNode(t *testing.T) {
	for cmd, ok := range map[string]bool{
		"claude": true, "node": true, "/usr/local/bin/claude": true, "/opt/homebrew/bin/node": true,
		"zsh": false, "-zsh": false, "bash": false, "claude-wrapper": false, "vim": false, "": false,
	} {
		e := newEnv(t)
		e.sys.cmd = cmd
		err := e.svc.Send(context.Background(), "s1", "hi", "")
		if ok && err != nil {
			t.Errorf("%q: %v, want typed", cmd, err)
		}
		if !ok {
			if Code(err) != "not_claude" || len(e.drv.sent) != 0 {
				t.Errorf("%q: %v sent %q, want not_claude and nothing typed", cmd, err, e.drv.sent)
			}
		}
	}
	e := newEnv(t)
	e.sys.cmdErr = errors.New("ps failed")
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_claude")
	if st := e.svc.Status(context.Background(), "s1", "idle"); st.CanType || st.Reason != "not_claude" {
		t.Errorf("status %+v, want not_claude", st)
	}
}

func TestParseComm(t *testing.T) {
	for out, want := range map[string]string{"claude\n": "claude", "  /usr/local/bin/node \n": "/usr/local/bin/node", "\n-zsh\n": "-zsh"} {
		if got, err := parseComm(out); err != nil || got != want {
			t.Errorf("parseComm(%q) = %q, %v, want %q", out, got, err, want)
		}
	}
	if _, err := parseComm(" \n"); err == nil {
		t.Error("want an error for empty output")
	}
}

// Off macOS there is no terminal driver: the answer is no_terminal_driver
// before the idle probe (ioreg), so a missing ioreg never reads as mac_in_use.
func TestNonDarwinAnswersNoDriverBeforeIdle(t *testing.T) {
	e := newEnv(t)
	e.svc.goos = "linux"
	e.sys.idle = 0
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "no_terminal_driver")
	if st := e.svc.Status(context.Background(), "s1", "idle"); st.Reason != "no_terminal_driver" {
		t.Fatalf("status %+v", st)
	}
	if e.sys.idleCalls != 0 || len(e.drv.targets) != 0 || len(e.drv.sent) != 0 {
		t.Fatalf("idle probed %d times, driver used %v", e.sys.idleCalls, e.drv.targets)
	}
	e = newEnv(t)
	e.sys.idle = 0
	e.drv.front, e.drv.focusTTY = true, "/dev/ttys001"
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "mac_in_use")
}

// The guard blocks only when the Mac was used in the last 2 minutes AND
// iTerm2 is in front AND its focused session is the target's tty, unless
// allow_while_in_use is set.
func TestGuardTable(t *testing.T) {
	ctx := context.Background()
	for _, idle := range []float64{119, 120} {
		for _, front := range []bool{true, false} {
			for _, focus := range []string{"/dev/ttys001", "/dev/ttys002"} {
				for _, allow := range []bool{false, true} {
					name := fmt.Sprintf("idle=%v front=%v focus=%s allow=%v", idle, front, focus, allow)
					blocked := idle < 120 && front && focus == "/dev/ttys001" && !allow
					e := newEnv(t)
					e.svc = NewService(config.TerminalInputConfig{Enabled: true, AllowWhileInUse: allow}, fakeSessions{"s1": 42}, e.sys, e.log, e.drv)
					e.svc.goos = "darwin"
					e.svc.sleep = func(context.Context, time.Duration) error { return nil }
					e.sys.idle, e.drv.front, e.drv.focusTTY = idle, front, focus

					st := e.svc.Status(ctx, "s1", "idle")
					err := e.svc.Send(ctx, "s1", "hi", "")
					if blocked {
						if st.CanType || st.Reason != "mac_in_use" {
							t.Errorf("%s: status = %+v, want mac_in_use", name, st)
						}
						if Code(err) != "mac_in_use" || len(e.drv.sent) != 0 || len(e.drv.keys) != 0 {
							t.Errorf("%s: send = %v, typed %q keys %v; want mac_in_use and nothing typed", name, err, e.drv.sent, e.drv.keys)
						}
						continue
					}
					if !st.CanType || err != nil || len(e.drv.keys) != 1 {
						t.Errorf("%s: status = %+v, send = %v, keys %v; want typed and submitted", name, st, err, e.drv.keys)
					}
					if (idle >= 120 || allow) && e.drv.focusCalls != 0 {
						t.Errorf("%s: asked iTerm2 for focus %d times, want none", name, e.drv.focusCalls)
					}
				}
			}
		}
	}
}

func TestGuardFailsClosed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.sys.idleErr = errors.New("ioreg failed")
	wantCode(t, e.svc.Send(ctx, "s1", "hi", ""), "mac_in_use")

	e = newEnv(t)
	e.sys.idle, e.drv.focusErr = 5, errors.New("iterm2: helper died")
	wantCode(t, e.svc.Send(ctx, "s1", "hi", ""), "mac_in_use")

	e = newEnv(t)
	e.sys.idle, e.drv.focusErr = 5, ErrAutomationDenied
	wantCode(t, e.svc.Send(ctx, "s1", "hi", ""), "automation_denied")

	// A driver that cannot tell which session is focused blocks every
	// session while the Mac is in use, as before.
	e = newEnv(t)
	e.sys.idle = 5
	e.svc = NewService(config.TerminalInputConfig{Enabled: true}, fakeSessions{"s1": 42}, e.sys, e.log, noFocusDriver{e.drv})
	e.svc.goos = "darwin"
	wantCode(t, e.svc.Send(ctx, "s1", "hi", ""), "mac_in_use")
	if len(e.drv.sent) != 0 {
		t.Fatal("typed through a driver without focus")
	}
	e.sys.idle = 600
	e.svc.sleep = func(context.Context, time.Duration) error { return nil }
	if err := e.svc.Send(ctx, "s1", "hi", ""); err != nil {
		t.Fatalf("idle Mac, no focus capability: %v", err)
	}
}

// The owner clicks into the target session between typing and Enter: the
// text stays in the box and Enter is never pressed.
func TestGuardFocusChangeBeforeEnter(t *testing.T) {
	e := newEnv(t)
	e.sys.idle, e.drv.front = 5, true
	e.drv.focusTTYs = []string{"/dev/ttys002", "/dev/ttys001"}
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_submitted")
	if len(e.drv.sent) != 1 || len(e.drv.keys) != 0 {
		t.Fatalf("typed %q keys %v; want typed once and no Enter", e.drv.sent, e.drv.keys)
	}
}

// The Mac goes from idle to in use between typing and Enter, with the
// target focused.
func TestGuardIdleDropBeforeEnter(t *testing.T) {
	e := newEnv(t)
	e.sys.idles = []float64{600, 1}
	e.drv.front, e.drv.focusTTY = true, "/dev/ttys001"
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_submitted")
	if len(e.drv.keys) != 0 {
		t.Fatalf("pressed %v", e.drv.keys)
	}
}

func TestSendBusyWhileWorking(t *testing.T) {
	ctx := context.Background()
	for _, f := range []string{"busy-empty.txt", "busy-queued-hint.txt", "working-todos.txt"} {
		e := newEnv(t)
		e.drv.screen = fixture(t, f)
		wantCode(t, e.svc.Send(ctx, "s1", "hi", "k1"), "busy")
		if len(e.drv.sent) != 0 || len(e.drv.keys) != 0 {
			t.Fatalf("%s: typed %q keys %v while working", f, e.drv.sent, e.drv.keys)
		}
		// A refused send is not remembered: the same key goes through later.
		e.drv.screen = idleScreen
		if err := e.svc.Send(ctx, "s1", "hi", "k1"); err != nil || len(e.drv.sent) != 1 {
			t.Fatalf("%s: after the turn: %v, typed %q", f, err, e.drv.sent)
		}
	}
}

func TestStatusWorking(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if st := e.svc.Status(ctx, "s1", "idle"); st.Working {
		t.Fatalf("idle screen: %+v", st)
	}
	e.drv.screen = fixture(t, "busy-empty.txt")
	// CanType stays true so clients without a queue still offer the
	// composer; Send answers busy.
	if st := e.svc.Status(ctx, "s1", "busy"); !st.Working || !st.CanType || st.State != "busy" {
		t.Fatalf("working screen: %+v", st)
	}
	e.drv.screen = fixture(t, "idle-after-turn.txt")
	if st := e.svc.Status(ctx, "s1", "idle"); st.Working || !st.CanType {
		t.Fatalf("finished turn: %+v", st)
	}
}

// A broken probe still blocks, and leaves one log line saying which probe
// of which driver failed, never the message text.
func TestGuardProbeErrorLogged(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, want string
		mutate     func(*env)
	}{
		{"idle", "check=idle", func(e *env) { e.sys.idleErr = errors.New("ioreg exited 1") }},
		{"focus", "check=focus", func(e *env) { e.sys.idle, e.drv.focusErr = 5, errors.New("helper died") }},
	} {
		e := newEnv(t)
		c.mutate(e)
		wantCode(t, e.svc.Send(ctx, "s1", "secret words", ""), "mac_in_use")
		l := e.log.String()
		if strings.Count(l, "probe") != 1 || !strings.Contains(l, c.want) || !strings.Contains(l, "driver=fake") || strings.Contains(l, "secret") {
			t.Errorf("%s send: log = %q", c.name, l)
		}
		e.log.Reset()
		if st := e.svc.Status(ctx, "s1", "idle"); st.Reason != "mac_in_use" {
			t.Errorf("%s status: %+v", c.name, st)
		}
		if l := e.log.String(); strings.Count(l, "probe") != 1 || !strings.Contains(l, c.want) {
			t.Errorf("%s status: log = %q", c.name, l)
		}
	}
	// A blocked guard without a probe error logs no probe line.
	e := newEnv(t)
	e.sys.idle, e.drv.front, e.drv.focusTTY = 5, true, "/dev/ttys001"
	wantCode(t, e.svc.Send(ctx, "s1", "hi", ""), "mac_in_use")
	if strings.Contains(e.log.String(), "probe") {
		t.Errorf("log = %q", e.log.String())
	}
}

// The owner focuses the session between reading the prompt and the key.
func TestPressGuardBeforeKey(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = fixture(t, "prompt-bash.txt")
	id, _ := bashPromptID(t, e.drv.screen)
	e.sys.idle, e.drv.front = 5, true
	e.drv.focusTTYs = []string{"/dev/ttys002", "/dev/ttys001"}
	wantCode(t, e.svc.Press(context.Background(), "s1", id, "1", "k1"), "mac_in_use")
	if len(e.drv.keys) != 0 || e.drv.focusCalls != 2 {
		t.Fatalf("keys %v, focus asked %d times", e.drv.keys, e.drv.focusCalls)
	}
	// Not remembered: the same key goes through once the owner leaves.
	e.drv.focusTTYs = nil
	e.drv.focusTTY = "/dev/ttys002"
	if err := e.svc.Press(context.Background(), "s1", id, "1", "k1"); err != nil || len(e.drv.keys) != 1 {
		t.Fatalf("after: %v keys %v", err, e.drv.keys)
	}
}

// iTerm2 in front with no window: no session gets keystrokes.
func TestGuardFrontmostNoWindow(t *testing.T) {
	e := newEnv(t)
	e.sys.idle, e.drv.front, e.drv.focusTTY = 5, true, ""
	if err := e.svc.Send(context.Background(), "s1", "hi", ""); err != nil || len(e.drv.keys) != 1 {
		t.Fatalf("send: %v keys %v", err, e.drv.keys)
	}
}

func TestCheckMessage(t *testing.T) {
	cases := []struct {
		text       string
		allowSlash bool
		want       error
	}{
		{"run the tests", false, nil},
		{"line one\n\tline two", false, nil},
		{"  /clear", false, ErrBadText},
		{"  /clear", true, nil},
		{"a /path in the middle", false, nil},
		{"!ls", true, ErrBadText},
		{"x\x1b[201~", true, ErrBadText},
		{"x\r", true, ErrBadText},
		{"x\u0085", true, ErrBadText},
		{"", false, nil}, // emptiness is the caller's rule
	}
	for _, c := range cases {
		if got := CheckMessage(c.text, c.allowSlash); got != c.want {
			t.Errorf("CheckMessage(%q, %v) = %v, want %v", c.text, c.allowSlash, got, c.want)
		}
	}
}

func TestSubmitPressesEnterWhenBoxHoldsText(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = boxWith("hello there")
	if err := e.svc.Submit(context.Background(), "s1", "hello there"); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.sent) != 0 || len(e.drv.keys) != 1 || e.drv.keys[0] != KeyEnter {
		t.Fatalf("typed %q keys %v, want Enter only", e.drv.sent, e.drv.keys)
	}
}

func TestSubmitRefusesOtherText(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		screen, code string
	}{
		"empty box":    {idleScreen, "box_empty"},
		"other text":   {boxWith("hello there, owner"), "not_in_box"},
		"prompt":       {fixture(t, "prompt-bash.txt"), "not_at_input"},
		"working":      {fixture(t, "busy-empty.txt"), "busy"},
		"stale prefix": {boxWith("hello"), "not_in_box"},
		"paste marker": {strings.Replace(idleScreen, "❯\n", "❯ [Pasted text #1 +2 lines]\n", 1), "not_in_box"},
	} {
		e := newEnv(t)
		e.drv.screen = tc.screen
		err := e.svc.Submit(ctx, "s1", "hello there")
		if Code(err) != tc.code {
			t.Fatalf("%s: Code = %q (%v), want %q", name, Code(err), err, tc.code)
		}
		if len(e.drv.keys) != 0 || len(e.drv.sent) != 0 {
			t.Fatalf("%s: pressed %v typed %q", name, e.drv.keys, e.drv.sent)
		}
	}
}

func TestSubmitRunsTheChecks(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.drv.screen = boxWith("hi")
	e.svc.cfg.Enabled = false
	wantCode(t, e.svc.Submit(ctx, "s1", "hi"), "disabled")

	e = newEnv(t)
	e.drv.screen = boxWith("hi")
	e.sys.idle, e.drv.front, e.drv.focusTTY = 3, true, "/dev/ttys001"
	wantCode(t, e.svc.Submit(ctx, "s1", "hi"), "mac_in_use")

	e = newEnv(t)
	e.drv.screen = boxWith("hi")
	e.sys.fgFlipAt = 2 // the foreground changes after locate
	wantCode(t, e.svc.Submit(ctx, "s1", "hi"), "not_claude")
	if len(e.drv.keys) != 0 {
		t.Fatalf("pressed %v", e.drv.keys)
	}
}

func TestSubmitLogHasNoText(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = boxWith("secret words")
	if err := e.svc.Submit(context.Background(), "s1", "secret words"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.log.String(), "secret") || !strings.Contains(e.log.String(), "submit") {
		t.Fatalf("log = %q", e.log.String())
	}
}

func TestSubmitNeedsTheTextItselfNotAPasteMarker(t *testing.T) {
	long := strings.Repeat("word ", 60) // pasted: over 200 characters
	e := newEnv(t)
	e.drv.screen = boxWith(long) // the fake shows the marker for pasted text
	wantCode(t, e.svc.Submit(context.Background(), "s1", long), "not_in_box")
	if len(e.drv.keys) != 0 {
		t.Fatalf("pressed %v on a paste marker", e.drv.keys)
	}
	e.drv.screen = strings.Replace(idleScreen, "❯\n", "❯ "+long+"\n", 1)
	if err := e.svc.Submit(context.Background(), "s1", long); err != nil {
		t.Fatalf("the text itself in the box: %v", err)
	}
}

// The Mac goes from idle to in use, with the target focused, between
// Submit's screen read and Enter: Enter is not pressed.
func TestSubmitGuardBeforeEnter(t *testing.T) {
	e := newEnv(t)
	e.drv.screen = boxWith("hi")
	e.sys.idles = []float64{600, 1}
	e.drv.front, e.drv.focusTTY = true, "/dev/ttys001"
	wantCode(t, e.svc.Submit(context.Background(), "s1", "hi"), "mac_in_use")
	if len(e.drv.keys) != 0 {
		t.Fatalf("pressed %v", e.drv.keys)
	}
	if e.sys.idleCalls != 2 {
		t.Fatalf("idle checked %d times, want at locate and before Enter", e.sys.idleCalls)
	}
}

// registrySessions also reports each session's status in Claude's registry.
type registrySessions struct {
	fakeSessions
	status map[string]string
}

func (r registrySessions) RegistryStatus(id string) string { return r.status[id] }

// A session Claude's registry calls busy refuses a direct send even when
// the screen shows no working line, as the dispatcher's idle check does.
func TestSendBusyByRegistry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	reg := registrySessions{fakeSessions{"s1": 42}, map[string]string{"s1": "busy"}}
	e.svc.sessions = reg
	wantCode(t, e.svc.Send(ctx, "s1", "hi", ""), "busy")
	if len(e.drv.sent) != 0 || len(e.drv.keys) != 0 {
		t.Fatalf("typed %q keys %v", e.drv.sent, e.drv.keys)
	}
	reg.status["s1"] = "idle"
	if err := e.svc.Send(ctx, "s1", "hi", ""); err != nil || len(e.drv.sent) != 1 {
		t.Fatalf("idle registry: %v, typed %q", err, e.drv.sent)
	}
}

// A retry after not_submitted finds its own text still in the box: it presses
// Enter instead of refusing the box as not empty, and types nothing.
func TestSendSubmitsTheSameTextWaitingInTheBox(t *testing.T) {
	e := newEnv(t)
	e.drv.afterType = boxWith("my own draft") // the first attempt cannot see its text: not submitted
	wantCode(t, e.svc.Send(context.Background(), "s1", "give me an example", "k1"), "not_submitted")
	e.drv.screen, e.drv.afterType = boxWith("give me an example"), ""
	e.drv.sent = nil
	if err := e.svc.Send(context.Background(), "s1", "give me an example", "k2"); err != nil {
		t.Fatal(err)
	}
	if len(e.drv.sent) != 0 || len(e.drv.keys) != 1 || e.drv.keys[0] != KeyEnter {
		t.Fatalf("sent %q keys %v, want Enter only", e.drv.sent, e.drv.keys)
	}
}

// Text aimux did not leave there is the owner's draft, even when it reads the
// same without spaces: refused, nothing typed or pressed.
func TestSendNeverSubmitsTheOwnersDraft(t *testing.T) {
	for name, draft := range map[string]string{
		"never typed by aimux":    "give me an example",
		"same letters, no spaces": "giveme anexample",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if name != "never typed by aimux" {
				e.drv.afterType = boxWith("something else")
				wantCode(t, e.svc.Send(context.Background(), "s1", "give me an example", ""), "not_submitted")
				e.drv.sent = nil
			}
			e.drv.screen = boxWith(draft)
			wantCode(t, e.svc.Send(context.Background(), "s1", "give me an example", ""), "not_at_input")
			if len(e.drv.sent) != 0 || len(e.drv.keys) != 0 {
				t.Fatalf("sent %q keys %v over a draft", e.drv.sent, e.drv.keys)
			}
		})
	}
}

func TestSendLogsWhyItWasNotSubmitted(t *testing.T) {
	e := newEnv(t)
	e.sys.idle = 5
	// The owner focuses this session while it types: the check before Enter sees it.
	e.drv.front, e.drv.focusTTYs = true, []string{"/dev/ttys009", "/dev/ttys001"}
	wantCode(t, e.svc.Send(context.Background(), "s1", "hello", ""), "not_submitted")
	if !strings.Contains(e.log.String(), " not_submitted in_use\n") {
		t.Fatalf("log = %q", e.log.String())
	}
}
