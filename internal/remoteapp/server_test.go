package remoteapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/terminput/iterm2"
	"github.com/zanetworker/aimux/internal/waiting"
)

// eventLog records what the fake services did, in order.
type eventLog struct {
	mu  sync.Mutex
	log []string
}

func (e *eventLog) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *eventLog) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

func indexOf(log []string, s string) int {
	for i, l := range log {
		if l == s {
			return i
		}
	}
	return -1
}

// fakeJob starts, says so, and returns when ctx ends.
func fakeJob(ev *eventLog, name string, await bool, started *sync.WaitGroup) job {
	started.Add(1)
	return job{name: name, await: await, run: func(ctx context.Context) {
		ev.add("start " + name)
		started.Done()
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // a job that takes a moment to stop
		ev.add("stop " + name)
	}}
}

// A fake set of services: every job starts, every awaited job stops on
// cancel before anything closes, and the closers run last first (the
// queue file, the audit log, the terminal input log, the drivers, the
// chat manager: the reverse of the order they were opened).
func TestServe_StartsEveryJobAndStopsThemBeforeTheClosers(t *testing.T) {
	ev := &eventLog{}
	var started sync.WaitGroup
	w := &wiring{}
	for _, c := range []string{"chat manager", "terminal drivers", "terminal input log", "audit log", "queue file"} {
		w.onClose(func() { ev.add("close " + c) })
	}
	w.jobs = []job{
		fakeJob(ev, "device login", false, &started),
		fakeJob(ev, "chat queue", true, &started),
		fakeJob(ev, "namer", true, &started),
		fakeJob(ev, "last focus", true, &started),
		fakeJob(ev, "folders", true, &started),
		fakeJob(ev, "waiting alerts", true, &started),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w.ln = ln
	w.srv = &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	w.banner = func(addr net.Addr) string { return "on " + addr.String() + "\n" }

	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		defer w.close()
		done <- w.serve(ctx, &out)
	}()
	started.Wait()
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("server not serving: %v", err)
	}
	_ = resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop on cancel")
	}
	// close runs after serve returns; wait for its last line.
	deadline := time.Now().Add(2 * time.Second)
	for indexOf(ev.all(), "close chat manager") < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	log := ev.all()
	for _, j := range w.jobs {
		if indexOf(log, "start "+j.name) < 0 {
			t.Errorf("job %q never started: %v", j.name, log)
		}
	}
	firstClose := indexOf(log, "close queue file")
	if firstClose < 0 {
		t.Fatalf("closers never ran: %v", log)
	}
	for _, name := range []string{"chat queue", "namer", "last focus", "folders", "waiting alerts"} {
		if i := indexOf(log, "stop "+name); i < 0 || i > firstClose {
			t.Errorf("awaited job %q stopped at %d, after the first closer at %d: %v", name, i, firstClose, log)
		}
	}
	closes := []string{}
	for _, l := range log {
		if strings.HasPrefix(l, "close ") {
			closes = append(closes, l)
		}
	}
	if got := strings.Join(closes, ", "); got != "close queue file, close audit log, close terminal input log, close terminal drivers, close chat manager" {
		t.Errorf("closers ran as %s", got)
	}
	if out.String() != "on "+ln.Addr().String()+"\n" {
		t.Errorf("startup lines %q", out.String())
	}
}

// A job that is not awaited does not hold up the shutdown.
func TestStartJobs_WaitsOnlyForAwaitedJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	defer close(block)
	var ran atomic.Int32
	wait := startJobs(ctx, []job{
		{name: "stuck", run: func(context.Context) { ran.Add(1); <-block }},
		{name: "awaited", await: true, run: func(ctx context.Context) { ran.Add(1); <-ctx.Done() }},
	})
	cancel()
	waited := make(chan struct{})
	go func() { wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("wait blocked on a job that is not awaited")
	}
	if wait := startJobs(ctx, nil); wait == nil {
		t.Fatal("no jobs: want a wait that returns")
	} else {
		wait()
	}
}

func TestEvery_RunsEachPeriodUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	done := make(chan struct{})
	go func() { every(5*time.Millisecond, func() { n.Add(1) })(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n.Load() < 2 {
		t.Fatalf("ran %d times", n.Load())
	}
	cancel()
	<-done
	// Cancelled before the first tick: never runs.
	var none atomic.Int32
	dead, stop := context.WithCancel(context.Background())
	stop()
	every(time.Hour, func() { none.Add(1) })(dead)
	if none.Load() != 0 {
		t.Fatal("ran after cancel")
	}
}

func TestRefreshing_RunsAtOnceLogsFailuresAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	var n atomic.Int32
	done := make(chan struct{})
	go func() {
		refreshing(func(context.Context) error {
			if n.Add(1) == 1 {
				return errors.New("index locked")
			}
			return nil
		}, 5*time.Millisecond, &out)(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if n.Load() < 2 || out.String() != "index locked\n" {
		t.Fatalf("runs %d, log %q", n.Load(), out.String())
	}
	// A failure because ctx ended is not logged.
	var quiet syncBuffer
	dead, stop := context.WithCancel(context.Background())
	stop()
	refreshing(func(ctx context.Context) error { return ctx.Err() }, time.Hour, &quiet)(dead)
	if quiet.String() != "" {
		t.Fatalf("logged %q after cancel", quiet.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fixedAddr string

func (a fixedAddr) Network() string { return "tcp" }
func (a fixedAddr) String() string  { return string(a) }

// The startup lines, pinned: what remote serve has always printed.
func TestStartupLines_Pinned(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	st := startup{port: 7382, allowed: []string{"me@example.com"}, hosts: []string{"mac.tail1.ts.net"}, hooksSettings: settings}
	want := "aimux remote API on http://127.0.0.1:7382/v1 for [me@example.com] via [mac.tail1.ts.net]\n" +
		"Expose it on your tailnet (HTTPS, tailnet only):\n" +
		"  tailscale serve --bg --https=8443 http://127.0.0.1:7382\n" +
		"Then open the web client on your phone: https://mac.tail1.ts.net:8443/app/\n" +
		"Permission alerts for terminal sessions: off; run `aimux remote hooks install`\n"
	if got := st.lines(fixedAddr("127.0.0.1:7382")); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := waiting.Install(settings, "/x/aimux remote hooks notify", time.Now()); err != nil {
		t.Fatal(err)
	}
	st.push, st.terminalOn, st.drivers = "ntfy topic k3x9", true, 1
	want = "aimux remote API on http://127.0.0.1:7382/v1 for [me@example.com] via [mac.tail1.ts.net]\n" +
		"Expose it on your tailnet (HTTPS, tailnet only):\n" +
		"  tailscale serve --bg --https=8443 http://127.0.0.1:7382\n" +
		"Then open the web client on your phone: https://mac.tail1.ts.net:8443/app/\n" +
		"Approval notifications: ntfy topic k3x9\n" +
		"Permission alerts for terminal sessions: on (Claude Code hooks)\n" +
		"Terminal input: on (1 terminal driver(s)); run `aimux remote terminal-input check` once at the Mac\n"
	if got := st.lines(fixedAddr("127.0.0.1:7382")); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// testServer is remote serve over a temp HOME: a named host (no
// tailscale), no terminal drivers, port 0.
func testServer(t *testing.T, cfg config.Config, out, errOut *syncBuffer) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home) // the search service finds its corpus through HOME
	cfg.RemoteAccess.Hostnames = []string{"mac.tail1.ts.net"}
	return New(cfg, Options{
		Home: home, Allowed: []string{"me@example.com"}, AdapterCommand: "true", MaxRuns: 1, ApprovalTimeout: time.Minute,
		Version: "test", Out: out, ErrOut: errOut,
		DetectHost: func() (string, error) { return "", errors.New("tailscale must not be asked") },
		Drivers:    func() ([]terminput.Driver, func()) { return nil, func() {} },
		Reserve:    acprun.Reservation(home),
	}), home
}

// The real build wires every job, in start order, with the awaited ones
// marked; optional jobs appear when their switch is on.
func TestBuild_WiresEveryJob(t *testing.T) {
	names := func(w *wiring) string {
		var s []string
		for _, j := range w.jobs {
			n := j.name
			if j.await {
				n += "*"
			}
			s = append(s, n)
		}
		return strings.Join(s, ", ")
	}
	var out, errOut syncBuffer
	s, _ := testServer(t, config.Config{}, &out, &errOut)
	w := &wiring{}
	defer w.close()
	if err := s.build(w); err != nil {
		t.Fatal(err)
	}
	if got, want := names(w), "device login, queue events, chat queue*, folders*, waiting alerts*, reap idle runs, search refresh"; got != want {
		t.Fatalf("jobs %s, want %s", got, want)
	}
	if len(w.closers) != 6 {
		t.Fatalf("%d closers, want 6 (chat manager, drivers, terminal input log, audit log, queue file, listener)", len(w.closers))
	}

	off := false
	cfg := config.Config{}
	cfg.Sessions.AutoTitle = true
	cfg.RemoteAccess.DeviceLogin = &off
	s2, _ := testServer(t, cfg, &out, &errOut)
	w2 := &wiring{}
	defer w2.close()
	if err := s2.build(w2); err != nil {
		t.Fatal(err)
	}
	if got, want := names(w2), "queue events, chat queue*, namer*, folders*, waiting alerts*, reap idle runs, search refresh"; got != want {
		t.Fatalf("jobs %s, want %s", got, want)
	}
}

// The listener is one of the closers: a build whose serve never runs
// still frees its port.
func TestBuild_ClosesTheListener(t *testing.T) {
	var out, errOut syncBuffer
	s, _ := testServer(t, config.Config{}, &out, &errOut)
	w := &wiring{}
	if err := s.build(w); err != nil {
		w.close()
		t.Fatal(err)
	}
	addr := w.ln.Addr().String()
	w.close()
	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port still held after close: %v", err)
	}
	_ = again.Close()
}

// With no Reserve in Options, New still takes the cross-process session
// lock (~/.aimux/locks), so a second claim of a session is refused.
func TestNew_NilReserveStillLocksSessions(t *testing.T) {
	home := t.TempDir()
	s := New(config.Config{}, Options{Home: home})
	if s.opts.Reserve == nil {
		t.Fatal("nil Reserve left nil: the session lock is off")
	}
	release, err := s.opts.Reserve("s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acprun.SessionLock(filepath.Join(home, ".aimux", "locks"), "s1"); !errors.Is(err, acprun.ErrInUseElsewhere) {
		t.Fatalf("second claim: %v, want ErrInUseElsewhere", err)
	}
	if _, err := New(config.Config{}, Options{Home: home}).opts.Reserve("s1"); !errors.Is(err, acprun.ErrInUseElsewhere) {
		t.Fatalf("another server's claim: %v, want ErrInUseElsewhere", err)
	}
}

// Run end to end on a temp HOME: the startup lines (pinned), the audit log
// and search index it writes, and a clean stop on cancel with the drivers
// closed and the search refresh finished.
func TestRun_StartsPrintsAndStopsOnCancel(t *testing.T) {
	var out, errOut syncBuffer
	s, home := testServer(t, config.Config{}, &out, &errOut)
	closed := &atomic.Bool{}
	s.opts.Drivers = func() ([]terminput.Driver, func()) { return nil, func() { closed.Store(true) } }
	refreshDone := make(chan struct{})
	s.wrapJob = func(j job) job {
		if j.name != "search refresh" {
			return j
		}
		run := j.run
		j.run = func(ctx context.Context) { defer close(refreshDone); run(ctx) }
		return j
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	addr := regexp.MustCompile(`http://(127\.0\.0\.1:\d+)/v1`)
	deadline := time.Now().Add(10 * time.Second)
	for !addr.MatchString(out.String()) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	m := addr.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no startup line: %q (stderr %q)", out.String(), errOut.String())
	}
	resp, err := http.Get("http://" + m[1] + "/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("health without tailscale headers: %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	index := filepath.Join(home, ".aimux", "remote-search.db")
	for _, err := os.Stat(index); err != nil && time.Now().Before(deadline); _, err = os.Stat(index) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	if !closed.Load() {
		t.Fatal("terminal drivers not closed")
	}
	select {
	case <-refreshDone: // its writes are over before the temp dir goes
	case <-time.After(15 * time.Second):
		t.Fatal("the search refresh did not stop")
	}
	want := fmt.Sprintf("aimux remote API on http://%s/v1 for [me@example.com] via [mac.tail1.ts.net]\n", m[1]) +
		"Expose it on your tailnet (HTTPS, tailnet only):\n" +
		"  tailscale serve --bg --https=8443 http://127.0.0.1:0\n" +
		"Then open the web client on your phone: https://mac.tail1.ts.net:8443/app/\n" +
		"Permission alerts for terminal sessions: off; run `aimux remote hooks install`\n"
	if out.String() != want {
		t.Fatalf("stdout\n%q\nwant\n%q", out.String(), want)
	}
	if !strings.Contains(errOut.String(), "Device login: on") {
		t.Fatalf("stderr %q, want the device login line", errOut.String())
	}
	for _, f := range []string{filepath.Join("logs", "remote-audit.log"), "remote-search.db"} {
		if _, err := os.Stat(filepath.Join(home, ".aimux", f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// A port in use stops Run with the listen error, after closing what it
// had opened.
func TestRun_PortInUseIsAnErrorAndClosesWhatWasOpened(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	var out, errOut syncBuffer
	s, _ := testServer(t, config.Config{}, &out, &errOut)
	s.opts.Port = busy.Addr().(*net.TCPAddr).Port
	closed := &atomic.Bool{}
	s.opts.Drivers = func() ([]terminput.Driver, func()) { return nil, func() { closed.Store(true) } }
	err = s.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "choose another port with --port") {
		t.Fatalf("err %v", err)
	}
	if !closed.Load() || out.String() != "" {
		t.Fatalf("drivers closed %v, stdout %q", closed.Load(), out.String())
	}
}

// Without configured names and with tailscale down, Run refuses before
// opening anything.
func TestRun_NoHostIsAnError(t *testing.T) {
	var out, errOut syncBuffer
	s, _ := testServer(t, config.Config{}, &out, &errOut)
	s.cfg.RemoteAccess.Hostnames = nil
	if err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "hostnames") {
		t.Fatalf("err %v", err)
	}
}

// fakeITerm2 is a terminal driver named like iTerm2's; it never types.
type fakeITerm2 struct{}

func (fakeITerm2) Name() string { return "iterm2" }
func (fakeITerm2) Locate(context.Context, terminput.Target) (terminput.Pane, bool, error) {
	return terminput.Pane{}, false, nil
}
func (fakeITerm2) Type(context.Context, terminput.Pane, string) error {
	return errors.New("no typing in tests")
}
func (fakeITerm2) Key(context.Context, terminput.Pane, terminput.Key) error {
	return errors.New("no keys in tests")
}
func (fakeITerm2) Screen(context.Context, terminput.Pane) (string, error) { return "", nil }

// openCall is one call of the fake opener.
type openCall struct {
	title, dir string
	argv       []string
}

// fakeClaudeOnPath puts an executable named claude first on PATH, so the
// provider's resume command resolves without Claude Code installed, and
// returns its path. It is never run.
func fakeClaudeOnPath(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return claude
}

// openOnMacRig is remote serve built over a temp HOME with device login off
// and terminal input on, its drivers and opener given by the test, and a
// fake claude on PATH. It returns the handler and the HOME.
func openOnMacRig(t *testing.T, drivers []terminput.Driver, open func(ctx context.Context, title string, argv []string, dir string) error) (http.Handler, string) {
	t.Helper()
	fakeClaudeOnPath(t)
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	cfg.RemoteAccess.TerminalInput.Enabled = true
	var out, errOut syncBuffer
	s, home := testServer(t, cfg, &out, &errOut)
	s.opts.Drivers = func() ([]terminput.Driver, func()) { return drivers, func() {} }
	s.opts.OpenTab = open
	w := &wiring{}
	t.Cleanup(w.close)
	if err := s.build(w); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Fatalf("build printed %q", out.String())
	}
	return w.srv.Handler, home
}

// serveRig sends one request through the handler as tailscale serve would.
func serveRig(h http.Handler, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "mac.tail1.ts.net"
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("Tailscale-User-Login", "me@example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// seedClaudeSession writes a Claude Code transcript for id, recorded in cwd.
func seedClaudeSession(t *testing.T, home, id, cwd, prompt string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-w")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-11-01T10:00:00Z", "cwd": cwd, "sessionId": id, "entrypoint": "cli",
		"message": map[string]any{"role": "user", "content": prompt}})
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// holdInTerminal writes Claude Code's registry entry for this test process
// holding id, so the session reads as open in a terminal.
func holdInTerminal(home, id string) error {
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, os.Getpid(), id)
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid())), []byte(reg), 0o600)
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return body.Code
}

func healthOpenOnMac(t *testing.T, h http.Handler) bool {
	t.Helper()
	rec := serveRig(h, http.MethodGet, "/v1/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("health %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	on, ok := body["openOnMac"].(bool)
	if !ok {
		t.Fatalf("health %s has no openOnMac", rec.Body.String())
	}
	return on
}

func neverOpen(t *testing.T) func(context.Context, string, []string, string) error {
	return func(context.Context, string, []string, string) error {
		t.Error("the opener ran")
		return errors.New("must not open")
	}
}

// Without an iTerm2 driver, Open on Mac is off: health says so and the
// route answers no_terminal without opening anything.
func TestOpenOnMac_NoDriverIsOff(t *testing.T) {
	h, home := openOnMacRig(t, nil, neverOpen(t))
	seedClaudeSession(t, home, "s1", t.TempDir(), "fix the build")
	if healthOpenOnMac(t, h) {
		t.Fatal("openOnMac true with no terminal driver")
	}
	rec := serveRig(h, http.MethodPost, "/v1/sessions/s1/open-on-mac")
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "no_terminal" {
		t.Fatalf("%d %s, want 503 no_terminal", rec.Code, rec.Body.String())
	}
}

// With terminal input off, an iTerm2 driver alone does not turn it on.
func TestOpenOnMac_TerminalInputOffIsOff(t *testing.T) {
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	var out, errOut syncBuffer
	s, _ := testServer(t, cfg, &out, &errOut)
	s.opts.Drivers = func() ([]terminput.Driver, func()) { return []terminput.Driver{fakeITerm2{}}, func() {} }
	s.opts.OpenTab = neverOpen(t)
	w := &wiring{}
	defer w.close()
	if err := s.build(w); err != nil {
		t.Fatal(err)
	}
	if healthOpenOnMac(t, w.srv.Handler) {
		t.Fatal("openOnMac true with terminal input off")
	}
}

// With an iTerm2 driver and terminal input on, the route opens the
// provider's resume command in the session's folder, titled with the
// shown title cut to 60 runes, and answers once the terminal holds the
// session (the hold it takes meanwhile must not hide that).
func TestOpenOnMac_OpensTheResumeCommandInTheSessionFolder(t *testing.T) {
	var calls []openCall
	var home string
	h, home := openOnMacRig(t, []terminput.Driver{fakeITerm2{}}, func(ctx context.Context, title string, argv []string, dir string) error {
		calls = append(calls, openCall{title: title, argv: argv, dir: dir})
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the opener's context has no deadline")
		}
		return holdInTerminal(home, "s1") // the new tab's claude takes the session
	})
	cwd := t.TempDir()
	prompt := strings.Repeat("a", 70) // under history's own cap on the first message
	seedClaudeSession(t, home, "s1", cwd, prompt)
	if !healthOpenOnMac(t, h) {
		t.Fatal("openOnMac false with an iTerm2 driver and terminal input on")
	}
	rec := serveRig(h, http.MethodPost, "/v1/sessions/s1/open-on-mac")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"open_in_terminal"`) {
		t.Fatalf("%d %s, want 200 open_in_terminal", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 {
		t.Fatalf("opener ran %d times", len(calls))
	}
	c := calls[0]
	if want := []string{exec.Command("claude").Path, "--resume", "s1"}; strings.Join(c.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("argv %q, want %q", c.argv, want)
	}
	if c.dir != cwd {
		t.Fatalf("dir %q, want %q", c.dir, cwd)
	}
	if c.title != strings.Repeat("a", 60) {
		t.Fatalf("title %q, want the first message cut to 60 runes", c.title)
	}
}

// A session created here whose agent wrote no transcript yet has nothing
// to resume: not_started, nothing opened.
func TestOpenOnMac_CreatedWithoutFileIsNotStarted(t *testing.T) {
	h, home := openOnMacRig(t, []terminput.Driver{fakeITerm2{}}, neverOpen(t))
	created := &remoteapi.CreatedStore{Path: filepath.Join(home, ".aimux", "remote-created.json")}
	if err := created.Add("new1", t.TempDir(), "start here"); err != nil {
		t.Fatal(err)
	}
	rec := serveRig(h, http.MethodPost, "/v1/sessions/new1/open-on-mac")
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "not_started" {
		t.Fatalf("%d %s, want 409 not_started", rec.Code, rec.Body.String())
	}
	rec = serveRig(h, http.MethodPost, "/v1/sessions/nobody/open-on-mac")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "unknown_session" {
		t.Fatalf("unknown: %d %s, want 404 unknown_session", rec.Code, rec.Body.String())
	}
}

// Another aimux process continuing the session (its lock held) answers
// in_use before anything is opened.
func TestOpenOnMac_LockedElsewhereIsInUse(t *testing.T) {
	h, home := openOnMacRig(t, []terminput.Driver{fakeITerm2{}}, neverOpen(t))
	seedClaudeSession(t, home, "s1", t.TempDir(), "fix the build")
	release, err := acprun.Reservation(home)("s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rec := serveRig(h, http.MethodPost, "/v1/sessions/s1/open-on-mac")
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "in_use" {
		t.Fatalf("%d %s, want 409 in_use", rec.Code, rec.Body.String())
	}
}

// macOS refusing aimux control of iTerm2 answers not_authorized.
func TestOpenOnMac_NotAuthorized(t *testing.T) {
	h, home := openOnMacRig(t, []terminput.Driver{fakeITerm2{}}, func(context.Context, string, []string, string) error {
		return fmt.Errorf("iterm2: open tab: %w", iterm2.ErrNotAuthorized)
	})
	seedClaudeSession(t, home, "s1", t.TempDir(), "fix the build")
	rec := serveRig(h, http.MethodPost, "/v1/sessions/s1/open-on-mac")
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "not_authorized" {
		t.Fatalf("%d %s, want 403 not_authorized", rec.Code, rec.Body.String())
	}
}

// The default opener is iTerm2's, and it keeps no state under HOME.
func TestNew_DefaultOpener(t *testing.T) {
	home := t.TempDir()
	if New(config.Config{}, Options{Home: home}).opts.OpenTab == nil {
		t.Fatal("no default opener")
	}
	if a, b := windowOpener(), windowOpener(); a == b {
		t.Fatal("windowOpener shares one Opener between servers")
	}
	if ents, _ := os.ReadDir(filepath.Join(home, ".aimux")); len(ents) != 0 {
		t.Fatalf("~/.aimux has %d entries, want none", len(ents))
	}
}

func TestCutRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{strings.Repeat("é", 70), 60, strings.Repeat("é", 60)}, // runes, not bytes
		{"short", 60, "short"},
		{strings.Repeat("x", 60), 60, strings.Repeat("x", 60)}, // exactly n stays
		{"", 60, ""},
	} {
		if got := cutRunes(c.in, c.n); got != c.want {
			t.Errorf("cutRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
