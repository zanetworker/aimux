package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func newHTTPMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("fixture", "1")
	s.AddTool(mcp.NewTool("echo", mcp.WithString("x")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(req.GetString("x", "")), nil
		})
	s.AddTool(mcp.NewTool("post_message", mcp.WithString("text")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("posted"), nil
		})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	return ts
}

func startHTTPBackend(t *testing.T, url string, headers map[string]string) Backend {
	t.Helper()
	st := HTTPStarter(map[string]HTTPServerConfig{"slack": {URL: url, Headers: headers}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := st(ctx, "slack")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestHTTPBackendListsTools(t *testing.T) {
	b := startHTTPBackend(t, newHTTPMCPServer(t).URL, nil)
	raw, err := b.ListToolsRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d tools, want 2", len(raw))
	}
	names := map[string]bool{}
	for _, r := range raw {
		var tl struct{ Name string }
		if err := json.Unmarshal(r, &tl); err != nil {
			t.Fatal(err)
		}
		names[tl.Name] = true
	}
	if !names["echo"] || !names["post_message"] {
		t.Fatalf("names = %v", names)
	}
}

func TestHTTPBackendCallsTool(t *testing.T) {
	b := startHTTPBackend(t, newHTTPMCPServer(t).URL, nil)
	req := mcp.CallToolRequest{}
	req.Params.Name = "echo"
	req.Params.Arguments = map[string]any{"x": "hi"}
	res, err := b.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok || tc.Text != "hi" {
		t.Fatalf("content = %#v", res.Content)
	}
}

func TestHTTPBackendUnknownToolIsRequestError(t *testing.T) {
	b := startHTTPBackend(t, newHTTPMCPServer(t).URL, nil)
	req := mcp.CallToolRequest{}
	req.Params.Name = "nope"
	_, err := b.CallTool(context.Background(), req)
	var re *RequestError
	if err == nil || !errors.As(err, &re) {
		t.Fatalf("err = %v, want RequestError", err)
	}
}

func TestHTTPBackendSendsHeaders(t *testing.T) {
	s := server.NewMCPServer("fixture", "1")
	inner := server.NewStreamableHTTPServer(s)
	got := make(chan string, 16)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("Authorization"):
		default:
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()
	startHTTPBackend(t, ts.URL, map[string]string{"Authorization": "Bearer tok"})
	select {
	case h := <-got:
		if h != "Bearer tok" {
			t.Fatalf("Authorization = %q", h)
		}
	default:
		t.Fatal("no request reached the server")
	}
}

func TestHTTPStarterUnknownServer(t *testing.T) {
	st := HTTPStarter(map[string]HTTPServerConfig{})
	_, err := st(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want it to name ghost", err)
	}
}

func TestHTTPBackendServerDown(t *testing.T) {
	ts := newHTTPMCPServer(t)
	b := startHTTPBackend(t, ts.URL, nil)
	ts.Close()
	done := make(chan error, 1)
	go func() {
		_, err := b.ListToolsRaw(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error from a closed server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListToolsRaw did not return within 5s")
	}
}

// stallingServer serves a real MCP server until stall is set, then holds
// every request open (up but silent) until the test ends.
func stallingServer(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	inner := server.NewStreamableHTTPServer(server.NewMCPServer("fixture", "1"))
	stall := &atomic.Bool{}
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stall.Load() {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(release); ts.Close() })
	return ts, stall
}

func TestHTTPBackendStalledServerTimesOut(t *testing.T) {
	ts, stall := stallingServer(t)
	b := startHTTPBackend(t, ts.URL, nil)
	stall.Store(true)
	begin := time.Now()
	_, err := b.ListToolsRaw(context.Background())
	if err == nil {
		t.Fatal("want an error from a silent server")
	}
	if d := time.Since(begin); d > 6*time.Second {
		t.Fatalf("ListToolsRaw took %v, want under 6s", d)
	}
}

func TestHTTPBackendStalledCallTimesOut(t *testing.T) {
	old := httpCallTimeout
	httpCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { httpCallTimeout = old })
	ts, stall := stallingServer(t)
	b := startHTTPBackend(t, ts.URL, nil)
	stall.Store(true)
	req := mcp.CallToolRequest{}
	req.Params.Name = "echo"
	begin := time.Now()
	if _, err := b.CallTool(context.Background(), req); err == nil {
		t.Fatal("want an error from a silent server")
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Fatalf("CallTool took %v, want about 300ms", d)
	}
}

func TestHTTPStarterStalledServerTimesOut(t *testing.T) {
	old := httpStartTimeout
	httpStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { httpStartTimeout = old })
	ts, stall := stallingServer(t)
	stall.Store(true)
	st := HTTPStarter(map[string]HTTPServerConfig{"s": {URL: ts.URL}})
	begin := time.Now()
	if _, err := st(context.Background(), "s"); err == nil {
		t.Fatal("want an error from a silent server")
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Fatalf("start took %v, want about 300ms", d)
	}
}
