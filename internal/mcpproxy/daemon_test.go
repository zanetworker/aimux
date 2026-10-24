package mcpproxy

import (
	"context"
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
