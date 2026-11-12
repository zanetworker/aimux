package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// longTool is longer than any exposed-name limit the old combined endpoint
// had, so a test can see it is served untruncated.
var longTool = strings.Repeat("very_long_tool_name_", 3) + "x"

func testCatalog() *Catalog {
	c := &Catalog{}
	at := time.Now()
	c.Set("slack", []json.RawMessage{
		json.RawMessage(`{"name":"post_message","description":"Post","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}`),
	}, at)
	c.Set("notebooklm-mcp", []json.RawMessage{
		json.RawMessage(`{"name":"` + longTool + `","inputSchema":{"type":"object"}}`),
	}, at)
	c.Set("bad", []json.RawMessage{json.RawMessage(`{"inputSchema":{}}`)}, at)
	return c
}

func connectTo(t *testing.T, srv *server.MCPServer) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(srv)
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

func toolNames(t *testing.T, c *client.Client) []string {
	t.Helper()
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func TestNewServers_OneServerPerCatalogServerWithOriginalToolNames(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	srvs := NewServers(testCatalog(), pool)

	if len(srvs) != 3 || srvs["slack"] == nil || srvs["notebooklm-mcp"] == nil || srvs["bad"] == nil {
		t.Fatalf("got servers %v, want slack, notebooklm-mcp and bad", srvs)
	}
	slack := connectTo(t, srvs["slack"])
	if got := toolNames(t, slack); len(got) != 1 || got[0] != "post_message" {
		t.Fatalf("slack lists %v, want exactly [post_message]", got)
	}
	res, _ := slack.ListTools(context.Background(), mcp.ListToolsRequest{})
	if d := res.Tools[0].Description; d != "Post" {
		t.Fatalf("description %q, want the backend's own description", d)
	}
	if got := toolNames(t, connectTo(t, srvs["notebooklm-mcp"])); len(got) != 1 || got[0] != longTool {
		t.Fatalf("notebooklm-mcp lists %v, want the long name untruncated", got)
	}
	if got := toolNames(t, connectTo(t, srvs["bad"])); len(got) != 0 {
		t.Fatalf("bad lists %v, want its malformed tool skipped", got)
	}
	if fs.count("slack")+fs.count("notebooklm-mcp") != 0 {
		t.Fatal("listing tools started a backend")
	}
}

func TestNewServers_CallReachesThatServersBackendUnderTheOriginalName(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	srvs := NewServers(testCatalog(), pool)
	c := connectTo(t, srvs["slack"])

	r := req("post_message")
	r.Params.Arguments = map[string]any{"text": "hello"}
	res, err := c.CallTool(context.Background(), r)
	if err != nil || res.IsError {
		t.Fatalf("call failed: %v %+v", err, res)
	}
	if fs.count("slack") != 1 || fs.count("notebooklm-mcp") != 0 {
		t.Fatalf("starts: slack %d, notebooklm-mcp %d; want only slack", fs.count("slack"), fs.count("notebooklm-mcp"))
	}
	got := fs.backends[0].lastReq
	if got.Params.Name != "post_message" {
		t.Fatalf("backend got tool %q, want post_message", got.Params.Name)
	}
	if args, _ := got.Params.Arguments.(map[string]any); args["text"] != "hello" {
		t.Fatalf("backend got arguments %v", got.Params.Arguments)
	}
}

func TestNewServers_EmptyCatalogGivesNoServers(t *testing.T) {
	pool := NewPool(newFakeStarter().start, PoolOptions{})
	defer pool.Close()
	if srvs := NewServers(&Catalog{}, pool); len(srvs) != 0 {
		t.Fatalf("got %d servers from an empty catalog", len(srvs))
	}
}

func TestCallGuard_RefusesBeforeBackend(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	var guarded [][2]string
	var rejected []string
	guard := func(_ context.Context, server, tool string) error {
		guarded = append(guarded, [2]string{server, tool})
		return errors.New("schema drifted")
	}
	srvs := NewServers(testCatalog(), pool, WithCallGuard(guard),
		WithRejectLog(func(server, tool string, err error) { rejected = append(rejected, server+"/"+tool) }))
	c := connectTo(t, srvs["slack"])

	res, err := c.CallTool(context.Background(), req("post_message"))
	if err != nil {
		t.Fatalf("a guard refusal must be a tool result, got transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "post_message") || !strings.Contains(text, "schema drifted") {
		t.Fatalf("refusal text %q must name the tool and the reason", text)
	}
	if fs.count("slack") != 0 {
		t.Fatalf("backend started %d times; the guard must refuse before the pool", fs.count("slack"))
	}
	if len(guarded) != 1 || guarded[0] != [2]string{"slack", "post_message"} {
		t.Fatalf("guard saw %v", guarded)
	}
	if len(rejected) != 1 || rejected[0] != "slack/post_message" {
		t.Fatalf("reject log saw %v", rejected)
	}
}

func TestCallGuard_PassesWhenGuardAllows(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	srvs := NewServers(testCatalog(), pool, WithCallGuard(func(context.Context, string, string) error { return nil }))
	res, err := connectTo(t, srvs["slack"]).CallTool(context.Background(), req("post_message"))
	if err != nil || res.IsError {
		t.Fatalf("call refused: %v %+v", err, res)
	}
	if fs.count("slack") != 1 {
		t.Fatalf("backend starts = %d, want 1", fs.count("slack"))
	}
}
