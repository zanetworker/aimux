package mcpproxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func startDaemon(t *testing.T, fs *fakeStarter, opt DaemonOptions) (url string, stop func() error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(fs.start, PoolOptions{})
	srv, _ := NewServer(testCatalog(), pool, "mux")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, srv, pool, opt) }()
	return "http://" + ln.Addr().String() + "/mcp", func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return after cancel")
			return nil
		}
	}
}

func httpClient(t *testing.T, url string) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestServe_ListAndCallOverHTTP(t *testing.T) {
	fs := newFakeStarter()
	url, stop := startDaemon(t, fs, DaemonOptions{})
	c := httpClient(t, url)

	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil || len(res.Tools) != 2 {
		t.Fatalf("list over HTTP: %v, %d tools", err, len(res.Tools))
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "slack__post_message"
	if r, err := c.CallTool(context.Background(), req); err != nil || r.IsError {
		t.Fatalf("call over HTTP: %v %+v", err, r)
	}
	_ = c.Close()
	if err := stop(); err != nil {
		t.Fatalf("Serve returned %v after cancel, want nil", err)
	}
	if !fs.backends[0].closed.Load() {
		t.Fatal("backend still open after the daemon stopped")
	}
}

func TestServe_RejectsNonLocalHost(t *testing.T) {
	fs := newFakeStarter()
	url, stop := startDaemon(t, fs, DaemonOptions{})
	defer func() { _ = stop() }()
	req, _ := http.NewRequest("POST", url, nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403", resp.StatusCode)
	}
}

func TestServe_ReapsIdleBackends(t *testing.T) {
	fs := newFakeStarter()
	url, stop := startDaemon(t, fs, DaemonOptions{IdleTimeout: 50 * time.Millisecond, ReapEvery: 20 * time.Millisecond})
	defer func() { _ = stop() }()
	c := httpClient(t, url)
	defer func() { _ = c.Close() }()
	req := mcp.CallToolRequest{}
	req.Params.Name = "slack__post_message"
	if _, err := c.CallTool(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !fs.backends[0].closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("idle backend was not reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServe_PerSessionBackendsOverHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cat := &Catalog{}
	cat.Set("playwright", []json.RawMessage{json.RawMessage(`{"name":"click","inputSchema":{"type":"object"}}`)}, time.Now())
	cat.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"post","inputSchema":{"type":"object"}}`)}, time.Now())
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	srv, _ := NewServer(cat, pool, "mux")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, srv, pool, DaemonOptions{}) }()
	defer func() { cancel(); <-done }()
	url := "http://" + ln.Addr().String() + "/mcp"

	call := func(c *client.Client, tool string) {
		t.Helper()
		req := mcp.CallToolRequest{}
		req.Params.Name = tool
		if r, err := c.CallTool(context.Background(), req); err != nil || r.IsError {
			t.Fatalf("call %s: %v %+v", tool, err, r)
		}
	}
	a, b := httpClient(t, url), httpClient(t, url)
	call(a, "playwright__click")
	call(b, "playwright__click")
	call(a, "slack__post")
	call(b, "slack__post")
	if got := fs.count("playwright"); got != 2 {
		t.Fatalf("playwright started %d times for two clients, want 2", got)
	}
	if got := fs.count("slack"); got != 1 {
		t.Fatalf("slack started %d times, want 1 shared", got)
	}

	_ = a.Close() // ends client a's session
	deadline := time.Now().Add(3 * time.Second)
	for !fs.backends[0].closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("client a's per-session backend still open after it disconnected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fs.backends[1].closed.Load() || fs.backends[2].closed.Load() {
		t.Fatal("disconnecting one client closed another client's backend or the shared one")
	}
	_ = b.Close()
}

// Claude Code exits without ending its MCP session; its per-session
// backends must still be stopped once the session goes idle.
func TestServe_AbandonedSessionStopsItsBackends(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cat := &Catalog{}
	cat.Set("playwright", []json.RawMessage{json.RawMessage(`{"name":"click","inputSchema":{"type":"object"}}`)}, time.Now())
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	srv, _ := NewServer(cat, pool, "mux")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, srv, pool, DaemonOptions{IdleTimeout: 300 * time.Millisecond, ReapEvery: time.Hour})
	}()
	defer func() { cancel(); <-done }()

	c := httpClient(t, "http://"+ln.Addr().String()+"/mcp")
	req := mcp.CallToolRequest{}
	req.Params.Name = "playwright__click"
	if _, err := c.CallTool(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// No c.Close(): the client just goes away. ReapEvery is an hour, so only
	// session expiry can stop the backend here.
	deadline := time.Now().Add(5 * time.Second)
	for !fs.backends[0].closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("abandoned session's per-session backend was never stopped")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
