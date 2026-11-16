package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	t.Cleanup(ts.Close) // after the backend's cleanup closes its listening stream
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
	ts.CloseClientConnections() // as a server that dies drops them, listening stream included
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

func TestWithoutProtocolMeta(t *testing.T) {
	in := &mcp.Meta{ProgressToken: "p1", AdditionalFields: map[string]any{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientCapabilities: map[string]any{},
		"trace":                       "keep",
	}}
	out := withoutProtocolMeta(in)
	if out.ProgressToken != "p1" || len(out.AdditionalFields) != 1 || out.AdditionalFields["trace"] != "keep" {
		t.Fatalf("out = %+v", out)
	}
	if len(in.AdditionalFields) != 3 {
		t.Fatalf("input modified: %+v", in.AdditionalFields)
	}
	if withoutProtocolMeta(nil) != nil {
		t.Fatal("nil meta must stay nil")
	}
	only := withoutProtocolMeta(&mcp.Meta{AdditionalFields: map[string]any{mcp.MetaKeyLogLevel: "info"}})
	if only.AdditionalFields != nil {
		t.Fatalf("only protocol keys: %+v", only.AdditionalFields)
	}
}

// A server's notifications/tools/list_changed reaches the callbacks
// OnToolsChanged registered; other notifications do not.
func TestHTTPBackend_ToolsChangedCallback(t *testing.T) {
	s := server.NewMCPServer("fixture", "1")
	s.AddTool(mcp.NewTool("echo", mcp.WithString("x")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(req.GetString("x", "")), nil
		})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	b := startHTTPBackend(t, ts.URL, nil)
	n, ok := b.(ToolsChangedNotifier)
	if !ok {
		t.Fatalf("%T does not implement ToolsChangedNotifier", b)
	}
	var calls, seconds atomic.Int32
	first, second := make(chan struct{}, 4), make(chan struct{}, 4)
	n.OnToolsChanged(func() { calls.Add(1); first <- struct{}{} })
	n.OnToolsChanged(func() { seconds.Add(1); second <- struct{}{} }) // several registrations are allowed
	// Another notification fires nothing. Should it reach the callbacks after
	// this wait, the counts below catch it: it is ahead of list_changed on
	// the same stream.
	s.SendNotificationToAllClients("notifications/message", map[string]any{"level": "info", "data": "x"})
	select {
	case <-first:
		t.Fatal("a log notification fired the tools-changed callback")
	case <-second:
		t.Fatal("a log notification fired the second callback")
	case <-time.After(200 * time.Millisecond):
	}
	s.SendNotificationToAllClients(mcp.MethodNotificationToolsListChanged, nil)
	for _, ch := range []chan struct{}{first, second} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("no callback for tools/list_changed")
		}
	}
	time.Sleep(200 * time.Millisecond) // grace: nothing else may arrive
	if c, d := calls.Load(), seconds.Load(); c != 1 || d != 1 {
		t.Fatalf("callbacks ran %d and %d times, want 1 each (only list_changed counts)", c, d)
	}
}

// getFailingServer is a real MCP server whose GET (the listening stream)
// always answers 500; gets counts the GETs that reached it.
func getFailingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	s := server.NewMCPServer("fixture", "1")
	s.AddTool(mcp.NewTool("echo"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	inner := server.NewStreamableHTTPServer(s)
	gets := &atomic.Int32{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, gets
}

// captureListenLog sends listenLog's lines to the returned func for the
// test's duration.
func captureListenLog(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	old := listenLog
	listenLog = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	t.Cleanup(func() { listenLog = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// A listening stream that keeps failing is logged once per backend (never
// with its headers), not once per retry; after listenMaxFailures failed
// streams in a row the backend stops listening, so no further GET reaches the
// server, and calls still work. One rig serves both checks (mcp-go retries
// the stream every second, so each extra retry cycle costs a second).
func TestHTTPBackend_ListenFailureLoggedOnceThenStops(t *testing.T) {
	logged := captureListenLog(t)
	old := listenMaxFailures
	listenMaxFailures = 2
	t.Cleanup(func() { listenMaxFailures = old })
	ts, gets := getFailingServer(t)
	b := startHTTPBackend(t, ts.URL, map[string]string{"Authorization": "Bearer secret-tok"})
	deadline := time.Now().Add(8 * time.Second)
	for gets.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := gets.Load(); n != 2 {
		t.Fatalf("%d GETs reached the server, want 2 (retried once)", n)
	}
	lines := logged()
	if len(lines) != 1 {
		t.Fatalf("logged %d lines over 2 failures, want 1: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "slack") || !strings.Contains(lines[0], "500") || strings.Contains(lines[0], "secret-tok") {
		t.Fatalf("line %q: want the server and status, never the headers", lines[0])
	}
	// One more retry cycle: the guard now answers the stream's GET itself.
	time.Sleep(1300 * time.Millisecond)
	if n := gets.Load(); n != 2 {
		t.Fatalf("%d GETs reached the server, want 2 then none", n)
	}
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatalf("list after the stream stopped: %v", err)
	}
}

// rtFunc is an http.RoundTripper from a function.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A GET that resumes a response stream (it carries Last-Event-ID) is not
// the listening stream: its failures never count toward the breaker, and
// once the breaker tripped it still reaches the server, never a 405.
func TestListenGuard_ResumptionGETsBypassTheBreaker(t *testing.T) {
	captureListenLog(t)
	old := listenMaxFailures
	listenMaxFailures = 2
	t.Cleanup(func() { listenMaxFailures = old })
	var reached atomic.Int32
	g := &listenGuard{server: "slack", next: rtFunc(func(r *http.Request) (*http.Response, error) {
		reached.Add(1)
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody, Request: r}, nil
	})}
	get := func(resume bool) int {
		req := httptest.NewRequest(http.MethodGet, "http://backend/mcp", nil)
		if resume {
			req.Header.Set("Last-Event-ID", "ev-7")
		}
		resp, err := g.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	get(false) // one listening failure
	for range 5 {
		if code := get(true); code != http.StatusInternalServerError {
			t.Fatalf("resumption GET answered %d, want the server's 500", code)
		}
	}
	if code := get(false); code != http.StatusInternalServerError || reached.Load() != 7 {
		t.Fatalf("listening GET after resumption failures: %d, %d reached; want it to reach the server (resumptions do not count)", code, reached.Load())
	}
	if code := get(false); code != http.StatusMethodNotAllowed { // two listening failures in a row: tripped
		t.Fatalf("listening GET after the breaker tripped: %d, want 405", code)
	}
	if code := get(true); code != http.StatusInternalServerError || reached.Load() != 8 {
		t.Fatalf("resumption GET after the breaker tripped: %d, %d reached; want it to reach the server", code, reached.Load())
	}
}

// A listening GET answered 200 with a body that is not an event stream is
// unusable to mcp-go (it retries every second), so it counts as a failed
// stream: logged once, and after listenMaxFailures in a row no further GET
// reaches the server.
func TestHTTPBackend_ListenNonSSESuccessCountsAsFailure(t *testing.T) {
	logged := captureListenLog(t)
	old := listenMaxFailures
	listenMaxFailures = 2
	t.Cleanup(func() { listenMaxFailures = old })
	s := server.NewMCPServer("fixture", "1")
	inner := server.NewStreamableHTTPServer(s)
	var gets atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	startHTTPBackend(t, ts.URL, nil)
	deadline := time.Now().Add(8 * time.Second)
	for gets.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := gets.Load(); n != 2 {
		t.Fatalf("%d GETs reached the server, want 2 (retried once)", n)
	}
	if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "application/json") {
		t.Fatalf("logged %q, want one line naming the content type", lines)
	}
	// One more retry cycle: the guard now answers the stream's GET itself.
	time.Sleep(1300 * time.Millisecond)
	if n := gets.Load(); n != 2 {
		t.Fatalf("%d GETs reached the server, want 2 then none", n)
	}
}

// Only a 2xx event stream resets the count; a 2xx with another content
// type counts as a failure.
func TestListenGuard_OnlyAnEventStreamResetsTheCount(t *testing.T) {
	captureListenLog(t)
	old := listenMaxFailures
	listenMaxFailures = 2
	t.Cleanup(func() { listenMaxFailures = old })
	contentType := ""
	g := &listenGuard{server: "slack", next: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: http.NoBody, Request: r}, nil
	})}
	get := func(ct string) int {
		contentType = ct
		resp, err := g.RoundTrip(httptest.NewRequest(http.MethodGet, "http://backend/mcp", nil))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	get("application/json")
	get("text/event-stream; charset=utf-8") // a working stream resets the count
	get("application/json")
	if code := get("text/event-stream"); code != http.StatusOK {
		t.Fatalf("after one failure since the reset: %d, want the stream to reach the server", code)
	}
	get("application/json")
	get("application/json")
	if code := get("text/event-stream"); code != http.StatusMethodNotAllowed {
		t.Fatalf("after two non-stream 200s in a row: %d, want 405", code)
	}
}
