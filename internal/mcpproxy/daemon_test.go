package mcpproxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// serveCatalog runs a daemon for cat on a free loopback port and returns
// its base URL (http://127.0.0.1:port) and a stop function.
func serveCatalog(t *testing.T, cat *Catalog, pool *Pool, opt DaemonOptions) (base string, stop func() error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServers(cat, pool), pool, opt) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return after cancel")
			return nil
		}
	}
	t.Cleanup(func() { _ = stop() })
	return "http://" + ln.Addr().String(), stop
}

func startDaemon(t *testing.T, fs *fakeStarter, opt DaemonOptions) (base string, stop func() error) {
	t.Helper()
	return serveCatalog(t, testCatalog(), NewPool(fs.start, PoolOptions{}), opt)
}

func endpointURL(base, server string) string { return base + EndpointPath(server) }

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

func mustCall(t *testing.T, c *client.Client, tool string) *mcp.CallToolResult {
	t.Helper()
	r, err := c.CallTool(context.Background(), req(tool))
	if err != nil || r.IsError {
		t.Fatalf("call %s: %v %+v", tool, err, r)
	}
	return r
}

func TestServe_EachServerHasItsOwnEndpointOverHTTP(t *testing.T) {
	fs := newFakeStarter()
	base, stop := startDaemon(t, fs, DaemonOptions{})

	slack := httpClient(t, endpointURL(base, "slack"))
	if got := toolNames(t, slack); len(got) != 1 || got[0] != "post_message" {
		t.Fatalf("/mcp/slack lists %v, want exactly [post_message]", got)
	}
	nb := httpClient(t, endpointURL(base, "notebooklm-mcp"))
	if got := toolNames(t, nb); len(got) != 1 || got[0] != longTool {
		t.Fatalf("/mcp/notebooklm-mcp lists %v, want the long name untruncated", got)
	}
	mustCall(t, slack, "post_message")
	if fs.count("slack") != 1 || fs.count("notebooklm-mcp") != 0 {
		t.Fatalf("starts: slack %d, notebooklm-mcp %d; want only slack", fs.count("slack"), fs.count("notebooklm-mcp"))
	}
	if got := fs.backends[0].lastReq.Params.Name; got != "post_message" {
		t.Fatalf("backend got %q, want post_message", got)
	}
	// Another server's tool is not on this endpoint.
	if r, err := slack.CallTool(context.Background(), req(longTool)); err == nil && !r.IsError {
		t.Fatal("/mcp/slack served notebooklm-mcp's tool")
	}
	_ = slack.Close()
	_ = nb.Close()
	if err := stop(); err != nil {
		t.Fatalf("Serve returned %v after cancel, want nil", err)
	}
	if !fs.backends[0].closed.Load() {
		t.Fatal("backend still open after the daemon stopped")
	}
}

func TestServe_UnknownOrMalformedPathsAre404(t *testing.T) {
	base, _ := startDaemon(t, newFakeStarter(), DaemonOptions{})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	for _, p := range []string{"/mcp", "/mcp/", "/mcp/nope", "/mcp/slack/extra", "/mcp/slack/", "/mcpx/slack"} {
		r, _ := http.NewRequest("POST", base+p, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("POST %s: %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestServe_ServerNamesNeedingEscapingRoute(t *testing.T) {
	cat := &Catalog{}
	names := []string{"my server", "team/weather", "café", "a%b", ".."}
	for _, n := range names {
		cat.Set(n, []json.RawMessage{json.RawMessage(`{"name":"tool","inputSchema":{"type":"object"}}`)}, time.Now())
	}
	fs := newFakeStarter()
	base, _ := serveCatalog(t, cat, NewPool(fs.start, PoolOptions{}), DaemonOptions{})
	for _, n := range names {
		c := httpClient(t, endpointURL(base, n))
		mustCall(t, c, "tool")
		_ = c.Close()
		if fs.count(n) != 1 {
			t.Fatalf("calling /mcp/%s started %q %d times, want 1", url.PathEscape(n), n, fs.count(n))
		}
	}
	// The unescaped form of a name with a slash is not that server.
	if resp, err := http.Post(base+"/mcp/team/weather", "application/json", strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	} else if _ = resp.Body.Close(); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /mcp/team/weather: %d, want 404", resp.StatusCode)
	}
}

func TestServe_RejectsNonLocalHost(t *testing.T) {
	base, _ := startDaemon(t, newFakeStarter(), DaemonOptions{})
	req, _ := http.NewRequest("POST", endpointURL(base, "slack"), nil)
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
	base, _ := startDaemon(t, fs, DaemonOptions{IdleTimeout: 50 * time.Millisecond, ReapEvery: 20 * time.Millisecond})
	c := httpClient(t, endpointURL(base, "slack"))
	defer func() { _ = c.Close() }()
	mustCall(t, c, "post_message")
	waitFor(t, "idle backend reaped", fs.backends[0].closed.Load)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func browserCatalog() *Catalog {
	cat := &Catalog{}
	cat.Set("playwright", []json.RawMessage{json.RawMessage(`{"name":"click","inputSchema":{"type":"object"}}`)}, time.Now())
	cat.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"post","inputSchema":{"type":"object"}}`)}, time.Now())
	return cat
}

func TestServe_PerSessionBackendsOverHTTP(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	base, _ := serveCatalog(t, browserCatalog(), pool, DaemonOptions{})

	pa, pb := httpClient(t, endpointURL(base, "playwright")), httpClient(t, endpointURL(base, "playwright"))
	sa, sb := httpClient(t, endpointURL(base, "slack")), httpClient(t, endpointURL(base, "slack"))
	mustCall(t, pa, "click")
	mustCall(t, pb, "click")
	mustCall(t, sa, "post")
	mustCall(t, sb, "post")
	if got := fs.count("playwright"); got != 2 {
		t.Fatalf("playwright started %d times for two client sessions, want 2", got)
	}
	if got := fs.count("slack"); got != 1 {
		t.Fatalf("slack started %d times, want 1 shared", got)
	}

	_ = pa.Close() // ends client a's playwright session
	waitFor(t, "client a's per-session backend closed", fs.backends[0].closed.Load)
	if fs.backends[1].closed.Load() || fs.backends[2].closed.Load() {
		t.Fatal("ending one session closed another session's backend or the shared one")
	}
	_ = sa.Close() // a session on the shared server's endpoint ending keeps the shared backend
	time.Sleep(50 * time.Millisecond)
	if fs.backends[2].closed.Load() {
		t.Fatal("ending a slack session closed the shared slack backend")
	}
	_ = pb.Close()
	_ = sb.Close()
}

// Claude Code exits without ending its MCP session; its per-session
// backends must still be stopped once the session goes idle.
func TestServe_AbandonedSessionStopsItsBackends(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	base, _ := serveCatalog(t, browserCatalog(), pool, DaemonOptions{IdleTimeout: 300 * time.Millisecond, ReapEvery: time.Hour})

	c := httpClient(t, endpointURL(base, "playwright"))
	mustCall(t, c, "click")
	// No c.Close(): the client just goes away. ReapEvery is an hour, so only
	// session expiry can stop the backend here.
	waitFor(t, "abandoned session's per-session backend stopped", fs.backends[0].closed.Load)
}
