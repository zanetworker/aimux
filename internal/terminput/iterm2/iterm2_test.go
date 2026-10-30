package iterm2

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/terminput"
)

// The test binary doubles as the fake helper: run with AIMUX_FAKE_HELPER set,
// it speaks the helper's JSON-lines protocol on stdin and stdout.
func TestMain(m *testing.M) {
	if os.Getenv("AIMUX_FAKE_HELPER") != "" {
		runFakeHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeReq struct {
	ID      int    `json:"id"`
	Op      string `json:"op"`
	Window  int    `json:"window"`
	Session string `json:"session"`
	Text    string `json:"text"`
	Paste   bool   `json:"paste"`
	Key     string `json:"key"`
}

func runFakeHelper() {
	logf, _ := os.OpenFile(os.Getenv("FAKE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 G703 -- test path
	defer func() { _ = logf.Close() }()
	_, _ = fmt.Fprintln(logf, `{"op":"start"}`)
	mode := os.Getenv("FAKE_MODE")
	var sessions []loc
	_ = json.Unmarshal([]byte(os.Getenv("FAKE_SESSIONS")), &sessions)
	locates := 0
	out := bufio.NewWriter(os.Stdout)
	reply := func(v map[string]any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return // EOF: aimux closed stdin
		}
		_, _ = logf.Write(line)
		var r fakeReq
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if strings.Contains(mode, "deny") {
			reply(map[string]any{"id": r.ID, "error": "automation_denied"})
			continue
		}
		if strings.Contains(mode, "not-running") {
			// What helper.js answers while iTerm2 is not running.
			switch r.Op {
			case "locate":
				reply(map[string]any{"id": r.ID, "sessions": []loc{}})
			case "focus":
				reply(map[string]any{"id": r.ID, "frontmost": false, "tty": ""})
			default:
				reply(map[string]any{"id": r.ID, "error": "missing"})
			}
			continue
		}
		switch r.Op {
		case "focus":
			var v map[string]any
			_ = json.Unmarshal([]byte(os.Getenv("FAKE_FOCUS")), &v)
			if v == nil {
				v = map[string]any{}
			}
			v["id"] = r.ID
			reply(v)
		case "locate":
			locates++
			reply(map[string]any{"id": r.ID, "sessions": sessions})
			if locates == 1 && os.Getenv("FAKE_SESSIONS2") != "" {
				// The panes change after the first listing.
				sessions = nil
				_ = json.Unmarshal([]byte(os.Getenv("FAKE_SESSIONS2")), &sessions)
			}
		case "send", "key", "screen":
			if r.Op == "send" && strings.Contains(mode, "exit-on-send") {
				os.Exit(3)
			}
			if r.Op == "send" && strings.Contains(mode, "hang-send-once") {
				marker := os.Getenv("FAKE_MARKER")
				if _, err := os.Stat(marker); err != nil { // #nosec G703 -- test temp path
					_ = os.WriteFile(marker, nil, 0o600) // #nosec G703 -- test temp path
					time.Sleep(time.Hour)                // never answer
				}
			}
			known := false
			for _, s := range sessions {
				if s.Window == r.Window && s.Session == r.Session {
					known = true
				}
			}
			switch {
			case !known:
				reply(map[string]any{"id": r.ID, "error": "missing"})
			case r.Op == "screen":
				reply(map[string]any{"id": r.ID, "text": os.Getenv("FAKE_SCREEN")})
			default:
				reply(map[string]any{"id": r.ID, "ok": true})
			}
		default:
			reply(map[string]any{"id": r.ID, "error": "unknown op"})
		}
	}
}

type fake struct {
	t   *testing.T
	log string
	env []string
}

func newFake(t *testing.T, sessions []loc, extra ...string) *fake {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(sessions)
	f := &fake{t: t, log: filepath.Join(dir, "log")}
	f.env = append([]string{"AIMUX_FAKE_HELPER=1", "GORACE=atexit_sleep_ms=0", "FAKE_LOG=" + f.log, "FAKE_SESSIONS=" + string(b), "FAKE_MARKER=" + filepath.Join(dir, "marker")}, extra...)
	return f
}

func (f *fake) driver(timeout time.Duration) *Driver {
	d := New(Options{Timeout: timeout, Command: func(ctx context.Context) *exec.Cmd {
		c := exec.CommandContext(ctx, os.Args[0]) // #nosec G204 G702 -- the test binary
		c.Env = append(os.Environ(), f.env...)
		return c
	}})
	f.t.Cleanup(func() { _ = d.Close() })
	return d
}

// requests returns what the fake received, "start" marking each helper start.
func (f *fake) requests() []fakeReq {
	f.t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	var rs []fakeReq
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r fakeReq
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			f.t.Fatalf("bad log line %q: %v", l, err)
		}
		rs = append(rs, r)
	}
	return rs
}

func (f *fake) raw() string {
	data, _ := os.ReadFile(f.log)
	return string(data)
}

func ops(rs []fakeReq) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.Op)
	}
	return strings.Join(s, ",")
}

var scratch = []loc{{Window: 7, Session: "S-A", TTY: "/dev/ttys001"}, {Window: 8, Session: "S-B", TTY: "/dev/ttys002"}}

func TestLocateFindsAndCaches(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	p, ok, err := d.Locate(ctx, terminput.Target{PID: 1, TTY: "/dev/ttys002"})
	if err != nil || !ok {
		t.Fatalf("locate: %v %v", ok, err)
	}
	if p.Driver != "iterm2" || p.ID != "/dev/ttys002" {
		t.Fatalf("pane %+v", p)
	}
	if _, ok, err := d.Locate(ctx, terminput.Target{PID: 1, TTY: "/dev/ttys002"}); err != nil || !ok {
		t.Fatalf("second locate: %v %v", ok, err)
	}
	if got := ops(f.requests()); got != "start,locate" {
		t.Fatalf("requests %s, want one locate (cached)", got)
	}
}

func TestLocateNotFound(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	_, ok, err := d.Locate(context.Background(), terminput.Target{PID: 1, TTY: "/dev/ttys099"})
	if err != nil || ok {
		t.Fatalf("got %v %v, want not found and no error", ok, err)
	}
}

func TestSendUsesCachedIDs(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	if err := d.Type(ctx, p, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := d.Key(ctx, p, "1"); err != nil {
		t.Fatal(err)
	}
	rs := f.requests()
	if got := ops(rs); got != "start,locate,send,key" {
		t.Fatalf("requests %s", got)
	}
	s, k := rs[2], rs[3]
	if s.Window != 7 || s.Session != "S-A" || s.Text != "hello" || s.Paste {
		t.Fatalf("send %+v", s)
	}
	if k.Window != 7 || k.Session != "S-A" || k.Key != "1" {
		t.Fatalf("key %+v", k)
	}
}

func TestMissingRelocatesOnce(t *testing.T) {
	moved, _ := json.Marshal([]loc{{Window: 9, Session: "S-C", TTY: "/dev/ttys001"}})
	f := newFake(t, scratch, "FAKE_SESSIONS2="+string(moved))
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	// The fake now answers missing for S-A: the pane moved to window 9.
	if err := d.Type(ctx, p, "hi"); err != nil {
		t.Fatalf("send after move: %v", err)
	}
	rs := f.requests()
	if got := ops(rs); got != "start,locate,send,locate,send" {
		t.Fatalf("requests %s", got)
	}
	if last := rs[4]; last.Window != 9 || last.Session != "S-C" {
		t.Fatalf("retry went to %+v", last)
	}
}

func TestMissingTwiceGivesUp(t *testing.T) {
	gone, _ := json.Marshal([]loc{})
	f := newFake(t, scratch, "FAKE_SESSIONS2="+string(gone))
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	err := d.Type(ctx, p, "hi")
	if !errors.Is(err, ErrPaneGone) {
		t.Fatalf("err %v, want ErrPaneGone", err)
	}
	if got := ops(f.requests()); got != "start,locate,send,locate" {
		t.Fatalf("requests %s, want exactly one re-locate and no second send", got)
	}
}

func TestTimeoutRestartsHelper(t *testing.T) {
	f := newFake(t, scratch, "FAKE_MODE=hang-send-once")
	d := f.driver(0) // default timeout, 3 s
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	start := time.Now()
	err := d.Type(ctx, p, "hi")
	if el := time.Since(start); err == nil || el > 3500*time.Millisecond || el < 2900*time.Millisecond {
		t.Fatalf("send: err %v after %v, want an error after about 3 s", err, el)
	}
	if err := d.Type(ctx, p, "again"); err != nil {
		t.Fatalf("send after restart: %v", err)
	}
	if got := ops(f.requests()); got != "start,locate,send,start,send" {
		t.Fatalf("requests %s, want a restarted helper reusing the cached ids", got)
	}
}

func TestHelperExitIsAnErrorThenRestarts(t *testing.T) {
	f := newFake(t, scratch, "FAKE_MODE=exit-on-send")
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	if err := d.Type(ctx, p, "hi"); err == nil {
		t.Fatal("send to an exiting helper: want error")
	}
	if _, err := d.Screen(ctx, p); err != nil {
		t.Fatalf("screen after restart: %v", err)
	}
	if got := ops(f.requests()); got != "start,locate,send,start,screen" {
		t.Fatalf("requests %s", got)
	}
}

func TestAutomationDenied(t *testing.T) {
	f := newFake(t, scratch, "FAKE_MODE=deny")
	d := f.driver(time.Second)
	_, _, err := d.Locate(context.Background(), terminput.Target{TTY: "/dev/ttys001"})
	if !errors.Is(err, terminput.ErrAutomationDenied) {
		t.Fatalf("err %v, want ErrAutomationDenied", err)
	}
	if err := d.Probe(context.Background()); !errors.Is(err, terminput.ErrAutomationDenied) {
		t.Fatalf("probe err %v", err)
	}
}

func TestReplyError(t *testing.T) {
	if !errors.Is(replyError("automation_denied"), terminput.ErrAutomationDenied) {
		t.Fatal("automation_denied")
	}
	if !errors.Is(replyError("execution error: Not authorized to send Apple events to iTerm2. (-1743)"), terminput.ErrAutomationDenied) {
		t.Fatal("osascript -1743 message")
	}
	if !errors.Is(replyError("missing"), errMissing) {
		t.Fatal("missing")
	}
	if err := replyError("boom"); err == nil || !strings.Contains(err.Error(), "boom") || errors.Is(err, terminput.ErrAutomationDenied) {
		t.Fatalf("other: %v", err)
	}
}

func TestTextRoundTripsAsASCIIJSON(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	text := "say \"hi\" \\ then 'bye'\n\tسلام عليكم 👋 `$(rm -rf /)`"
	if err := d.Type(ctx, p, text); err != nil {
		t.Fatal(err)
	}
	rs := f.requests()
	if got := rs[len(rs)-1].Text; got != text {
		t.Fatalf("text %q, want %q", got, text)
	}
	for i, b := range []byte(f.raw()) {
		if b >= 0x80 {
			t.Fatalf("non-ASCII byte %#x at %d on the helper's stdin", b, i)
		}
	}
}

func TestSendRejectsControlCharacters(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	for _, text := range []string{"a\rb", "one\ntwo \x1b[201~\rtyped", "hi\x1a", "\x03", "a\x7f", "a\u009bb"} {
		if err := d.Type(ctx, p, text); !errors.Is(err, terminput.ErrBadText) {
			t.Errorf("send %q: err %v, want ErrBadText", text, err)
		}
	}
	if got := ops(f.requests()); got != "start,locate" {
		t.Fatalf("requests %s: bad text reached the helper", got)
	}
}

func TestPasteForMultiLineOrLong(t *testing.T) {
	cases := []struct {
		text  string
		paste bool
	}{
		{"short", false},
		{"two\nlines", true},
		{strings.Repeat("a", 200), false},
		{strings.Repeat("a", 201), true},
		{strings.Repeat("س", 200), false}, // 200 characters, 400 bytes
		{"a\tb", true},                    // a typed tab would trigger completion
	}
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	for _, c := range cases {
		if err := d.Type(ctx, p, c.text); err != nil {
			t.Fatal(err)
		}
	}
	rs := f.requests()[2:]
	for i, c := range cases {
		if rs[i].Paste != c.paste {
			t.Errorf("%d chars, newline %v: paste %v, want %v", len([]rune(c.text)), strings.Contains(c.text, "\n"), rs[i].Paste, c.paste)
		}
	}
}

func TestScreenReturnsText(t *testing.T) {
	f := newFake(t, scratch, "FAKE_SCREEN=❯ ready")
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	got, err := d.Screen(ctx, p)
	if err != nil || got != "❯ ready" {
		t.Fatalf("screen %q %v", got, err)
	}
}

func TestKeyRejectsUnknownKeys(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	for _, k := range []terminput.Key{"0", "10", "a", "\r", ""} {
		if err := d.Key(ctx, p, k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	for _, k := range []terminput.Key{"9", terminput.KeyEscape, terminput.KeyEnter} {
		if err := d.Key(ctx, p, k); err != nil {
			t.Errorf("key %q: %v", k, err)
		}
	}
}

func TestSendToUnlocatedPaneLocatesFirst(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	err := d.Type(context.Background(), terminput.Pane{Driver: "iterm2", ID: "/dev/ttys002"}, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := ops(f.requests()); got != "start,locate,send" {
		t.Fatalf("requests %s", got)
	}
}

func TestCloseEndsHelperAndRestartsOnUse(t *testing.T) {
	f := newFake(t, scratch)
	d := f.driver(time.Second)
	ctx := context.Background()
	if err := d.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("close took %v; the helper should exit on EOF", el)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := d.Probe(ctx); err != nil {
		t.Fatalf("probe after close: %v", err)
	}
	if got := ops(f.requests()); got != "start,locate,start,locate" {
		t.Fatalf("requests %s", got)
	}
}

func TestContextCancelReturnsPromptly(t *testing.T) {
	f := newFake(t, scratch, "FAKE_MODE=hang-send-once")
	d := f.driver(0)
	ctx := context.Background()
	p, _, _ := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"})
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := d.Type(cctx, p, "hi"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestEmbeddedHelperKeepsTextOutOfSource(t *testing.T) {
	// The script reads requests from stdin and never evaluates them.
	for _, bad := range []string{"eval(", "Function(", "doShellScript"} {
		if strings.Contains(helperJS, bad) {
			t.Fatalf("helper.js contains %q", bad)
		}
	}
	for _, want := range []string{"NSFileHandle", "JSON.parse", "-1743", "-1728", "\\u001b[200~"} {
		if !strings.Contains(helperJS, want) {
			t.Fatalf("helper.js lacks %q", want)
		}
	}
}

func TestHelperSendTypesWithoutEnter(t *testing.T) {
	// The service presses Enter itself after reading the screen again.
	i := strings.Index(helperJS, "case 'send':")
	j := strings.Index(helperJS, "case 'key':")
	if i < 0 || j < i {
		t.Fatal("helper.js has no send op before the key op")
	}
	send := helperJS[i:j]
	for _, bad := range []string{"\\r", "delay(", "newline: true"} {
		if strings.Contains(send, bad) {
			t.Fatalf("send op contains %q:\n%s", bad, send)
		}
	}
}

func TestITermNotRunning(t *testing.T) {
	f := newFake(t, scratch, "FAKE_MODE=not-running")
	d := f.driver(time.Second)
	ctx := context.Background()
	if _, ok, err := d.Locate(ctx, terminput.Target{TTY: "/dev/ttys001"}); err != nil || ok {
		t.Fatalf("locate: %v %v, want not found and no error", ok, err)
	}
	if err := d.Type(ctx, terminput.Pane{Driver: "iterm2", ID: "/dev/ttys001"}, "hi"); !errors.Is(err, ErrPaneGone) {
		t.Fatalf("type: %v, want ErrPaneGone", err)
	}
}

// helper.js must never start iTerm2: any windows access launches it, so
// every function that touches app.windows checks app.running() first.
func TestHelperChecksRunningBeforeWindows(t *testing.T) {
	if i := strings.Index(helperJS, "app.windows"); i < 0 {
		t.Fatal("helper.js never reads app.windows")
	}
	funcs := strings.Split(helperJS, "\nfunction ")
	if strings.Contains(funcs[0], "app.windows") {
		t.Fatal("app.windows used outside a function")
	}
	checked := 0
	for _, fn := range funcs[1:] {
		w := strings.Index(fn, "app.windows")
		if w < 0 {
			continue
		}
		r := strings.Index(fn, "if (!app.running())")
		if r < 0 || r > w {
			t.Fatalf("function %q reads app.windows before checking app.running():\n%s", strings.SplitN(fn, "(", 2)[0], fn)
		}
		checked++
	}
	if checked < 2 {
		t.Fatalf("checked %d functions, want locate and session", checked)
	}
}

func TestFocused(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		env       string
		wantFront bool
		wantTTY   string
		wantErr   error
	}{
		{"focused session", `FAKE_FOCUS={"frontmost":true,"tty":"/dev/ttys002"}`, true, "/dev/ttys002", nil},
		{"iTerm2 behind", `FAKE_FOCUS={"frontmost":false,"tty":"/dev/ttys002"}`, false, "/dev/ttys002", nil},
		{"no window", `FAKE_FOCUS={"frontmost":true,"tty":""}`, true, "", nil},
		{"helper error", `FAKE_FOCUS={"error":"Can't get object."}`, false, "", errors.New("any")},
		{"not running", "FAKE_MODE=not-running", false, "", nil},
		{"denied", "FAKE_MODE=deny", false, "", terminput.ErrAutomationDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, scratch, tt.env)
			front, tty, err := f.driver(time.Second).Focused(ctx)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("err = %v", err)
			case tt.wantErr != nil && err == nil:
				t.Fatalf("err = nil, want an error")
			case errors.Is(tt.wantErr, terminput.ErrAutomationDenied) && !errors.Is(err, terminput.ErrAutomationDenied):
				t.Fatalf("err = %v, want ErrAutomationDenied", err)
			}
			if err == nil && (front != tt.wantFront || tty != tt.wantTTY) {
				t.Fatalf("Focused = %v %q, want %v %q", front, tty, tt.wantFront, tt.wantTTY)
			}
			if got := ops(f.requests()); got != "start,focus" {
				t.Fatalf("ops = %s, want start,focus", got)
			}
		})
	}
}

// The focus op only reads: it never activates, raises or selects.
func TestHelperFocusOnlyReads(t *testing.T) {
	i := strings.Index(helperJS, "\nfunction focus(")
	if i < 0 {
		t.Fatal("helper.js has no focus function")
	}
	fn := helperJS[i:]
	fn = fn[:strings.Index(fn[1:], "\nfunction ")+1]
	for _, bad := range []string{"activate", "select", "reveal", "frontmost =", "frontmost=", "frontmost: true", "write("} {
		if strings.Contains(fn, bad) {
			t.Fatalf("focus function contains %q:\n%s", bad, fn)
		}
	}
	r := strings.Index(fn, "if (!app.running())")
	for _, use := range []string{"app.frontmost", "app.currentWindow"} {
		u := strings.Index(fn, use)
		if u < 0 || r < 0 || r > u {
			t.Fatalf("focus must check app.running() before %s:\n%s", use, fn)
		}
	}
	if !strings.Contains(helperJS, "case 'focus':") {
		t.Fatal("helper.js does not route the focus op")
	}
}
