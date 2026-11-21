package mcpproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/errcode"
	"github.com/zanetworker/aimux/internal/secretref"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// Managed HTTP test server modes, set through the server's literal env.
const (
	testHTTPAddrEnv   = "AIMUX_MCPPROXY_TEST_HTTP_ADDR"   // serve MCP over HTTP here
	testHTTPExitEnv   = "AIMUX_MCPPROXY_TEST_HTTP_EXIT"   // exit 1 after this long serving
	testHTTPEnvFile   = "AIMUX_MCPPROXY_TEST_HTTP_ENV"    // write the environment here at start
	testHTTPForkFile  = "AIMUX_MCPPROXY_TEST_HTTP_FORK"   // start a sleeping grandchild, its PID here
	testHTTPDenyToken = "AIMUX_MCPPROXY_TEST_HTTP_DENY"   //nolint:gosec // G101: an env var name, answer 401 echoing its value
	testHTTPSayEnv    = "AIMUX_MCPPROXY_TEST_HTTP_STDERR" // print this on stderr at start
)

func runHTTPTestServer(addr string) {
	if f := os.Getenv(testHTTPEnvFile); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(os.Environ(), "\n")), 0o600) //nolint:gosec // G703: a test file path the test passes
	}
	if s := os.Getenv(testHTTPSayEnv); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	if f := os.Getenv(testHTTPForkFile); f != "" {
		c := exec.Command("/bin/sleep", "300")
		if c.Start() == nil {
			_ = os.WriteFile(f, []byte(strconv.Itoa(c.Process.Pid)), 0o600) //nolint:gosec // G703: a test file path the test passes
		}
	}
	if d, err := time.ParseDuration(os.Getenv(testHTTPExitEnv)); err == nil {
		go func() { time.Sleep(d); os.Exit(1) }()
	}
	srv := server.NewMCPServer("managed-test", "1.0.0")
	srv.AddTool(mcp.NewTool("ping", mcp.WithDescription("Answer pong")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("pong"), nil
	})
	var h http.Handler = server.NewStreamableHTTPServer(srv)
	if v := os.Getenv(testHTTPDenyToken); v != "" {
		h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad token "+os.Getenv(v), http.StatusUnauthorized)
		})
	}
	_ = http.ListenAndServe(addr, h) //nolint:gosec // test server on loopback
	os.Exit(1)
}

// freeAddr is a loopback address nothing listens on now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// managed is a managed HTTP server definition running the test binary on
// addr with extra literal env.
func managed(t *testing.T, addr string, env map[string]string) toolreg.Server {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := map[string]string{testHTTPAddrEnv: addr}
	for k, v := range env {
		e[k] = v
	}
	return toolreg.Server{Command: []string{exe}, URL: "http://" + addr + "/mcp", Env: e, Ready: &toolreg.ReadyProbe{Timeout: 10 * time.Second}}
}

// supClock is a settable Now and an After that records each delay and
// fires at once.
type supClock struct {
	mu     sync.Mutex
	now    time.Time
	step   time.Duration
	delays []time.Duration
}

func (c *supClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

func (c *supClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.delays = append(c.delays, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func (c *supClock) Delays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.delays)
}

func newTestSupervisor(t *testing.T, clock *supClock) *Supervisor {
	t.Helper()
	opts := SupervisorOptions{
		Resolver: secretref.NewResolver(secretref.Options{}),
		LogDir:   t.TempDir(),
		Base:     map[string]string{"PATH": "/usr/bin:/bin", "HOME": t.TempDir(), "TMPDIR": os.TempDir()},
	}
	if clock != nil {
		opts.Now, opts.After = clock.Now, clock.After
	}
	s := NewSupervisor(opts)
	t.Cleanup(s.StopAll)
	return s
}

func waitManaged(t *testing.T, s *Supervisor, name, want string) ManagedState {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.Status()[name]; st.State == want {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: state %+v, want %s", name, s.Status()[name], want)
	return ManagedState{}
}

func TestSupervisor_StartsAndWaitsForReady(t *testing.T) {
	s := newTestSupervisor(t, nil)
	addr := freeAddr(t)
	url, err := s.Ensure(context.Background(), "jira", managed(t, addr, nil))
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://"+addr+"/mcp" {
		t.Fatalf("url = %q", url)
	}
	st := s.Status()["jira"]
	if st.State != ManagedReady || st.PID == 0 || st.LogPath == "" {
		t.Fatalf("status = %+v, want ready with a PID and a log", st)
	}
	if again, err := s.Ensure(context.Background(), "jira", managed(t, addr, nil)); err != nil || again != url || s.Status()["jira"].PID != st.PID {
		t.Fatalf("a second Ensure must reuse the running server: %q %v", again, err)
	}
	s.Stop("jira")
	if st := s.Status()["jira"]; st.State != ManagedStopped {
		t.Fatalf("after Stop: %+v", st)
	}
}

// Review Focus 1: a hand-started server holds the port. The start fails
// with the exact message, the other process is untouched, and status says
// why.
func TestSupervisor_PortInUseFails(t *testing.T) {
	s := newTestSupervisor(t, nil)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	addr := l.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	_, err = s.Ensure(context.Background(), "JirAIa", managed(t, addr, nil))
	want := "port " + port + " is in use (another JirAIa started by hand?); stop it, or set adopt_existing: true"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if e, ok := errcode.Of(err); !ok || e.Code != errcode.ServerPortInUse {
		t.Fatalf("err code = %+v, want %s", e, errcode.ServerPortInUse)
	}
	if st := s.Status()["JirAIa"]; st.State != ManagedPortInUse || st.PID != 0 || st.LastError != want {
		t.Fatalf("status = %+v", st)
	}
	c, err := net.Dial("tcp", addr) // the hand-started listener still answers
	if err != nil {
		t.Fatalf("the other process's port was disturbed: %v", err)
	}
	_ = c.Close()
}

// adopt_existing takes over a server on the port only when it answers MCP
// initialize there; a non-MCP listener is still port_in_use.
func TestSupervisor_AdoptExistingNeedsInitialize(t *testing.T) {
	s := newTestSupervisor(t, nil)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { // accepts and closes: not MCP
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	def := managed(t, l.Addr().String(), nil)
	def.AdoptExisting = true
	if _, err := s.Ensure(context.Background(), "raw", def); err == nil || s.Status()["raw"].State != ManagedPortInUse {
		t.Fatalf("a listener that does not answer initialize must not be adopted: %v %+v", err, s.Status()["raw"])
	}
	_ = l.Close()

	// A real MCP server started by hand.
	hand := newTestSupervisor(t, nil)
	addr := freeAddr(t)
	if _, err := hand.Ensure(context.Background(), "mcp", managed(t, addr, nil)); err != nil {
		t.Fatal(err)
	}
	def = managed(t, addr, nil)
	def.AdoptExisting = true
	url, err := s.Ensure(context.Background(), "mcp", def)
	if err != nil || url != def.URL {
		t.Fatalf("adopt: %q %v", url, err)
	}
	if st := s.Status()["mcp"]; st.State != ManagedAdopted || st.PID != 0 {
		t.Fatalf("status = %+v, want adopted with no PID of ours", st)
	}
	s.Stop("mcp") // never stops what it did not start
	if st := hand.Status()["mcp"]; st.State != ManagedReady {
		t.Fatalf("the adopted server was stopped: %+v", st)
	}
}

func TestSupervisor_RestartBackoff(t *testing.T) {
	clock := &supClock{now: time.Unix(0, 0), step: time.Second} // short runs: the backoff grows
	s := newTestSupervisor(t, clock)
	addr := freeAddr(t)
	def := managed(t, addr, map[string]string{testHTTPExitEnv: "700ms"})
	if _, err := s.Ensure(context.Background(), "flaky", def); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for len(clock.Delays()) < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	s.Stop("flaky")
	got := clock.Delays()
	if len(got) < 3 || got[0] != time.Second || got[1] != 2*time.Second || got[2] != 4*time.Second {
		t.Fatalf("restart delays = %v, want 1s, 2s, 4s", got)
	}
	if st := s.Status()["flaky"]; st.Restarts < 3 {
		t.Fatalf("restarts = %d", st.Restarts)
	}
}

func TestBackoffDelay_CapsAt60s(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 6: 32 * time.Second, 7: time.Minute, 30: time.Minute, 100: time.Minute} {
		if got := backoffDelay(n); got != want {
			t.Errorf("backoffDelay(%d) = %v, want %v", n, got, want)
		}
	}
}

// Review Focus 5: a server that crashes at every start is crash_loop after
// 5 crashes in 5 minutes; it is not restarted again, other servers keep
// running, and status names the log path, never the log's lines.
func TestSupervisor_CrashLoopAfter5In5Min(t *testing.T) {
	clock := &supClock{now: time.Unix(0, 0), step: 10 * time.Second}
	s := newTestSupervisor(t, clock)
	other := freeAddr(t)
	if _, err := s.Ensure(context.Background(), "jira", managed(t, other, nil)); err != nil {
		t.Fatal(err)
	}
	def := managed(t, freeAddr(t), map[string]string{testHTTPExitEnv: "700ms", testHTTPSayEnv: "token refresh failed: sentinel-log-line"})
	if _, err := s.Ensure(context.Background(), "telemetry", def); err != nil {
		t.Fatal(err)
	}
	st := waitManaged(t, s, "telemetry", ManagedCrashLoop)
	if len(clock.Delays()) != 4 {
		t.Fatalf("restarts scheduled = %v, want 4 (the fifth crash stops them)", clock.Delays())
	}
	if strings.Contains(st.LastError, "sentinel-log-line") || !strings.Contains(st.LastError, st.LogPath) {
		t.Fatalf("last error %q: want the log path %s, not its lines", st.LastError, st.LogPath)
	}
	log, _ := os.ReadFile(st.LogPath) //nolint:gosec // test log
	if !strings.Contains(string(log), "sentinel-log-line") {
		t.Fatalf("the log %s does not hold the server's stderr", st.LogPath)
	}
	time.Sleep(500 * time.Millisecond)
	if len(clock.Delays()) != 4 || s.Status()["telemetry"].State != ManagedCrashLoop {
		t.Fatal("a crash_loop server must not restart")
	}
	_, err := s.Ensure(context.Background(), "telemetry", def)
	if e, ok := errcode.Of(err); !ok || e.Code != errcode.ServerCrashLoop || !strings.Contains(err.Error(), "aimux servers restart telemetry") {
		t.Fatalf("Ensure on crash_loop: %v", err)
	}
	if s.Status()["jira"].State != ManagedReady {
		t.Fatalf("other server: %+v", s.Status()["jira"])
	}
}

func TestSupervisor_KeepRunningStartsAtLaunch(t *testing.T) {
	s := newTestSupervisor(t, nil)
	keep := managed(t, freeAddr(t), nil)
	keep.KeepRunning = true
	lazy := managed(t, freeAddr(t), nil)
	s.StartKeepRunning(context.Background(), map[string]toolreg.Server{
		"drawio": keep, "jira": lazy, "remote": {URL: "http://127.0.0.1:1/mcp", KeepRunning: true},
	})
	waitManaged(t, s, "drawio", ManagedReady)
	if st, ok := s.Status()["jira"]; ok && st.State != ManagedStopped {
		t.Fatalf("a server without keep_running must not start: %+v", st)
	}
	if _, ok := s.Status()["remote"]; ok {
		t.Fatal("a remote server is not managed")
	}
}

func TestSupervisor_IdleStopCountsInFlight(t *testing.T) {
	s := newTestSupervisor(t, nil)
	def := managed(t, freeAddr(t), nil)
	if _, err := s.Ensure(context.Background(), "jira", def); err != nil {
		t.Fatal(err)
	}
	release := s.Acquire("jira")
	if s.StopIdle("jira") || s.Status()["jira"].State != ManagedReady {
		t.Fatal("a server with a call in flight must not be stopped as idle")
	}
	release()
	release() // twice is harmless
	if !s.StopIdle("jira") || s.Status()["jira"].State != ManagedStopped {
		t.Fatalf("idle server not stopped: %+v", s.Status()["jira"])
	}
	keep := managed(t, freeAddr(t), nil)
	keep.KeepRunning = true
	if _, err := s.Ensure(context.Background(), "drawio", keep); err != nil {
		t.Fatal(err)
	}
	if s.StopIdle("drawio") {
		t.Fatal("keep_running is never stopped as idle")
	}
}

func TestSupervisor_StopKillsGroup(t *testing.T) {
	s := newTestSupervisor(t, nil)
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	if _, err := s.Ensure(context.Background(), "jira", managed(t, freeAddr(t), map[string]string{testHTTPForkFile: pidFile})); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(pidFile) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	s.Stop("jira")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d outlived Stop", pid)
}

func TestChildEnv_OnlyBaseLiteralAndResolved(t *testing.T) {
	got := ChildEnv(map[string]string{"PATH": "/bin", "HOME": "/h", "LANG": "C", "TMPDIR": "/t", "SSH_AUTH_SOCK": "/s"},
		map[string]string{"LOG": "debug"}, map[string]string{"TOKEN": "v"})
	slices.Sort(got)
	want := []string{"HOME=/h", "LANG=C", "LOG=debug", "PATH=/bin", "TMPDIR=/t", "TOKEN=v"}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildEnv = %v, want %v", got, want)
	}

	t.Setenv("AIMUX_SUPERVISOR_SENTINEL", "leak-me")
	s := newTestSupervisor(t, nil)
	envFile := filepath.Join(t.TempDir(), "env")
	if _, err := s.Ensure(context.Background(), "jira", managed(t, freeAddr(t), map[string]string{testHTTPEnvFile: envFile})); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(envFile) //nolint:gosec // test file
	if strings.Contains(string(env), "leak-me") || !strings.Contains(string(env), "PATH=/usr/bin:/bin") {
		t.Fatalf("child environment:\n%s", env)
	}
}

// A cmd secret without ttl runs at every start, and its value reaches the
// child's environment.
func TestSupervisor_SecretResolvedAtEachStart(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := filepath.Join(dir, "token.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho run >> "+count+"\necho tok-value-123\n"), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	s := newTestSupervisor(t, nil)
	envFile := filepath.Join(dir, "env")
	def := managed(t, freeAddr(t), map[string]string{testHTTPEnvFile: envFile})
	def.Secrets = secretref.Refs{Env: map[string]secretref.Ref{"API_TOKEN": {Cmd: &secretref.CmdRef{Argv: []string{script}}}}}
	for range 2 {
		if _, err := s.Ensure(context.Background(), "jira", def); err != nil {
			t.Fatal(err)
		}
		s.Stop("jira")
	}
	runs, _ := os.ReadFile(count) //nolint:gosec // test file
	if n := strings.Count(string(runs), "run"); n != 2 {
		t.Fatalf("secret command ran %d times, want once per start", n)
	}
	env, _ := os.ReadFile(envFile) //nolint:gosec // test file
	if !strings.Contains(string(env), "API_TOKEN=tok-value-123") {
		t.Fatalf("child environment lacks the resolved secret:\n%s", env)
	}
}

// A readiness failure that echoes a secret is scrubbed in status.
func TestSupervisor_LastErrorScrubbed(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "token.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho tok-secret-98765\n"), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	s := newTestSupervisor(t, nil)
	def := managed(t, freeAddr(t), map[string]string{testHTTPDenyToken: "API_TOKEN"})
	def.Ready.Timeout = time.Second
	def.Secrets = secretref.Refs{Env: map[string]secretref.Ref{"API_TOKEN": {Cmd: &secretref.CmdRef{Argv: []string{script}}}}}
	_, err := s.Ensure(context.Background(), "jira", def)
	if err == nil {
		t.Fatal("a server answering 401 is not ready")
	}
	st := s.Status()["jira"]
	if strings.Contains(err.Error(), "tok-secret-98765") || strings.Contains(st.LastError, "tok-secret-98765") || !strings.Contains(st.LastError, "401") {
		t.Fatalf("err %q / last error %q: want 401, never the secret", err, st.LastError)
	}
}

func TestRotatingLog_KeepsTwo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	w, err := openRotatingLog(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, err := fmt.Fprintf(w, "%02d %s\n", i, strings.Repeat("x", 40)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	for _, f := range []string{p, p + ".1"} {
		if fi, err := os.Stat(f); err != nil || fi.Size() > 100 {
			t.Fatalf("%s: %v (size must stay under the cap)", f, err)
		}
	}
	if _, err := os.Stat(p + ".2"); err == nil {
		t.Fatal("only two logs are kept")
	}
	cur, _ := os.ReadFile(p) //nolint:gosec // test log
	if !strings.Contains(string(cur), "09 ") {
		t.Fatal("the newest line must be in the current log")
	}
}

// The pool reaches a managed server through ManagedStarter: the first
// backend starts it, and it stops when the last backend closes (the pool
// reaped it idle), unless keep_running.
func TestManagedStarter_StopsWhenLastBackendCloses(t *testing.T) {
	s := newTestSupervisor(t, nil)
	keep := managed(t, freeAddr(t), nil)
	keep.KeepRunning = true
	start := ManagedStarter(s, map[string]toolreg.Server{"jira": managed(t, freeAddr(t), nil), "drawio": keep})
	ctx := context.Background()
	a, err := start(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	b, err := start(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListToolsRaw(ctx); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	if st := s.Status()["jira"]; st.State != ManagedReady {
		t.Fatalf("one backend still open: %+v", st)
	}
	_ = b.Close()
	if st := s.Status()["jira"]; st.State != ManagedStopped {
		t.Fatalf("last backend closed: %+v, want stopped", st)
	}
	d, err := start(ctx, "drawio")
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if st := s.Status()["drawio"]; st.State != ManagedReady {
		t.Fatalf("keep_running: %+v, want ready", st)
	}
	if _, err := start(ctx, "nope"); err == nil {
		t.Fatal("an unknown server must fail")
	}
}

// The supervisor records the identity each process was started under, so a
// listing can later be tied to the process that answered it (lifting the
// managed-server hold, Task 6).
func TestSupervisor_RecordsStartedIdentity(t *testing.T) {
	s := newTestSupervisor(t, nil)
	def := managed(t, freeAddr(t), nil)
	if _, err := s.Ensure(context.Background(), "jira", def); err != nil {
		t.Fatal(err)
	}
	want, err := def.Identity(toolreg.HashFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Status()["jira"].Identity; got != want {
		t.Fatalf("identity %q, want %q", got, want)
	}
	s.Stop("jira")
	if got := s.Status()["jira"].Identity; got != "" {
		t.Fatalf("a stopped server has no running identity: %q", got)
	}
}
