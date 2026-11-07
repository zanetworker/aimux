package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// listBackend is a fake backend with tools to list.
type listBackend struct {
	fakeBackend
	tools   []json.RawMessage
	listErr error
}

func (b *listBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	return b.tools, b.listErr
}

func twoTools() []json.RawMessage {
	return []json.RawMessage{json.RawMessage(`{"name":"a"}`), json.RawMessage(`{"name":"b"}`)}
}

func TestPool_LeaseStartsThroughThePoolWithoutCountingACall(t *testing.T) {
	var started []*listBackend
	start := func(context.Context, string) (Backend, error) {
		b := &listBackend{tools: twoTools()}
		started = append(started, b)
		return b, nil
	}
	p := NewPool(start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	defer p.Close()

	b, err := p.Lease(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	if tools, err := b.ListToolsRaw(context.Background()); err != nil || len(tools) != 2 {
		t.Fatalf("list through lease: %v, %d tools", err, len(tools))
	}
	_ = b.Close()
	_ = b.Close() // a second Close must not release twice
	s := statusOf(t, p, "slack")
	if s.State != StateRunning || s.Calls != 0 {
		t.Fatalf("after lease: %+v, want running (shared backend kept) and no call counted", s)
	}
	if started[0].closed.Load() {
		t.Fatal("closing a lease of a shared server closed its backend")
	}

	b, err = p.Lease(context.Background(), "browser")
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if s := statusOf(t, p, "browser"); s.Instances != 0 {
		t.Fatalf("after the lease closed: %+v, want its backend no longer counted", s)
	}
	waitClosed(t, &started[1].fakeBackend, "a per-session server started for a lease")
}

// waitClosed waits for a backend closed in the background.
func waitClosed(t *testing.T, b *fakeBackend, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !b.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("%s is still open", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPool_LeaseErrors(t *testing.T) {
	fs := newFakeStarter()
	fs.fail["gh"] = errors.New("exec: not found")
	p := NewPool(fs.start, PoolOptions{})
	if _, err := p.Lease(context.Background(), "gh"); err == nil {
		t.Fatal("lease of a server that cannot start succeeded")
	}
	if s := statusOf(t, p, "gh"); s.State != StateFailed || s.Reason != ReasonStartFailed || s.Failures != 0 {
		t.Fatalf("after failed lease: %+v, want failed, start_failed, no call failure counted", s)
	}

	broken := &listBackend{listErr: errors.New("broken pipe")}
	p2 := NewPool(func(context.Context, string) (Backend, error) { return broken, nil }, PoolOptions{})
	defer p2.Close()
	b, err := p2.Lease(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ListToolsRaw(context.Background()); err == nil {
		t.Fatal("list error not returned")
	}
	_ = b.Close()
	if s := statusOf(t, p2, "s"); s.State != StateFailed || s.Reason != ReasonTransportError {
		t.Fatalf("after a failed listing: %+v, want failed with transport_error", s)
	}

	p.Close()
	if _, err := p.Lease(context.Background(), "x"); err == nil {
		t.Fatal("lease on a closed pool succeeded")
	}
}

type statusDaemon struct {
	base string
	pool *Pool
	mon  *Monitor
	mcp  string
}

func startStatusDaemon(t *testing.T, start Starter, servers []string) statusDaemon {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(start, PoolOptions{})
	cat := testCatalog()
	mon := &Monitor{
		Pool: pool, Servers: servers, PID: 4321, StartedAt: time.Unix(1_700_000_000, 0).UTC(),
		Port:  ln.Addr().(*net.TCPAddr).Port,
		Tools: func(s string) int { return len(cat.Servers[s].Tools) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServers(cat, pool), pool, DaemonOptions{Status: mon}) }()
	t.Cleanup(func() { cancel(); <-done })
	base := "http://" + ln.Addr().String()
	return statusDaemon{base: base, pool: pool, mon: mon, mcp: base + "/mcp/slack"}
}

// probeHdr is the header a probe request must carry.
var probeHdr = []string{"X-Aimux-Probe", "1"}

// get sends a request with an optional Host and header name/value pairs.
func get(t *testing.T, method, url, host string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if host != "" {
		req.Host = host
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestServe_StatusJSON(t *testing.T) {
	fs := newFakeStarter()
	fs.fail["bad"] = &tailErr{msg: "exec: not found", tail: []string{"why it failed"}}
	d := startStatusDaemon(t, fs.start, []string{"slack", "bad", "idle"})
	d.pool.Call(context.Background(), "slack", "", req("post_message"))
	d.pool.Call(context.Background(), "bad", "", req("x"))

	code, body := get(t, "GET", d.base+"/status", "")
	if code != http.StatusOK {
		t.Fatalf("GET /status: %d %s", code, body)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"running", "pid", "startedAt", "port", "servers"} {
		if _, ok := top[k]; !ok {
			t.Fatalf("/status lacks %q: %s", k, body)
		}
	}
	var rep StatusReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Running || rep.PID != 4321 || rep.Port != d.mon.Port || len(rep.Servers) != 3 {
		t.Fatalf("report %+v", rep)
	}
	byName := map[string]ServerReport{}
	for _, s := range rep.Servers {
		byName[s.Server] = s
	}
	if s := byName["slack"]; s.State != StateRunning || s.Tools != 1 || s.Calls != 1 {
		t.Fatalf("slack %+v, want running, 1 tool, 1 call", s)
	}
	if s := byName["bad"]; s.State != StateFailed || s.LastError != "exec: not found" || s.Reason != ReasonStartFailed ||
		len(s.StderrTail) != 1 {
		t.Fatalf("bad %+v, want failed with its error, reason and stderr tail", s)
	}
	if s := byName["idle"]; s.State != StateNotStarted {
		t.Fatalf("idle %+v, want not_started", s)
	}
	var servers []map[string]json.RawMessage
	_ = json.Unmarshal(top["servers"], &servers)
	for _, k := range []string{"server", "state", "instances", "tools", "calls", "failures", "lastCall", "lastError", "reason", "stderrTail"} {
		found := false
		for _, s := range servers {
			if _, ok := s[k]; ok {
				found = true
			}
		}
		if !found {
			t.Fatalf("no server entry has lower-camel %q: %s", k, top["servers"])
		}
	}
}

func TestServe_StatusIsLocalOnlyAndMethodChecked(t *testing.T) {
	d := startStatusDaemon(t, newFakeStarter().start, []string{"slack"})
	for _, tc := range []struct{ method, path, host string }{
		{"GET", "/status", "evil.example"},
		{"POST", "/status/probe", "evil.example:7381"},
	} {
		if code, _ := get(t, tc.method, d.base+tc.path, tc.host, probeHdr...); code != http.StatusForbidden {
			t.Fatalf("%s %s with Host %s: %d, want 403", tc.method, tc.path, tc.host, code)
		}
	}
	if code, _ := get(t, "GET", d.base+"/status/probe", "", probeHdr...); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /status/probe: %d, want 405 (probing starts servers, so POST only)", code)
	}
	if code, _ := get(t, "POST", d.base+"/status", ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /status: %d, want 405", code)
	}
	if code, _ := get(t, "POST", d.base+"/status/probe?timeout=nonsense", "", probeHdr...); code != http.StatusBadRequest {
		t.Fatalf("bad timeout: %d, want 400", code)
	}
}

func TestServe_StatusProbeStartsEachServerAndListsItsTools(t *testing.T) {
	start := func(ctx context.Context, server string) (Backend, error) {
		switch server {
		case "bad":
			return nil, errors.New("exec: not found")
		case "hang":
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &listBackend{tools: twoTools()}, nil
	}
	d := startStatusDaemon(t, start, []string{"slack", "bad", "hang"})
	began := time.Now()
	code, body := get(t, "POST", d.base+"/status/probe?timeout=300ms", "", probeHdr...)
	if code != http.StatusOK {
		t.Fatalf("probe: %d %s", code, body)
	}
	if time.Since(began) > 5*time.Second {
		t.Fatal("probe ignored its timeout")
	}
	var rep StatusReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	byName := map[string]ServerReport{}
	for _, s := range rep.Servers {
		byName[s.Server] = s
	}
	if s := byName["slack"]; s.State != StateRunning || s.Tools != 2 || s.ProbeError != "" {
		t.Fatalf("slack after probe: %+v, want running with the 2 listed tools", s)
	}
	if s := byName["bad"]; s.State != StateFailed || s.ProbeError == "" {
		t.Fatalf("bad after probe: %+v, want failed with a probe error", s)
	}
	if s := byName["hang"]; !strings.Contains(s.ProbeError, "deadline") {
		t.Fatalf("hang after probe: %+v, want a probe error from the timeout", s)
	}
}

// A server's env values never appear in /status or the probe, even when
// the server prints them.
func TestStatus_NeverShowsEnvValues(t *testing.T) {
	const secret = "abc123"
	cfgs := map[string]ServerConfig{"dies": {Command: os.Args[0], Env: map[string]string{
		testDieEnv: "1", testEchoEnv: secret, "SECRET_TOKEN": secret,
	}}}
	d := startStatusDaemon(t, StdioStarter(cfgs), []string{"dies"})
	_, probe := get(t, "POST", d.base+"/status/probe?timeout=15s", "", probeHdr...)
	_, status := get(t, "GET", d.base+"/status", "")
	if !strings.Contains(status, "token is [redacted]") {
		t.Fatalf("/status lacks the scrubbed stderr line, so the test proves nothing:\n%s", status)
	}
	for name, out := range map[string]string{"probe": probe, "status": status} {
		if strings.Contains(out, secret) {
			t.Fatalf("%s output contains the env value:\n%s", name, out)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "never"},
		{now, "just now"},
		{now.Add(-30 * time.Second), "30s ago"},
		{now.Add(-90 * time.Second), "1m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-50 * time.Hour), "2d ago"},
		{now.Add(time.Minute), "just now"}, // clock skew never shows a negative age
	} {
		if got := Ago(now, tc.t); got != tc.want {
			t.Errorf("Ago(%v) = %q, want %q", now.Sub(tc.t), got, tc.want)
		}
	}
}

// A web page can POST a form to the proxy without an Origin header, so a
// probe, which starts every server, needs a header a form cannot set.
func TestServe_StatusProbeNeedsItsHeader(t *testing.T) {
	fs := newFakeStarter()
	d := startStatusDaemon(t, fs.start, []string{"slack"})

	if code, _ := get(t, "POST", d.base+"/status/probe", ""); code != http.StatusForbidden {
		t.Fatalf("probe without X-Aimux-Probe: %d, want 403", code)
	}
	if code, _ := get(t, "POST", d.base+"/status/probe", "", "Content-Type", "application/x-www-form-urlencoded"); code != http.StatusForbidden {
		t.Fatalf("form-shaped probe: %d, want 403", code)
	}
	if n := fs.count("slack"); n != 0 {
		t.Fatalf("a refused probe started slack %d times", n)
	}
	if code, _ := get(t, "POST", d.base+"/status/probe", "", "X-Aimux-Probe", "1", "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site probe with the header: %d, want 403", code)
	}
	if n := fs.count("slack"); n != 0 {
		t.Fatalf("a cross-site probe started slack %d times", n)
	}
	if code, body := get(t, "POST", d.base+"/status/probe", "", probeHdr...); code != http.StatusOK || fs.count("slack") != 1 {
		t.Fatalf("probe with the header: %d %s, started %d times; want 200 and one start", code, body, fs.count("slack"))
	}
}

// A script that sets the header first sends a CORS preflight; the proxy
// must never allow it.
func TestServe_StatusProbePreflightIsNotAllowed(t *testing.T) {
	d := startStatusDaemon(t, newFakeStarter().start, []string{"slack"})
	for _, origin := range []string{"https://evil.example", "http://localhost:5173"} {
		req, _ := http.NewRequest("OPTIONS", d.base+"/status/probe", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "x-aimux-probe")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods"} {
			if v := resp.Header.Get(h); v != "" {
				t.Fatalf("preflight from %s answered %s: %q", origin, h, v)
			}
		}
	}
}

// Claude Code's MCP HTTP client sends no Origin; a server's endpoint must
// keep taking it.
func TestServe_MCPAcceptsPostWithoutOrigin(t *testing.T) {
	d := startStatusDaemon(t, newFakeStarter().start, []string{"slack"})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	req, _ := http.NewRequest("POST", d.mcp, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /mcp/slack without Origin: %d, want 200", resp.StatusCode)
	}
}

// Two probes at once each get their own per-session backend, and each
// lease closes only its own.
func TestPool_ConcurrentLeasesOfAPerSessionServerAreSeparate(t *testing.T) {
	var started []*listBackend
	var mu sync.Mutex
	start := func(context.Context, string) (Backend, error) {
		mu.Lock()
		defer mu.Unlock()
		b := &listBackend{}
		started = append(started, b)
		return b, nil
	}
	p := NewPool(start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	defer p.Close()
	a, err := p.Lease(context.Background(), "browser")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Lease(context.Background(), "browser")
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 2 {
		t.Fatalf("two concurrent leases started %d backends, want 2", len(started))
	}
	_ = a.Close()
	waitClosed(t, &started[0].fakeBackend, "lease a's backend")
	if s := statusOf(t, p, "browser"); started[1].closed.Load() || s.Instances != 1 {
		t.Fatalf("closing lease a: %+v, b closed=%v; want lease b's backend still running", s, started[1].closed.Load())
	}
	_ = b.Close()
	waitClosed(t, &started[1].fakeBackend, "lease b's backend")
}

// A probe that gives up while a per-session server is still starting must
// not leave that server running once its start finishes.
func TestPool_LeaseThatTimesOutClosesALateStart(t *testing.T) {
	gate := make(chan struct{})
	late := &listBackend{}
	start := func(context.Context, string) (Backend, error) {
		<-gate
		return late, nil
	}
	p := NewPool(start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Lease(ctx, "browser"); err == nil {
		t.Fatal("lease succeeded before the start finished")
	}
	close(gate)
	deadline := time.Now().Add(3 * time.Second)
	for !late.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("late start still running: %+v", statusOf(t, p, "browser"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s := statusOf(t, p, "browser"); s.Instances != 0 {
		t.Fatalf("after the late start: %+v, want no instance", s)
	}
}

// blockingLister is a backend whose tools/list ignores its context and
// blocks until released, like a transport whose write lock a stuck call holds.
type blockingLister struct {
	fakeBackend
	release chan struct{}
}

func (b *blockingLister) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	<-b.release
	return twoTools(), nil
}

func TestLease_ListToolsReturnsAtItsDeadline(t *testing.T) {
	stuck := &blockingLister{release: make(chan struct{})}
	defer close(stuck.release)
	p := NewPool(func(context.Context, string) (Backend, error) { return stuck, nil }, PoolOptions{})
	defer p.Close()
	b, err := p.Lease(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err = b.ListToolsRaw(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the deadline error", err)
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("listing returned after %v, want about 50ms", d)
	}
	if s := statusOf(t, p, "s"); s.State != StateRunning {
		t.Fatalf("a probe's own timeout marked the server: %+v", s)
	}
}

func TestMonitor_ProbeFinishesWithinItsTimeoutWhenAListingHangs(t *testing.T) {
	stuck := &blockingLister{release: make(chan struct{})}
	defer close(stuck.release)
	p := NewPool(func(context.Context, string) (Backend, error) { return stuck, nil }, PoolOptions{})
	defer p.Close()
	m := &Monitor{Pool: p, Servers: []string{"s"}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	rep := m.Probe(ctx)
	if d := time.Since(began); d > time.Second {
		t.Fatalf("probe took %v, want about its 100ms timeout", d)
	}
	if rep.Servers[0].ProbeError == "" {
		t.Fatalf("hung listing reported no probe error: %+v", rep.Servers[0])
	}
}

// Once the probe's time is up, servers still waiting their turn never start.
func TestMonitor_ProbeStartsNothingAfterItsTimeout(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	var mu sync.Mutex
	started := map[string]bool{}
	start := func(ctx context.Context, server string) (Backend, error) {
		mu.Lock()
		started[server] = true
		mu.Unlock()
		<-gate
		return &listBackend{}, nil
	}
	p := NewPool(start, PoolOptions{})
	defer p.Close()
	servers := []string{"s1", "s2", "s3", "s4", "s5", "s6"}
	m := &Monitor{Pool: p, Servers: servers}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rep := m.Probe(ctx)
	time.Sleep(200 * time.Millisecond) // time for a wrongly scheduled start to show
	mu.Lock()
	n := len(started)
	mu.Unlock()
	if n != refreshParallelism {
		t.Fatalf("%d servers started, want only the first batch of %d", n, refreshParallelism)
	}
	for _, s := range rep.Servers {
		if s.ProbeError == "" {
			t.Fatalf("%s has no probe error after the timeout: %+v", s.Server, s)
		}
	}
}

func TestLease_ListRPCErrorKeepsBackend(t *testing.T) {
	b := &listBackend{listErr: &RequestError{Err: errors.New("method not found")}}
	p := NewPool(func(context.Context, string) (Backend, error) { return b, nil }, PoolOptions{})
	defer p.Close()
	rep := (&Monitor{Pool: p, Servers: []string{"s"}}).Probe(context.Background())
	if s := rep.Servers[0]; s.State != StateRunning || s.ProbeError == "" || s.Reason != ReasonNone {
		t.Fatalf("after an RPC error to tools/list: %+v, want running with a probe error", s)
	}
	if b.closed.Load() {
		t.Fatal("an RPC error to tools/list closed a healthy backend")
	}
}

// slowCloser is a backend whose Close blocks until released, like a server
// that ignores SIGTERM.
type slowCloser struct {
	listBackend
	release chan struct{}
	closing chan struct{}
	once    sync.Once
}

func newSlowCloser(listErr error) *slowCloser {
	return &slowCloser{listBackend: listBackend{tools: twoTools(), listErr: listErr},
		release: make(chan struct{}), closing: make(chan struct{})}
}

func (b *slowCloser) Close() error {
	b.once.Do(func() { close(b.closing) })
	<-b.release
	return b.listBackend.Close()
}

func closeStarted(t *testing.T, b *slowCloser) {
	t.Helper()
	select {
	case <-b.closing:
	case <-time.After(3 * time.Second):
		t.Fatal("the backend's Close never ran")
	}
}

func TestMonitor_ProbeDoesNotWaitForAPerSessionBackendToStop(t *testing.T) {
	b := newSlowCloser(nil)
	defer close(b.release)
	p := NewPool(func(context.Context, string) (Backend, error) { return b, nil },
		PoolOptions{PerSession: map[string]bool{"browser": true}})
	m := &Monitor{Pool: p, Servers: []string{"browser"}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	rep := m.Probe(ctx)
	if d := time.Since(began); d > time.Second {
		t.Fatalf("probe took %v waiting for a backend to stop, want under its 200ms timeout", d)
	}
	if s := rep.Servers[0]; s.Tools != 2 || s.Instances != 0 || s.ProbeError != "" {
		t.Fatalf("probe report %+v, want 2 tools and the probe's backend no longer counted", s)
	}
	closeStarted(t, b)
}

func TestMonitor_ProbeDoesNotWaitToCloseABrokenBackend(t *testing.T) {
	b := newSlowCloser(errors.New("broken pipe"))
	defer close(b.release)
	p := NewPool(func(context.Context, string) (Backend, error) { return b, nil }, PoolOptions{})
	m := &Monitor{Pool: p, Servers: []string{"s"}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	rep := m.Probe(ctx)
	if d := time.Since(began); d > time.Second {
		t.Fatalf("probe took %v closing a broken backend, want under its 200ms timeout", d)
	}
	if s := rep.Servers[0]; s.State != StateFailed || s.Reason != ReasonTransportError {
		t.Fatalf("probe report %+v, want failed with transport_error", s)
	}
	closeStarted(t, b)
}

func TestServe_StatusProbeOnlyNamedServers(t *testing.T) {
	var mu sync.Mutex
	var started []string
	start := func(ctx context.Context, server string) (Backend, error) {
		mu.Lock()
		started = append(started, server)
		mu.Unlock()
		return &listBackend{tools: twoTools()}, nil
	}
	d := startStatusDaemon(t, start, []string{"slack", "github", "other"})
	code, body := get(t, "POST", d.base+"/status/probe?timeout=2s&server=slack&server=nosuch", "", probeHdr...)
	if code != http.StatusOK {
		t.Fatalf("probe: %d %s", code, body)
	}
	var rep StatusReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	byName := map[string]ServerReport{}
	for _, s := range rep.Servers {
		byName[s.Server] = s
	}
	if len(rep.Servers) != 2 || byName["slack"].Tools != 2 || byName["slack"].ProbeError != "" {
		t.Fatalf("report = %+v, want slack probed and nosuch reported", rep.Servers)
	}
	if !strings.Contains(byName["nosuch"].ProbeError, "not configured") {
		t.Fatalf("nosuch = %+v", byName["nosuch"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(started) != 1 || started[0] != "slack" {
		t.Fatalf("started %v, want only slack", started)
	}
}

func TestMonitor_ReportsWhatTheDaemonRegistered(t *testing.T) {
	p := NewPool(newFakeStarter().start, PoolOptions{})
	defer p.Close()
	m := &Monitor{Pool: p, Servers: []string{"a", "b"}, Registered: map[string]string{"a": "fp-a"}}
	rep := m.Report()
	if rep.Servers[0].Registered != "fp-a" || rep.Servers[1].Registered != "" {
		t.Fatalf("report = %+v", rep.Servers)
	}
}

func TestMonitor_ReportsItsVersion(t *testing.T) {
	p := NewPool(func(context.Context, string) (Backend, error) { return nil, errors.New("unused") }, PoolOptions{})
	rep := (&Monitor{Pool: p, Version: "1.2.3"}).Report()
	if rep.Version != "1.2.3" {
		t.Fatalf("version %q, want 1.2.3", rep.Version)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"version":"1.2.3"`) {
		t.Fatalf("/status JSON lacks the version: %s", b)
	}
}

// /status says which servers have an endpoint: a configured server without
// a catalog entry has none, so install must not point Claude at it.
func TestServe_StatusReportsWhichServersHaveAnEndpoint(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(newFakeStarter().start, PoolOptions{})
	cat := &Catalog{}
	cat.Set("a", toolJSON("a_tool"), time.Now())
	mon := &Monitor{Pool: pool, Servers: []string{"a", "b"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServers(cat, pool), pool, DaemonOptions{Status: mon}) }()
	defer func() { cancel(); <-done }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var rep StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range rep.Servers {
		got[s.Server] = s.Endpoint
	}
	if !got["a"] || got["b"] || len(got) != 2 {
		t.Fatalf("endpoints %v, want a only", got)
	}
}
