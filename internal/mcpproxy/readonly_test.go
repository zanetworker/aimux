package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/toolreg"
)

func tempWiki(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello wingman\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func roToolNames(t *testing.T, srv *server.MCPServer) []string {
	t.Helper()
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

func slackFake(t *testing.T) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("slack", "1")
	h := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	s.AddTool(mcp.NewTool("search_messages"), h)
	s.AddTool(mcp.NewTool("post_message"), h)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	return ts
}

func downURL(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(nil)
	u := ts.URL
	ts.Close()
	return u
}

func testReadonlyConfig(t *testing.T, slackURL string) ReadonlyConfig {
	return ReadonlyConfig{
		HTTP: map[string]HTTPServerConfig{
			"slack": {URL: slackURL},
			"jira":  {URL: downURL(t)},
		},
		Notes: map[string]string{"wiki": tempWiki(t)},
		Allow: Allowlist{
			"wiki":  {"wiki_search", "wiki_read"},
			"slack": {"search_messages"},
			"jira":  {"search_issues_jql"},
		},
	}
}

func TestBuildReadonlyExposesOnlyAllowlist(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var keys []string
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"slack", "wiki"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("servers = %v, want %v (errs %v)", keys, want, errs)
	}
	if got := roToolNames(t, servers["slack"]); !reflect.DeepEqual(got, []string{"search_messages"}) {
		t.Errorf("slack tools = %v", got)
	}
	if got := roToolNames(t, servers["wiki"]); !reflect.DeepEqual(got, []string{"wiki_read", "wiki_search"}) {
		t.Errorf("wiki tools = %v", got)
	}
}

func TestBuildReadonlyBackendDown(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if errs["jira"] == nil {
		t.Errorf("jira missing from error map: %v", errs)
	}
	if _, ok := servers["jira"]; ok {
		t.Error("down backend must not be served")
	}
	if servers["slack"] == nil || servers["wiki"] == nil {
		t.Error("live backends must still be served")
	}
	if errs["slack"] != nil || errs["wiki"] != nil {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestBuildReadonlyMissingWikiRoot(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	cfg.Notes["wiki"] = filepath.Join(t.TempDir(), "nope")
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if errs["wiki"] == nil {
		t.Errorf("missing wiki root not reported: %v", errs)
	}
	if _, ok := servers["wiki"]; ok {
		t.Error("wiki must not be served without a root")
	}
}

func TestBuildReadonlyRejectsWrite(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	type rej struct{ server, tool string }
	got := make(chan rej, 4)
	servers, pool, _, err := BuildReadonly(context.Background(), cfg, passPins{}, func(s, tl string, _ error) { got <- rej{s, tl} })
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	c, err := client.NewInProcessClient(servers["slack"])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	call := mcp.CallToolRequest{}
	call.Params.Name = "post_message"
	if res, err := c.CallTool(ctx, call); err == nil && (res == nil || !res.IsError) {
		t.Fatalf("post_message succeeded: %+v", res)
	}
	select {
	case r := <-got:
		if r != (rej{"slack", "post_message"}) {
			t.Errorf("reject = %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rejectLog not called")
	}
	// An allowlisted call still works.
	call.Params.Name = "search_messages"
	if res, err := c.CallTool(ctx, call); err != nil || res.IsError {
		t.Errorf("search_messages: %v %+v", err, res)
	}
}

func TestReadonlyConfigFrom(t *testing.T) {
	reg := toolreg.Registry{
		Version: 1,
		Sources: map[string]toolreg.Source{
			"jira":  {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
			"idle":  {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:9/mcp"},
			"notes": {Type: toolreg.TypeLocalNotes, Root: "/n"},
		},
		Sets: map[string]toolreg.Set{
			"jira:readonly":  {Source: "jira", Tools: []string{"search", "get_issue"}},
			"jira:triage":    {Source: "jira", Tools: []string{"get_issue", "get_comments"}},
			"notes:readonly": {Source: "notes", Tools: []string{"wiki_read"}},
		},
	}
	cfg := ReadonlyConfigFrom(reg)
	wantHTTP := map[string]HTTPServerConfig{"jira": {URL: "http://127.0.0.1:8001/mcp"}, "idle": {URL: "http://127.0.0.1:9/mcp"}}
	if !reflect.DeepEqual(cfg.HTTP, wantHTTP) {
		t.Errorf("http = %+v", cfg.HTTP)
	}
	if !reflect.DeepEqual(cfg.Notes, map[string]string{"notes": "/n"}) {
		t.Errorf("notes = %+v", cfg.Notes)
	}
	// Two sets on one source allow their union, each tool once.
	want := Allowlist{"jira": {"get_comments", "get_issue", "search"}, "notes": {"wiki_read"}}
	if !reflect.DeepEqual(cfg.Allow, want) {
		t.Errorf("allow = %v, want %v", cfg.Allow, want)
	}
}

// With no notes source configured there is no wiki backend at all: nothing
// is served from a default folder.
func TestBuildReadonly_NoNotesSourceNoWiki(t *testing.T) {
	cfg := ReadonlyConfigFrom(toolreg.Default())
	cfg.HTTP = map[string]HTTPServerConfig{"slack": {URL: slackFake(t).URL + "/"}}
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, ok := servers["wiki"]; ok {
		t.Error("a wiki server is served with no notes source")
	}
	if _, ok := errs["wiki"]; ok {
		t.Errorf("wiki reported without a notes source: %v", errs)
	}
	if servers["slack"] == nil {
		t.Errorf("slack not served: %v", errs)
	}
}

// A backend down at the first build is rebuilt later on the same pool, and
// only the backends asked for are read.
func TestBuildReadonlyBackendsRebuildsDownBackend(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	cfg.HTTP["jira"] = HTTPServerConfig{URL: downURL(t)}
	_, pool, errs, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if errs["jira"] == nil {
		t.Fatal("setup: jira should be down")
	}
	jira := slackFake(t) // jira comes up (serving a tool it allows below)
	cfg.HTTP["jira"] = HTTPServerConfig{URL: jira.URL + "/"}
	cfg.Allow["jira"] = []string{"search_messages"}
	servers, errs := BuildReadonlyBackends(context.Background(), cfg, []string{"jira"}, passPins{}, pool, nil)
	if len(errs) != 0 || len(servers) != 1 || servers["jira"] == nil {
		t.Fatalf("rebuild: servers=%v errs=%v", servers, errs)
	}
	if got := roToolNames(t, servers["jira"]); !reflect.DeepEqual(got, []string{"search_messages"}) {
		t.Fatalf("jira tools = %v", got)
	}
	_, errs = BuildReadonlyBackends(context.Background(), cfg, []string{"wiki", "nope"}, passPins{}, pool, nil)
	if errs["nope"] == nil || errs["wiki"] != nil {
		t.Fatalf("errs = %v", errs)
	}
}

// passPins trusts every tool: it stands in for pins where a test is not
// about pinning. (The real pins live in internal/toolgateway, which imports
// this package.)
type passPins struct{}

func (passPins) Filter(c *Catalog) *Catalog { return c }
func (passPins) Guard(func(context.Context, string) ([]json.RawMessage, error)) func(context.Context, string, string) error {
	return func(context.Context, string, string) error { return nil }
}
func (passPins) Changed(string) {}

// schemaPins pins a tool to its inputSchema text as the backend sent it.
type schemaPins map[string]string // "server/tool" -> inputSchema JSON

func (p schemaPins) Filter(c *Catalog) *Catalog {
	out := &Catalog{Servers: map[string]CatalogEntry{}}
	for s, e := range c.Servers {
		kept := CatalogEntry{FetchedAt: e.FetchedAt}
		for _, raw := range e.Tools {
			var t struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"inputSchema"`
			}
			if json.Unmarshal(raw, &t) == nil && p[s+"/"+t.Name] == string(t.Schema) {
				kept.Tools = append(kept.Tools, raw)
			}
		}
		if len(kept.Tools) > 0 {
			out.Servers[s] = kept
		}
	}
	return out
}

func (p schemaPins) Guard(func(context.Context, string) ([]json.RawMessage, error)) func(context.Context, string, string) error {
	return func(context.Context, string, string) error { return nil }
}
func (schemaPins) Changed(string) {}

func TestBuildReadonly_HidesChangedTool(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	cfg.Allow = Allowlist{"slack": {"search_messages"}}
	// The backend's real schema for search_messages, read once to pin it.
	_, pool, _, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pool.Lease(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	raws, err := b.ListToolsRaw(context.Background())
	_ = b.Close()
	pool.Close()
	if err != nil || len(raws) == 0 {
		t.Fatalf("list: %v %d", err, len(raws))
	}
	var live struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"inputSchema"`
	}
	found := false
	for _, r := range raws {
		if json.Unmarshal(r, &live) == nil && live.Name == "search_messages" {
			found = true
			break
		}
	}
	if !found || len(live.Schema) == 0 {
		t.Fatalf("setup: search_messages (with a schema) is not among the backend's %d live tools", len(raws))
	}

	same := schemaPins{"slack/search_messages": string(live.Schema)}
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, same, nil)
	if err != nil || errs["slack"] != nil {
		t.Fatalf("matching pin: build: %v %v", err, errs)
	}
	defer pool.Close()
	if got := roToolNames(t, servers["slack"]); !reflect.DeepEqual(got, []string{"search_messages"}) {
		t.Fatalf("matching pin: tools = %v", got)
	}

	drifted := schemaPins{"slack/search_messages": `{"type":"object","properties":{"other":{"type":"number"}}}`}
	servers, pool2, errs, err := BuildReadonly(context.Background(), cfg, drifted, nil)
	if err != nil || errs["slack"] != nil {
		t.Fatalf("drifted pin: build: %v %v", err, errs)
	}
	defer pool2.Close()
	if srv, ok := servers["slack"]; ok {
		t.Fatalf("slack exposed %v although its schema no longer matches the pin", roToolNames(t, srv))
	}
}

func TestReadonlyCatalog_ListsLiveBackendsAndReportsDownOnes(t *testing.T) {
	cfg := testReadonlyConfig(t, slackFake(t).URL+"/")
	cat, errs := ReadonlyCatalog(context.Background(), cfg)
	if errs["jira"] == nil || len(errs) != 1 {
		t.Fatalf("errs = %v, want only jira", errs)
	}
	if _, ok := cat.Servers["jira"]; ok {
		t.Error("down backend is in the catalog")
	}
	var names []string
	for _, raw := range cat.Servers["slack"].Tools {
		tl, err := DecodeTool(raw)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	// The live listing is unfiltered: the allowlist is the caller's to apply.
	if !reflect.DeepEqual(names, []string{"post_message", "search_messages"}) {
		t.Fatalf("slack tools = %v", names)
	}
	if len(cat.Servers["wiki"].Tools) == 0 {
		t.Error("wiki tools missing")
	}
}

// cachedPins pins a tool to its inputSchema like schemaPins, and its guard
// keeps each server's listing until Changed drops it, as the real verifier
// does within its 60 s MaxAge. Changed reports each server on changed.
type cachedPins struct {
	schemaPins
	mu      sync.Mutex
	cache   map[string][]json.RawMessage
	changed chan string
}

func newCachedPins(p schemaPins) *cachedPins {
	return &cachedPins{schemaPins: p, cache: map[string][]json.RawMessage{}, changed: make(chan string, 64)}
}

func (p *cachedPins) Guard(list func(context.Context, string) ([]json.RawMessage, error)) func(context.Context, string, string) error {
	return func(ctx context.Context, server, tool string) error {
		p.mu.Lock()
		raws, ok := p.cache[server]
		p.mu.Unlock()
		if !ok {
			var err error
			if raws, err = list(ctx, server); err != nil {
				return err
			}
			p.mu.Lock()
			p.cache[server] = raws
			p.mu.Unlock()
		}
		for _, raw := range raws {
			var t struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"inputSchema"`
			}
			if json.Unmarshal(raw, &t) == nil && t.Name == tool {
				if p.schemaPins[server+"/"+tool] != string(t.Schema) {
					return fmt.Errorf("%s/%s changed since it was pinned", server, tool)
				}
				return nil
			}
		}
		return fmt.Errorf("%s/%s changed since it was pinned (no longer listed)", server, tool)
	}
}

func (p *cachedPins) Changed(server string) {
	p.mu.Lock()
	delete(p.cache, server)
	p.mu.Unlock()
	select {
	case p.changed <- server:
	default:
	}
}

// swappableSlack is a Slack fixture whose search_messages schema the test
// swaps (setSchema), returning the MCP server so it can notify.
func swappableSlack(t *testing.T) (*server.MCPServer, string, func(prop string)) {
	t.Helper()
	s := server.NewMCPServer("slack", "1", server.WithToolCapabilities(true))
	h := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	set := func(prop string) { s.AddTool(mcp.NewTool("search_messages", mcp.WithString(prop)), h) }
	set("query")
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	return s, ts.URL + "/", set
}

// liveSchema is search_messages' inputSchema as slack serves it now.
func liveSchema(t *testing.T, url string) string {
	t.Helper()
	cat, errs := ReadonlyCatalog(context.Background(), ReadonlyConfig{HTTP: map[string]HTTPServerConfig{"slack": {URL: url}}})
	if errs["slack"] != nil {
		t.Fatal(errs["slack"])
	}
	for _, raw := range cat.Servers["slack"].Tools {
		var tl struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"inputSchema"`
		}
		if json.Unmarshal(raw, &tl) == nil && tl.Name == "search_messages" {
			return string(tl.Schema)
		}
	}
	t.Fatal("search_messages not listed")
	return ""
}

func roCall(t *testing.T, srv *server.MCPServer, tool string) *mcp.CallToolResult {
	t.Helper()
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	call := mcp.CallToolRequest{}
	call.Params.Name = tool
	res, err := c.CallTool(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// awaitChanged sends list_changed from s until pins hears it for slack,
// for up to 2 s: the backend's listening stream may still be opening.
func awaitChanged(t *testing.T, s *server.MCPServer, pins *cachedPins) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	s.SendNotificationToAllClients(mcp.MethodNotificationToolsListChanged, nil)
	for {
		select {
		case got := <-pins.changed:
			if got != "slack" {
				t.Fatalf("Changed(%q), want slack", got)
			}
			return
		case <-tick.C:
			s.SendNotificationToAllClients(mcp.MethodNotificationToolsListChanged, nil)
		case <-deadline:
			t.Fatal("pins never heard tools/list_changed")
		}
	}
}

func listChangedRig(t *testing.T) (*server.MCPServer, func(string), map[string]*server.MCPServer, *Pool, *cachedPins) {
	t.Helper()
	s, url, set := swappableSlack(t)
	cfg := ReadonlyConfig{HTTP: map[string]HTTPServerConfig{"slack": {URL: url}}, Allow: Allowlist{"slack": {"search_messages"}}}
	pins := newCachedPins(schemaPins{"slack/search_messages": liveSchema(t, url)})
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, pins, nil)
	if err != nil || errs["slack"] != nil || servers["slack"] == nil {
		t.Fatalf("build: %v %v %v", err, errs, servers)
	}
	t.Cleanup(pool.Close)
	if res := roCall(t, servers["slack"], "search_messages"); res.IsError {
		t.Fatalf("pinned call refused: %+v", res.Content)
	}
	return s, set, servers, pool, pins
}

func assertRefusedChanged(t *testing.T, srv *server.MCPServer, since time.Time) {
	t.Helper()
	res := roCall(t, srv, "search_messages")
	if !res.IsError {
		t.Fatal("call to the changed tool succeeded")
	}
	if tc, ok := res.Content[0].(mcp.TextContent); !ok || !strings.Contains(tc.Text, "changed") {
		t.Fatalf("refusal %+v does not say the tool changed", res.Content)
	}
	if d := time.Since(since); d >= 5*time.Second {
		t.Fatalf("refused after %v, want under 5 s", d)
	}
}

func TestBuildReadonly_ListChangedHidesChangedToolImmediately(t *testing.T) {
	s, set, servers, _, pins := listChangedRig(t)
	start := time.Now()
	set("other") // same name, new input schema
	awaitChanged(t, s, pins)
	assertRefusedChanged(t, servers["slack"], start)
}

func TestBuildReadonly_RestartedBackendStillNotifies(t *testing.T) {
	s, set, servers, pool, pins := listChangedRig(t)
	pool.CloseServer("slack")
	// The listing is still cached, so this call only starts a new backend.
	if res := roCall(t, servers["slack"], "search_messages"); res.IsError {
		t.Fatalf("call on the restarted backend refused: %+v", res.Content)
	}
	for len(pins.changed) > 0 { // anything the first backend heard
		<-pins.changed
	}
	start := time.Now()
	set("other")
	awaitChanged(t, s, pins)
	assertRefusedChanged(t, servers["slack"], start)
}

// notifyingBackend is a fakeBackend that reports tools/list_changed when
// the test calls notify. Registering a callback takes register, as a real
// client's handler setup may.
type notifyingBackend struct {
	*fakeBackend
	register time.Duration
	fnMu     sync.Mutex
	fns      []func()
}

func (b *notifyingBackend) OnToolsChanged(fn func()) {
	time.Sleep(b.register)
	b.fnMu.Lock()
	b.fns = append(b.fns, fn)
	b.fnMu.Unlock()
}

func (b *notifyingBackend) notify() {
	b.fnMu.Lock()
	fns := slices.Clone(b.fns)
	b.fnMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// namedPins trusts every tool and reports each Changed with its name.
type namedPins struct {
	passPins
	name string
	got  chan string
}

func (p namedPins) Changed(string) { p.got <- p.name }

func notifyingStarter(register time.Duration, started chan *notifyingBackend) Starter {
	return func(context.Context, string) (Backend, error) {
		b := &notifyingBackend{fakeBackend: &fakeBackend{}, register: register}
		started <- b
		return b, nil
	}
}

func heard(t *testing.T, got chan string) string {
	t.Helper()
	select {
	case n := <-got:
		return n
	case <-time.After(time.Second):
		return ""
	}
}

// A rebuild that finishes after a newer one was accepted (a relist
// superseded by a newer snapshot) must not take over notifications from
// the newer pins: only the caller that accepts a rebuild publishes pins.
func TestReadonly_SupersededRebuildKeepsNewerPins(t *testing.T) {
	got := make(chan string, 4)
	started := make(chan *notifyingBackend, 4)
	pool := readonlyPool(notifyingStarter(0, started))
	t.Cleanup(pool.Close)
	pool.PublishToolPins(namedPins{name: "newer", got: got})
	BuildReadonlyBackends(context.Background(), ReadonlyConfig{}, nil, namedPins{name: "stale", got: got}, pool, nil)
	lease, err := pool.Lease(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	b := <-started
	time.Sleep(50 * time.Millisecond) // let any after-start hook finish
	b.notify()
	if n := heard(t, got); n != "newer" {
		t.Fatalf("tools/list_changed reached %q pins, want the newer ones", n)
	}
}

// A backend's subscription is in place before anyone can use it, so a
// tools/list_changed sent right after start, before any call, is heard;
// a backend from a starter set later (SetStarter) is subscribed too.
func TestReadonly_SubscribedBeforeBackendIsUsable(t *testing.T) {
	got := make(chan string, 4)
	started := make(chan *notifyingBackend, 4)
	pool := readonlyPool(notifyingStarter(100*time.Millisecond, started))
	t.Cleanup(pool.Close)
	pool.PublishToolPins(namedPins{name: "pins", got: got})
	for i, server := range []string{"slack", "jira"} {
		if i == 1 {
			pool.SetStarter(notifyingStarter(100*time.Millisecond, started))
		}
		lease, err := pool.Lease(context.Background(), server)
		if err != nil {
			t.Fatal(err)
		}
		(<-started).notify() // at once: no call has been made
		if n := heard(t, got); n != "pins" {
			t.Fatalf("%s: tools/list_changed right after start was not heard", server)
		}
		_ = lease.Close()
	}
}

// A read-only build keeps each built backend running in its pool, so a
// change the server announces before any tool call is heard, and the
// idle reaper does not stop it (its notification stream would go with it).
func TestBuildReadonly_HearsListChangedBeforeAnyCallAndAfterReap(t *testing.T) {
	s, url, _ := swappableSlack(t)
	cfg := ReadonlyConfig{HTTP: map[string]HTTPServerConfig{"slack": {URL: url}}, Allow: Allowlist{"slack": {"search_messages"}}}
	pins := newCachedPins(schemaPins{"slack/search_messages": liveSchema(t, url)})
	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, pins, nil)
	if err != nil || errs["slack"] != nil || servers["slack"] == nil {
		t.Fatalf("build: %v %v %v", err, errs, servers)
	}
	t.Cleanup(pool.Close)
	awaitChanged(t, s, pins) // no call has been made
	if n := pool.Reap(0); n != 0 {
		t.Fatalf("reaper stopped %d read-only backends, want 0", n)
	}
	for len(pins.changed) > 0 {
		<-pins.changed
	}
	awaitChanged(t, s, pins)
}
