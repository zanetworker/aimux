package mcpproxy

import (
	"context"
	"encoding/json"
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
)

// TestMain lets the test binary act as a stdio MCP server for the tests
// below, so they exercise real processes and the real protocol.
func TestMain(m *testing.M) {
	if os.Getenv(testServerEnv) == "1" {
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
	s.AddTool(mcp.NewTool("crash", mcp.WithDescription("Exit the process")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			os.Exit(3)
			return nil, nil
		})
	if os.Getenv(testStderrEnv) == "1" {
		// Like a chatty server: far more log output than a pipe buffer holds.
		_, _ = os.Stderr.Write([]byte(strings.Repeat("log line from the server\n", 80000)))
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
	if len(names) != 3 {
		t.Fatalf("got tools %v, want echo, getenv and crash", names)
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
