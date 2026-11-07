package mcpproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"golang.org/x/sys/unix"
)

const (
	testServerEnv  = "AIMUX_MCPPROXY_TEST_SERVER"
	testPIDFileEnv = "AIMUX_MCPPROXY_TEST_PIDFILE"
	testLingerEnv  = "AIMUX_MCPPROXY_TEST_LINGER"
	testStderrEnv  = "AIMUX_MCPPROXY_TEST_STDERR_FLOOD"
	testDeafEnv    = "AIMUX_MCPPROXY_TEST_DEAF"
	testDieEnv     = "AIMUX_MCPPROXY_TEST_DIE"
	testExitEnv    = "AIMUX_MCPPROXY_TEST_EXIT_AFTER"
	testEchoEnv    = "AIMUX_MCPPROXY_TEST_ECHO_VALUE"
	// testNoStderrEnv makes the test server close its stderr, then keep serving.
	testNoStderrEnv = "AIMUX_MCPPROXY_TEST_CLOSE_STDERR"
	// testListErrEnv makes a server whose tools/list answers a JSON-RPC
	// error that echoes testEchoEnv's value.
	testListErrEnv = "AIMUX_MCPPROXY_TEST_LIST_ERROR"
	// testRequireEnv names a variable the test server needs: without it, it
	// exits like a server missing its token.
	testRequireEnv = "AIMUX_MCPPROXY_TEST_REQUIRE"
	// testNoToolsEnv makes a server that serves no tools.
	testNoToolsEnv = "AIMUX_MCPPROXY_TEST_NO_TOOLS"
	// testListCodeEnv sets the JSON-RPC error code the list-error server
	// answers (default -32603).
	testListCodeEnv = "AIMUX_MCPPROXY_TEST_LIST_CODE"
	// testListMalformedEnv makes the list-error server answer tools/list
	// with a result that is not a tool list.
	testListMalformedEnv = "AIMUX_MCPPROXY_TEST_LIST_MALFORMED"
)

// TestMain lets the test binary act as a stdio MCP server for the tests
// below, so they exercise real processes and the real protocol.
func TestMain(m *testing.M) {
	if os.Getenv(testDieEnv) == "1" {
		// A server that cannot start: it explains why on stderr and exits.
		if os.Getenv(testStderrEnv) == "1" {
			_, _ = os.Stderr.Write([]byte(strings.Repeat("noise before the reason\n", 20000)))
		}
		for i := 1; i <= 8; i++ {
			fmt.Fprintf(os.Stderr, "die line %d\n", i)
		}
		if v := os.Getenv(testEchoEnv); v != "" {
			fmt.Fprintf(os.Stderr, "token is %s\n", v)
		}
		os.Exit(1)
	}
	if os.Getenv(testDeafEnv) == "1" {
		runDeafServer()
		return
	}
	if os.Getenv(testListErrEnv) == "1" {
		runListErrorServer(os.Getenv(testEchoEnv))
		return
	}
	if os.Getenv(testNoToolsEnv) == "1" {
		_ = server.ServeStdio(server.NewMCPServer("no-tools", "1.0.0"))
		return
	}
	if os.Getenv(testServerEnv) == "1" {
		if v := os.Getenv(testRequireEnv); v != "" && os.Getenv(v) == "" {
			fmt.Fprintf(os.Stderr, "%s is not set\n", v)
			os.Exit(1)
		}
		runTestServer()
		return
	}
	os.Exit(m.Run())
}

func runTestServer() {
	s := server.NewMCPServer("test-backend", "1.0.0")
	echo := mcp.NewToolWithRawSchema("echo", "Echo the text back",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"anyOf":[{"required":["text"]}]}`))
	s.AddTool(echo, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("echo: " + req.GetString("text", "")), nil
	})
	s.AddTool(mcp.NewTool("getenv", mcp.WithDescription("Return an environment variable")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(os.Getenv(req.GetString("name", ""))), nil
		})
	// progress sends three progress notifications with the token it was
	// given, then answers with that token.
	s.AddTool(mcp.NewTool("progress", mcp.WithDescription("Report progress three times")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var tok any
			if req.Params.Meta != nil {
				tok = req.Params.Meta.ProgressToken
			}
			srv := server.ServerFromContext(ctx)
			for i := 1; i <= 3; i++ {
				_ = srv.SendNotificationToClient(ctx, "notifications/progress",
					map[string]any{"progressToken": tok, "progress": i, "total": 3})
				time.Sleep(20 * time.Millisecond)
			}
			return mcp.NewToolResultText(fmt.Sprint(tok)), nil
		})
	s.AddTool(mcp.NewTool("crash", mcp.WithDescription("Exit the process")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			os.Exit(3)
			return nil, nil
		})
	if os.Getenv(testStderrEnv) == "1" {
		// Like a chatty server: far more log output than a pipe buffer holds.
		_, _ = os.Stderr.Write([]byte(strings.Repeat("log line from the server\n", 80000)))
	}
	if d, err := time.ParseDuration(os.Getenv(testExitEnv)); err == nil {
		go func() { time.Sleep(d); os.Exit(0) }() // dies on its own, unasked
	}
	if os.Getenv(testNoStderrEnv) == "1" {
		_ = os.Stderr.Close() // a healthy server that just stops logging
	}
	if f := os.Getenv(testPIDFileEnv); f != "" {
		_ = os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())), 0o600) // #nosec G703 -- test helper, path set by the test
	}
	_ = server.ServeStdio(s)
	if os.Getenv(testLingerEnv) == "1" {
		// Act like a server that ignores shutdown: survive stdin EOF and SIGTERM.
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
	}
}

// runDeafServer completes the MCP handshake, then stops reading stdin, like
// a server stuck in a busy loop: writes to it block once the pipe is full.
func runDeafServer() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(sc.Bytes(), &msg)
		switch msg.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"deaf","version":"1"}}}`+"\n", msg.ID)
		case "notifications/initialized":
			time.Sleep(time.Minute)
			return
		}
	}
}

// runListErrorServer completes the handshake and answers tools/list with a
// JSON-RPC error whose message contains value.
func runListErrorServer(value string) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(sc.Bytes(), &msg)
		switch msg.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"listerr","version":"1"}}}`+"\n", msg.ID)
		case "tools/list":
			code := os.Getenv(testListCodeEnv)
			if code == "" {
				code = "-32603"
			}
			if os.Getenv(testListMalformedEnv) == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":"not a list"}}`+"\n", msg.ID)
				continue
			}
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%s,"message":"bad key %s"}}`+"\n", msg.ID, code, value)
		case "tools/call":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"called"}]}}`+"\n", msg.ID)
		}
	}
}

func testConfigs() map[string]ServerConfig {
	return map[string]ServerConfig{
		"test": {Command: os.Args[0], Env: map[string]string{testServerEnv: "1"}},
	}
}

func callReq(tool string, args map[string]any) mcp.CallToolRequest {
	r := mcp.CallToolRequest{}
	r.Params.Name = tool
	r.Params.Arguments = args
	return r
}

func TestStdioStarter_UnknownServer(t *testing.T) {
	_, err := StdioStarter(testConfigs())(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v, want an error naming the server", err)
	}
}

func TestStdioStarter_MissingCommand(t *testing.T) {
	cfgs := map[string]ServerConfig{"ghost": {Command: "/nonexistent/aimux-test-mcp"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := StdioStarter(cfgs)(ctx, "ghost"); err == nil {
		t.Fatal("starting a missing command succeeded")
	}
}

func TestStdioBackend_ListToolsRawKeepsSchemas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := StdioStarter(testConfigs())(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	raw, err := b.ListToolsRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, r := range raw {
		tool, err := DecodeTool(r)
		if err != nil {
			t.Fatal(err)
		}
		names[tool.Name] = string(tool.RawInputSchema)
	}
	if len(names) != 4 {
		t.Fatalf("got tools %v, want echo, getenv, progress and crash", names)
	}
	if !strings.Contains(names["echo"], "anyOf") {
		t.Fatalf("echo schema lost anyOf: %s", names["echo"])
	}
}

func TestStdioBackend_CallTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := StdioStarter(testConfigs())(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	res, err := b.CallTool(ctx, callReq("echo", map[string]any{"text": "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].(mcp.TextContent).Text; got != "echo: hi" {
		t.Fatalf("got %q", got)
	}
}

func TestPool_RestartsCrashedStdioBackend(t *testing.T) {
	p := NewPool(StdioStarter(testConfigs()), PoolOptions{CallTimeout: 10 * time.Second})
	defer p.Close()
	ctx := context.Background()

	if r := p.Call(ctx, "test", "", callReq("echo", map[string]any{"text": "one"})); r.IsError {
		t.Fatalf("first call failed: %+v", r)
	}
	if r := p.Call(ctx, "test", "", callReq("crash", nil)); !r.IsError {
		t.Fatal("call that kills the server reported success")
	}
	r := p.Call(ctx, "test", "", callReq("echo", map[string]any{"text": "two"}))
	if r.IsError || r.Content[0].(mcp.TextContent).Text != "echo: two" {
		t.Fatalf("call after crash: %+v, want a restarted backend", r)
	}
}

func TestStdioBackend_CloseKillsProcessesBehindAWrapper(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	cfgs := map[string]ServerConfig{"wrapped": {
		// Like npx or uv: a parent process that runs the real server as a child.
		Command: "sh", Args: []string{"-c", os.Args[0] + "; true"},
		Env: map[string]string{testServerEnv: "1", testPIDFileEnv: pidFile, testLingerEnv: "1"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := StdioStarter(cfgs)(ctx, "wrapped")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pidFile) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(data))
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("server child pid %d not running before Close", pid)
	}
	_ = b.Close()
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("server child %d still running after Close", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStdioBackend_HasNoControllingTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := StdioStarter(testConfigs())(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	pid := b.(*stdioBackend).cmd.Process.Pid
	sid, err := unix.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if mine, _ := unix.Getsid(0); sid != pid || sid == mine {
		t.Fatalf("backend session %d (pid %d, proxy session %d); want its own session", sid, pid, mine)
	}
}

func TestStdioBackend_ChattyStderrDoesNotHang(t *testing.T) {
	cfgs := map[string]ServerConfig{"chatty": {Command: os.Args[0], Env: map[string]string{testServerEnv: "1", testStderrEnv: "1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := StdioStarter(cfgs)(ctx, "chatty")
	if err != nil {
		t.Fatalf("start with 2 MB of stderr: %v (the pipe filled and the server blocked)", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.CallTool(ctx, callReq("echo", map[string]any{"text": "hi"})); err != nil {
		t.Fatal(err)
	}
}

func TestStdioBackend_ExpandsClaudeVariablesAtLaunch(t *testing.T) {
	t.Setenv("AIMUX_TEST_CRED", "s3cret")
	cfgs := map[string]ServerConfig{"test": {Command: os.Args[0], Env: map[string]string{
		testServerEnv: "1", "AIMUX_PROBE": "${AIMUX_TEST_CRED}",
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := StdioStarter(cfgs)(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	res, err := b.CallTool(ctx, callReq("getenv", map[string]any{"name": "AIMUX_PROBE"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].(mcp.TextContent).Text; got != "s3cret" {
		t.Fatalf("server saw AIMUX_PROBE=%q, want the expanded value", got)
	}
}

// The real server answers an unknown tool with a JSON-RPC error; the backend
// is healthy and must keep serving.
func TestPool_UnknownToolOnRealBackendKeepsIt(t *testing.T) {
	var starts atomic.Int32
	p := NewPool(StdioStarter(testConfigs()), PoolOptions{OnStart: func(string, Backend) { starts.Add(1) }})
	defer p.Close()
	ctx := context.Background()
	if r := p.Call(ctx, "test", "", callReq("no_such_tool", nil)); !r.IsError {
		t.Fatal("unknown tool reported success")
	}
	r := p.Call(ctx, "test", "", callReq("echo", map[string]any{"text": "still here"}))
	if r.IsError || r.Content[0].(mcp.TextContent).Text != "echo: still here" {
		t.Fatalf("after a JSON-RPC error: %+v", r)
	}
	if starts.Load() != 1 {
		t.Fatalf("backend started %d times, want 1 (not restarted after a request error)", starts.Load())
	}
}

func TestPool_TimeoutUnblocksAWriteToAServerThatStoppedReading(t *testing.T) {
	cfgs := map[string]ServerConfig{"deaf": {Command: os.Args[0], Env: map[string]string{testDeafEnv: "1"}}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{CallTimeout: 300 * time.Millisecond})
	defer p.Close()
	big := strings.Repeat("x", 4<<20) // far more than a pipe buffer holds
	done := make(chan *mcp.CallToolResult, 1)
	go func() { done <- p.Call(context.Background(), "deaf", "", callReq("echo", map[string]any{"text": big})) }()
	select {
	case r := <-done:
		if !r.IsError {
			t.Fatalf("got a success result from a server that never read the call")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call still blocked writing to the server long after CallTimeout")
	}
}

func TestStdioStarter_FailedStartKeepsStderrTail(t *testing.T) {
	cfgs := map[string]ServerConfig{"dies": {Command: os.Args[0], Env: map[string]string{testDieEnv: "1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := StdioStarter(cfgs)(ctx, "dies")
	if err == nil {
		_ = b.Close()
		t.Fatal("a server that exits before the handshake started")
	}
	tail := stderrTailOf(err)
	want := []string{"die line 4", "die line 5", "die line 6", "die line 7", "die line 8"}
	if strings.Join(tail, "|") != strings.Join(want, "|") {
		t.Fatalf("stderr tail %q, want %q (err: %v)", tail, want, err)
	}
	if strings.Contains(err.Error(), "die line") {
		t.Fatalf("error text %q carries stderr; it belongs in the tail only", err)
	}
}

func TestStdioBackend_ReportsPIDAndNoTailWhenHealthy(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	cfgs := map[string]ServerConfig{"test": {Command: os.Args[0], Env: map[string]string{testServerEnv: "1", testPIDFileEnv: pidFile}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := StdioStarter(cfgs)(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	raw, err := os.ReadFile(pidFile) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	want, _ := strconv.Atoi(string(raw))
	hb, ok := b.(interface {
		PID() int
		StderrTail() []string
	})
	if !ok {
		t.Fatalf("%T does not report PID and StderrTail", b)
	}
	if hb.PID() != want || want == 0 {
		t.Fatalf("PID() = %d, want %d", hb.PID(), want)
	}
	if got := hb.StderrTail(); len(got) != 0 {
		t.Fatalf("StderrTail() = %q for a quiet server, want none", got)
	}
}

func TestStderrRing_KeepsLastLinesAndCutsLongOnes(t *testing.T) {
	var r stderrRing
	long := strings.Repeat("x", 3*stderrLineMax)
	in := "a\nb\nc\n" + long + "\nd\ne\nf"
	r.drain(strings.NewReader(in))
	got := r.Lines()
	if len(got) != stderrTailLines {
		t.Fatalf("kept %d lines %q, want %d", len(got), got, stderrTailLines)
	}
	if got[0] != "c" || len(got[1]) != stderrLineMax || got[4] != "f" {
		t.Fatalf("lines %q, want c, the long line cut to %d bytes, d, e, f", got, stderrLineMax)
	}
	var empty stderrRing
	empty.drain(strings.NewReader(""))
	if l := empty.Lines(); len(l) != 0 {
		t.Fatalf("empty stderr gave %q", l)
	}
}

func TestStdioStarter_FailedStartAfterLotsOfStderrKeepsTheLastLines(t *testing.T) {
	cfgs := map[string]ServerConfig{"dies": {Command: os.Args[0], Env: map[string]string{testDieEnv: "1", testStderrEnv: "1"}}}
	for i := 0; i < 10; i++ { // the race is narrow; one run often misses it
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := StdioStarter(cfgs)(ctx, "dies")
		cancel()
		tail := stderrTailOf(err)
		if len(tail) == 0 || tail[len(tail)-1] != "die line 8" {
			t.Fatalf("run %d: tail %q, want it to end with die line 8 (lines lost when stderr was closed early)", i, tail)
		}
	}
}

func TestStdioStarter_ScrubsSecretsFromStderrTail(t *testing.T) {
	const secret = "s3cr3t-value-for-test"
	cfgs := map[string]ServerConfig{"dies": {Command: os.Args[0], Env: map[string]string{testDieEnv: "1", testEchoEnv: secret}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := StdioStarter(cfgs)(ctx, "dies")
	tail := strings.Join(stderrTailOf(err), "\n")
	if strings.Contains(tail, secret) || !strings.Contains(tail, "token is [redacted]") {
		t.Fatalf("tail %q, want the env value replaced by [redacted]", tail)
	}
}

func TestPool_StdioProcessExitMarksFailed(t *testing.T) {
	cfgs := map[string]ServerConfig{"short": {Command: os.Args[0], Env: map[string]string{testServerEnv: "1", testExitEnv: "300ms"}}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{})
	defer p.Close()
	if r := p.Call(context.Background(), "short", "", callReq("echo", map[string]any{"text": "hi"})); r.IsError {
		t.Fatalf("call failed: %s", resultText(t, r))
	}
	deadline := time.Now().Add(10 * time.Second)
	var s ServerStatus
	for time.Now().Before(deadline) {
		if s = statusOf(t, p, "short"); s.State == StateFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.State != StateFailed || s.LastError != "process exited" || s.Instances != 0 || s.Failures != 0 || s.LastErrorAt.IsZero() {
		t.Fatalf("after the server exited on its own: %+v, want failed, process exited, no instance, no failure counted", s)
	}
}

func TestPool_ReapedStdioBackendIsNotAnExit(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	p := NewPool(StdioStarter(testConfigs()), PoolOptions{Now: clock.Now})
	defer p.Close()
	if r := p.Call(context.Background(), "test", "", callReq("echo", map[string]any{"text": "hi"})); r.IsError {
		t.Fatalf("call failed: %s", resultText(t, r))
	}
	clock.Add(time.Hour)
	if n := p.Reap(time.Minute); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	time.Sleep(300 * time.Millisecond) // the closed child's stderr has ended by now
	if s := statusOf(t, p, "test"); s.State != StateIdleStopped || s.LastError != "" {
		t.Fatalf("after reap: %+v, want idle_stopped with no error", s)
	}
}

func TestScrubber_RedactsEnvValuesAndTokens(t *testing.T) {
	sc := newScrubber(map[string]string{"API_KEY": "hunter2-long", "SHORT": "abc12", "EMPTY": ""})
	cases := []struct{ in, want string }{
		{"key=hunter2-long ok", "key=[redacted] ok"},
		{"short abc12 stays", "short abc12 stays"}, // under 6 chars: too common to redact
		{"Authorization: Bearer abc.def-123", "Authorization: Bearer [redacted]"},
		{"using sk-ant-api03-XYZ123abc", "using [redacted]"},
		{"gh ghp_AbCdEf0123456789", "gh [redacted]"},
		{"hash 0123456789abcdef0123456789abcdef01 end", "hash [redacted] end"},
		{"b64 QWxhZGRpbjpvcGVuIHNlc2FtZQ12345678901234 end", "b64 [redacted] end"},
		{"no digits ERR_SOME_VERY_LONG_IDENTIFIER_NAME_HERE", "no digits ERR_SOME_VERY_LONG_IDENTIFIER_NAME_HERE"},
		{"path /Users/someone/go/src/github.com/x/y/z/w2.go", "path /Users/someone/go/src/github.com/x/y/z/w2.go"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sc.scrub(c.in); got != c.want {
			t.Errorf("scrub(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A secret that straddles the 512-byte cut is scrubbed whole: lines are
// scrubbed before they are cut, so no part of the value survives.
func TestStderrRing_ScrubsBeforeCutting(t *testing.T) {
	secret := "zq9VALUEthatISsecret77" // #nosec G101 -- a fake value the test expects redacted
	sc := newScrubber(map[string]string{"TOKEN": secret})
	r := stderrRing{scrub: sc.scrub}
	pad := strings.Repeat("ab ", stderrLineMax/3)     // words, so no token rule matches the padding
	line := pad[:stderrLineMax-10] + secret + " tail" // the cut at 512 falls inside the secret
	r.drain(strings.NewReader(line + "\n"))
	got := r.Lines()
	if len(got) != 1 || len(got[0]) > stderrLineMax {
		t.Fatalf("lines %q, want one line of at most %d bytes", got, stderrLineMax)
	}
	if strings.Contains(got[0], secret[:6]) {
		t.Fatalf("the cut kept the start of the secret: %q", got[0][len(got[0])-20:])
	}
}

// Closing stderr is not exiting: a server that stops logging but keeps
// serving stays running, in the same process.
func TestPool_StdioServerThatClosesStderrKeepsRunning(t *testing.T) {
	defer exitPoll.Store(exitPoll.Swap(int64(20 * time.Millisecond)))
	cfgs := map[string]ServerConfig{"quiet": {Command: os.Args[0], Env: map[string]string{testServerEnv: "1", testNoStderrEnv: "1"}}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{})
	defer p.Close()
	ctx := context.Background()
	if r := p.Call(ctx, "quiet", "", callReq("echo", map[string]any{"text": "one"})); r.IsError {
		t.Fatalf("first call failed: %s", resultText(t, r))
	}
	pid := statusOf(t, p, "quiet").PID
	time.Sleep(500 * time.Millisecond) // many polls after stderr closed
	s := statusOf(t, p, "quiet")
	if s.State != StateRunning || s.PID != pid || s.Reason != ReasonNone {
		t.Fatalf("after the server closed stderr: %+v, want still running as pid %d", s, pid)
	}
	if r := p.Call(ctx, "quiet", "", callReq("echo", map[string]any{"text": "two"})); r.IsError {
		t.Fatalf("call after stderr closed failed: %s", resultText(t, r))
	}
	if s := statusOf(t, p, "quiet"); s.PID != pid {
		t.Fatalf("server restarted: pid %d, was %d", s.PID, pid)
	}
}

// A wrapper (npx, uv) that exits while a child it started keeps stderr
// open: exit is noticed by watching the process, not stderr's end.
func TestPool_StdioWrapperExitWithStderrHeldOpenMarksFailed(t *testing.T) {
	defer exitPollRunning.Store(exitPollRunning.Swap(int64(50 * time.Millisecond)))
	cfgs := map[string]ServerConfig{"wrapped": {
		Command: "sh", Args: []string{"-c", "sleep 30 & exec " + os.Args[0]}, // sleep inherits stderr
		Env: map[string]string{testServerEnv: "1", testExitEnv: "300ms"},
	}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{})
	defer p.Close()
	if r := p.Call(context.Background(), "wrapped", "", callReq("echo", map[string]any{"text": "hi"})); r.IsError {
		t.Fatalf("call failed: %s", resultText(t, r))
	}
	if s := waitState(t, p, "wrapped", StateFailed); s.Reason != ReasonExited {
		t.Fatalf("Reason %q, want exited", s.Reason)
	}
}

func TestPool_StdioProcessExitIsReasonExited(t *testing.T) {
	cfgs := map[string]ServerConfig{"short": {Command: os.Args[0], Env: map[string]string{testServerEnv: "1", testExitEnv: "300ms"}}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{})
	defer p.Close()
	p.Call(context.Background(), "short", "", callReq("echo", map[string]any{"text": "hi"}))
	if s := waitState(t, p, "short", StateFailed); s.Reason != ReasonExited {
		t.Fatalf("Reason %q, want exited", s.Reason)
	}
}

// A tools/list error is the server's text, so it is scrubbed like any other.
func TestStdioBackend_ListToolsErrorIsScrubbed(t *testing.T) {
	const secret = "abc123"
	cfgs := map[string]ServerConfig{"listerr": {Command: os.Args[0], Env: map[string]string{
		testListErrEnv: "1", testEchoEnv: secret, "SECRET_TOKEN": secret,
	}}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := p.Lease(ctx, "listerr")
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.ListToolsRaw(ctx)
	_ = b.Close()
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "bad key [redacted]") {
		t.Fatalf("list error %v, want the env value replaced by [redacted]", err)
	}
	if s := statusOf(t, p, "listerr"); strings.Contains(s.LastError, secret) {
		t.Fatalf("LastError %q contains the env value", s.LastError)
	}
}

// A JSON-RPC error answer to tools/list means the server is alive: a probe
// reports it and keeps the backend, as a call's RPC error does.
func TestPool_ListRPCErrorKeepsStdioBackend(t *testing.T) {
	cfgs := map[string]ServerConfig{"listerr": {Command: os.Args[0], Env: map[string]string{testListErrEnv: "1"}}}
	p := NewPool(StdioStarter(cfgs), PoolOptions{})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := p.Lease(ctx, "listerr")
	if err != nil {
		t.Fatal(err)
	}
	pid := statusOf(t, p, "listerr").PID
	_, err = b.ListToolsRaw(ctx)
	_ = b.Close()
	var rpcErr *RequestError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("list error %v (%T), want a RequestError", err, err)
	}
	if s := statusOf(t, p, "listerr"); s.State != StateRunning || s.PID != pid || s.Reason != ReasonNone {
		t.Fatalf("after an RPC error to tools/list: %+v, want still running as pid %d", s, pid)
	}
	if r := p.Call(ctx, "listerr", "", callReq("anything", nil)); r.IsError {
		t.Fatalf("call after the list error failed: %s", resultText(t, r))
	}
	if s := statusOf(t, p, "listerr"); s.PID != pid {
		t.Fatalf("server restarted: pid %d, was %d", s.PID, pid)
	}
}

func TestStdioBackend_ResolvedByInstallIsNotExpandedAgain(t *testing.T) {
	t.Setenv("suffix", "EXPANDED")
	cfgs := map[string]ServerConfig{"test": {Command: os.Args[0], Env: map[string]string{
		testServerEnv: "1", "AIMUX_PROBE": "pa$$word-${suffix}", // a resolved value that looks like a variable
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := StdioStarter(cfgs, "test")(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	res, err := b.CallTool(ctx, callReq("getenv", map[string]any{"name": "AIMUX_PROBE"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].(mcp.TextContent).Text; got != "pa$$word-${suffix}" {
		t.Fatalf("server saw AIMUX_PROBE=%q, want the resolved value unchanged", got)
	}
}
