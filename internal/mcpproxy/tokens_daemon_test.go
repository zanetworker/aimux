package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// fakeTokens is a TokenAuthorizer with fixed tokens (toolgateway.Tokens is
// the real one; mcpproxy cannot import it).
type fakeTokens map[string]struct {
	session string
	allow   map[string][]string
}

func (f fakeTokens) Lookup(token string) (string, map[string][]string, bool) {
	e, ok := f[token]
	return e.session, e.allow, ok
}

func gatewayCatalog() *Catalog {
	c := &Catalog{}
	at := time.Now()
	tool := func(n string) json.RawMessage {
		return json.RawMessage(`{"name":"` + n + `","inputSchema":{"type":"object"}}`)
	}
	c.Set("wiki", []json.RawMessage{tool("wiki_search"), tool("wiki_read")}, at)
	c.Set("jira", []json.RawMessage{tool("get_issue_details")}, at)
	c.Set("slack", []json.RawMessage{tool("search_messages")}, at)
	return c
}

func bearerClient(t *testing.T, url, token string) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient(url, transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
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
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func postInit(t *testing.T, base, path, auth string) int {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` +
		mcp.LATEST_PROTOCOL_VERSION + `","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	r, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestDaemon_TokenOnlyReachesItsServers(t *testing.T) {
	toks := fakeTokens{
		"tok-a": {"sess-a", map[string][]string{"jira": {"get_issue_details"}}},
	}
	pool := NewPool(newFakeStarter().start, PoolOptions{})
	base, _ := serveCatalog(t, gatewayCatalog(), pool, DaemonOptions{Tokens: toks})

	if got := postInit(t, base, EndpointPath("jira"), "Bearer tok-a"); got != http.StatusOK {
		t.Fatalf("token for jira on /mcp/jira: got %d, want 200", got)
	}
	if got := postInit(t, base, EndpointPath("slack"), "Bearer tok-a"); got != http.StatusForbidden {
		t.Fatalf("token for jira on /mcp/slack: got %d, want 403", got)
	}
	if got := postInit(t, base, EndpointPath("jira"), "Bearer nope"); got != http.StatusUnauthorized {
		t.Fatalf("unknown token: got %d, want 401", got)
	}
	if got := postInit(t, base, EndpointPath("jira"), ""); got != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", got)
	}
	// Admin routes are not for session tokens.
	if got := postInit(t, base, "/status", "Bearer tok-a"); got != http.StatusForbidden {
		t.Fatalf("session token on /status: got %d, want 403", got)
	}
}

func TestDaemon_ToolsListFilteredPerToken(t *testing.T) {
	toks := fakeTokens{
		"tok-a":    {"sess-a", map[string][]string{"wiki": {"wiki_search"}}},
		"tok-b":    {"sess-b", map[string][]string{"wiki": {"wiki_search", "wiki_read"}}},
		"tok-none": {"sess-c", map[string][]string{"wiki": {}}},
	}
	pool := NewPool(newFakeStarter().start, PoolOptions{})
	base, _ := serveCatalog(t, gatewayCatalog(), pool, DaemonOptions{Tokens: toks})

	for token, want := range map[string]string{
		"tok-a":    "wiki_search",
		"tok-b":    "wiki_read,wiki_search",
		"tok-none": "",
	} {
		got := strings.Join(toolNames(t, bearerClient(t, endpointURL(base, "wiki"), token)), ",")
		if got != want {
			t.Errorf("%s lists %q, want %q", token, got, want)
		}
	}
}

func TestDaemon_CallOutsideTokenListRefused(t *testing.T) {
	toks := fakeTokens{"tok-a": {"sess-a", map[string][]string{"wiki": {"wiki_search"}}}}
	fs := newFakeStarter()
	pinSeen := 0
	pool := NewPool(fs.start, PoolOptions{})
	ln := serveWith(t, gatewayCatalog(), pool, DaemonOptions{Tokens: toks},
		WithCallGuard(func(context.Context, string, string) error { pinSeen++; return nil }))
	c := bearerClient(t, endpointURL(ln, "wiki"), "tok-a")

	res, err := c.CallTool(context.Background(), req("wiki_read"))
	if err != nil {
		t.Fatalf("want a tool error result, got transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "wiki_read") {
		t.Fatalf("call outside the token's list was not refused with a tool error naming it: %+v", res)
	}
	if fs.count("wiki") != 0 {
		t.Fatalf("backend started %d times; the call must be refused before it", fs.count("wiki"))
	}
	if pinSeen != 0 {
		t.Fatalf("pin guard ran %d times; the token list is checked first", pinSeen)
	}

	// Its own tool still goes through the pin guard and the backend.
	if r, err := c.CallTool(context.Background(), req("wiki_search")); err != nil || r.IsError {
		t.Fatalf("allowed call refused: %v %+v", err, r)
	}
	if pinSeen != 1 || fs.count("wiki") != 1 {
		t.Fatalf("allowed call: pin guard %d, backend starts %d, want 1 and 1", pinSeen, fs.count("wiki"))
	}
}

// serveWith is serveCatalog with server options (a call guard).
func serveWith(t *testing.T, cat *Catalog, pool *Pool, opt DaemonOptions, sopts ...ServerOption) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServers(cat, pool, sopts...), pool, opt) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done: //nolint:errcheck // Serve's exit error is irrelevant here; only that it returned
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after cancel")
		}
	})
	return "http://" + ln.Addr().String()
}

func TestDaemon_TokenNeverLogged(t *testing.T) {
	const tok = "secret-token-0123456789abcdef"
	toks := fakeTokens{tok: {"sess-a", map[string][]string{"wiki": {"wiki_search"}}}}
	var logged, bodies strings.Builder
	var mu sync.Mutex
	logw := writerFunc(func(p []byte) { mu.Lock(); logged.Write(p); mu.Unlock() })
	prev := log.Writer()
	log.SetOutput(logw)
	defer log.SetOutput(prev)

	pool := NewPool(newFakeStarter().start, PoolOptions{})
	rej := WithRejectLog(func(server, tool string, err error) {
		mu.Lock()
		fmt.Fprintf(&logged, "reject %s/%s: %v\n", server, tool, err)
		mu.Unlock()
	})
	base := serveWith(t, gatewayCatalog(), pool, DaemonOptions{Tokens: toks, Log: logw}, rej)

	// Every kind of refusal and a refused call, then read what came back.
	for _, p := range []struct{ path, auth string }{
		{EndpointPath("slack"), "Bearer " + tok},
		{"/status", "Bearer " + tok},
		{EndpointPath("wiki"), "Bearer " + tok + "x"},
	} {
		r, _ := http.NewRequest("POST", base+p.path, strings.NewReader("{}"))
		r.Header.Set("Authorization", p.auth)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		bodies.Write(b)
	}
	c := bearerClient(t, endpointURL(base, "wiki"), tok)
	res, err := c.CallTool(context.Background(), req("wiki_read"))
	if err != nil || !res.IsError {
		t.Fatalf("setup: want a refused call, got %v %+v", err, res)
	}
	bodies.WriteString(resultText(t, res))

	mu.Lock()
	defer mu.Unlock()
	if logged.Len() == 0 {
		t.Fatal("setup: nothing was logged, the assertion would be vacuous")
	}
	if strings.Contains(logged.String(), tok) {
		t.Fatalf("a token reached the log: %q", logged.String())
	}
	if strings.Contains(bodies.String(), tok) {
		t.Fatalf("a token was echoed to a client: %q", bodies.String())
	}
}

type writerFunc func(p []byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func rawMCP(t *testing.T, method, url, token, sid, body string) int {
	t.Helper()
	r, _ := http.NewRequest(method, url, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

const listBody = `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`

func TestDaemon_SessionFromOtherTokenRefused(t *testing.T) {
	allow := map[string][]string{"wiki": {"wiki_search", "wiki_read"}}
	toks := fakeTokens{"tok-a": {"sess-a", allow}, "tok-b": {"sess-b", allow}}
	fs := newFakeStarter()
	base, _ := serveCatalog(t, gatewayCatalog(), NewPool(fs.start, PoolOptions{}), DaemonOptions{Tokens: toks})
	url := endpointURL(base, "wiki")
	b := bearerClient(t, url, "tok-b")
	sid := b.GetSessionId()
	if sid == "" {
		t.Fatal("setup: B has no MCP session id")
	}
	call := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"wiki_search"}}`
	if got := rawMCP(t, "POST", url, "tok-a", sid, call); got != http.StatusNotFound {
		t.Fatalf("A calling on B session: got %d, want 404", got)
	}
	if got := rawMCP(t, "DELETE", url, "tok-a", sid, ""); got != http.StatusNotFound {
		t.Fatalf("A deleting B session: got %d, want 404", got)
	}
	if fs.count("wiki") != 0 {
		t.Fatalf("a refused request reached the backend (%d starts)", fs.count("wiki"))
	}
	if got := toolNames(t, b); len(got) != 2 {
		t.Fatalf("B's session broke after A's attempts: %v", got)
	}
	mustCall(t, b, "wiki_search")
}

func TestDaemon_SameTokenKeepsItsSession(t *testing.T) {
	toks := fakeTokens{"tok-b": {"sess-b", map[string][]string{"wiki": {"wiki_search"}}}}
	base, _ := serveCatalog(t, gatewayCatalog(), NewPool(newFakeStarter().start, PoolOptions{}), DaemonOptions{Tokens: toks})
	url := endpointURL(base, "wiki")
	b := bearerClient(t, url, "tok-b")
	sid := b.GetSessionId()
	if got := rawMCP(t, "POST", url, "tok-b", sid, listBody); got != http.StatusOK {
		t.Fatalf("owner on its own session: got %d, want 200", got)
	}
	if got := rawMCP(t, "POST", url, "tok-b", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+mcp.LATEST_PROTOCOL_VERSION+`","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`); got != http.StatusOK {
		t.Fatalf("initialize without a session id: got %d, want 200", got)
	}
	if got := rawMCP(t, "DELETE", url, "tok-b", sid, ""); got >= 400 {
		t.Fatalf("owner deleting its session: got %d", got)
	}
}

// A revoked token's sessions are unusable by anyone, even a re-issued token.
func TestDaemon_RevokedTokensSessionUnusable(t *testing.T) {
	allow := map[string][]string{"wiki": {"wiki_search"}}
	toks := &mutableTokens{m: fakeTokens{"tok-b": {"sess-b", allow}}}
	base, _ := serveCatalog(t, gatewayCatalog(), NewPool(newFakeStarter().start, PoolOptions{}), DaemonOptions{Tokens: toks})
	url := endpointURL(base, "wiki")
	sid := bearerClient(t, url, "tok-b").GetSessionId()
	toks.set(fakeTokens{"tok-b2": {"sess-b", allow}}) // revoke and re-issue
	if got := rawMCP(t, "POST", url, "tok-b2", sid, listBody); got != http.StatusNotFound {
		t.Fatalf("new token on the revoked token's session: got %d, want 404", got)
	}
	if got := rawMCP(t, "POST", url, "tok-b", sid, listBody); got != http.StatusUnauthorized {
		t.Fatalf("revoked token: got %d, want 401", got)
	}
}

type mutableTokens struct {
	mu sync.Mutex
	m  fakeTokens
}

func (t *mutableTokens) set(m fakeTokens) { t.mu.Lock(); t.m = m; t.mu.Unlock() }
func (t *mutableTokens) Lookup(tok string) (string, map[string][]string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m.Lookup(tok)
}

func TestDaemon_AdminSessionNotUsableByIssuedToken(t *testing.T) {
	toks := fakeTokens{"tok-a": {"sess-a", map[string][]string{"wiki": {"wiki_search"}}}}
	fs := newFakeStarter()
	base, _ := serveCatalog(t, gatewayCatalog(), NewPool(fs.start, PoolOptions{}), DaemonOptions{Token: "admin", Tokens: toks})
	url := endpointURL(base, "wiki")
	sid := bearerClient(t, url, "admin").GetSessionId()
	if sid == "" {
		t.Fatal("setup: admin has no MCP session id")
	}
	call := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"wiki_search"}}`
	if got := rawMCP(t, "POST", url, "tok-a", sid, call); got != http.StatusNotFound {
		t.Fatalf("issued token on an admin session: got %d, want 404", got)
	}
	if got := rawMCP(t, "DELETE", url, "tok-a", sid, ""); got != http.StatusNotFound {
		t.Fatalf("issued token deleting an admin session: got %d, want 404", got)
	}
	if fs.count("wiki") != 0 {
		t.Fatalf("a refused request reached the backend (%d starts)", fs.count("wiki"))
	}
}

// guardHarness is tokenGuard over a stub that hands out session "s1" to an
// initialize and answers everything else 200.
func guardHarness(t *testing.T) (*sessionBinds, func(method, sid string) int, *int) {
	t.Helper()
	toks := fakeTokens{"tok-a": {"sess-a", map[string][]string{"wiki": {"x"}}}}
	reached := 0
	binds := newSessionBinds()
	h := tokenGuard("", toks, binds, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		if r.Header.Get("Mcp-Session-Id") == "" {
			w.Header().Set("Mcp-Session-Id", "s1")
		}
		w.WriteHeader(http.StatusOK)
	}))
	return binds, func(method, sid string) int {
		r := httptest.NewRequest(method, EndpointPath("wiki"), nil)
		r.Header.Set("Authorization", "Bearer tok-a")
		if sid != "" {
			r.Header.Set("Mcp-Session-Id", sid)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}, &reached
}

func TestDaemon_UnrecordedSessionIdRefused(t *testing.T) {
	binds, do, reached := guardHarness(t)
	if got := do("POST", "made-up"); got != http.StatusNotFound {
		t.Fatalf("made-up session id: got %d, want 404", got)
	}
	if *reached != 0 || binds.len() != 0 {
		t.Fatalf("a made-up id was forwarded (%d) or stored (%d)", *reached, binds.len())
	}
}

func TestDaemon_OwnerDeletePrunesBinding(t *testing.T) {
	binds, do, _ := guardHarness(t)
	if do("POST", "") != http.StatusOK || binds.len() != 1 {
		t.Fatalf("initialize must record one binding, have %d", binds.len())
	}
	if got := do("POST", "s1"); got != http.StatusOK {
		t.Fatalf("owner on its session: %d", got)
	}
	if got := do("DELETE", "s1"); got != http.StatusOK || binds.len() != 0 {
		t.Fatalf("owner DELETE: status %d, bindings %d, want 200 and 0", got, binds.len())
	}
	if got := do("POST", "s1"); got != http.StatusNotFound {
		t.Fatalf("ended session: got %d, want 404", got)
	}
}
