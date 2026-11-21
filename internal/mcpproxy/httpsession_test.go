package mcpproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// restartable is an MCP HTTP server that can "restart": a fresh server
// with no sessions replaces it at the same URL. Optional deny answers a
// request for a session the server does not know with FastMCP's 400 "No
// valid session ID provided" instead of mcp-go's 404.
type restartable struct {
	mu      sync.Mutex
	h       http.Handler
	known   map[string]bool
	fastmcp bool
	calls   atomic.Int64
}

func newRestartable(t *testing.T, fastmcp bool) (*restartable, *httptest.Server) {
	r := &restartable{fastmcp: fastmcp}
	r.restart()
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return r, ts
}

func (r *restartable) restart() {
	s := server.NewMCPServer("fixture", "1")
	s.AddTool(mcp.NewTool("echo", mcp.WithString("x")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r.calls.Add(1)
		return mcp.NewToolResultText(req.GetString("x", "")), nil
	})
	r.mu.Lock()
	r.h, r.known = server.NewStreamableHTTPServer(s), map[string]bool{}
	r.mu.Unlock()
}

func (r *restartable) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	h, known := r.h, r.known
	r.mu.Unlock()
	if id := req.Header.Get("Mcp-Session-Id"); r.fastmcp && id != "" && !known[id] && req.Method == http.MethodPost {
		http.Error(w, "Bad Request: No valid session ID provided", http.StatusBadRequest)
		return
	}
	rec := &sessionRecorder{ResponseWriter: w}
	h.ServeHTTP(rec, req)
	if id := rec.Header().Get("Mcp-Session-Id"); id != "" {
		r.mu.Lock()
		r.known[id] = true
		r.mu.Unlock()
	}
}

type sessionRecorder struct{ http.ResponseWriter }

func (s *sessionRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// A backend that restarted no longer knows the proxy's session: the proxy
// opens a new session and retries the call once, for mcp-go's 404 and for
// FastMCP's 400 "No valid session ID provided" (seen with JirAIa).
func TestHTTPBackend_NewSessionAfterBackendRestart(t *testing.T) {
	for _, fastmcp := range []bool{false, true} {
		r, ts := newRestartable(t, fastmcp)
		b := startHTTPBackend(t, ts.URL, nil)
		call := func() (*mcp.CallToolResult, error) {
			req := mcp.CallToolRequest{}
			req.Params.Name, req.Params.Arguments = "echo", map[string]any{"x": "hi"}
			return b.CallTool(context.Background(), req)
		}
		if _, err := call(); err != nil {
			t.Fatalf("fastmcp=%v: first call: %v", fastmcp, err)
		}
		r.restart()
		res, err := call()
		if err != nil {
			t.Fatalf("fastmcp=%v: call after restart: %v", fastmcp, err)
		}
		if txt, _ := res.Content[0].(mcp.TextContent); txt.Text != "hi" {
			t.Fatalf("fastmcp=%v: result %+v", fastmcp, res)
		}
		if n := r.calls.Load(); n != 2 {
			t.Fatalf("fastmcp=%v: tool ran %d times, want 2 (no double call)", fastmcp, n)
		}
		r.restart()
		if _, err := b.ListToolsRaw(context.Background()); err != nil {
			t.Fatalf("fastmcp=%v: list after restart: %v", fastmcp, err)
		}
	}
}

// A client whose session the proxy ended (its owner exited, it idled out,
// or the endpoint was rebuilt) is never stuck: the proxy's endpoints use
// mcp-go's default (stateless) session ids, so the old id keeps answering
// and the session's per-session backends start again under it. (Not the
// spec's 404 for an unknown session: that would make every endpoint
// rebuild force each client to initialize again; see the plan 4c ledger.)
func TestEndpoint_SweptSessionStillAnswers(t *testing.T) {
	e := newEndpoints(time.Hour)
	e.add("weather", server.NewMCPServer("weather", "1"))
	post := func(session, body string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		rec := httptest.NewRecorder()
		e.get("weather").http.ServeHTTP(rec, req)
		return rec.Result()
	}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + mcp.LATEST_LEGACY_PROTOCOL_VERSION + `","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	resp := post("", initBody)
	s := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || s == "" {
		t.Fatalf("initialize: %d session %q", resp.StatusCode, s)
	}
	e.get("weather").terminate(s) // what the sweeper does
	if resp := post(s, list); resp.StatusCode != http.StatusOK {
		t.Fatalf("swept session: %d, want it to keep answering", resp.StatusCode)
	}
	e.add("weather", server.NewMCPServer("weather", "2")) // a rebuild replaces the endpoint
	if resp := post(s, list); resp.StatusCode != http.StatusOK {
		t.Fatalf("session of a replaced endpoint: %d, want it to keep answering", resp.StatusCode)
	}
}
