package mcpproxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
