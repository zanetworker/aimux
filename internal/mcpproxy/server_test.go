package mcpproxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func testCatalog() *Catalog {
	c := &Catalog{}
	at := time.Now()
	c.Set("slack", []json.RawMessage{
		json.RawMessage(`{"name":"post_message","description":"Post","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}`),
	}, at)
	c.Set("notebooklm-mcp", []json.RawMessage{
		json.RawMessage(`{"name":"` + strings.Repeat("very_long_tool_name_", 3) + `x","inputSchema":{"type":"object"}}`),
	}, at)
	c.Set("bad", []json.RawMessage{json.RawMessage(`{"inputSchema":{}}`)}, at)
	return c
}

func connect(t *testing.T, cat *Catalog, pool *Pool) *client.Client {
	t.Helper()
	srv, skipped := NewServer(cat, pool, "mux")
	if len(skipped) != 1 || !strings.Contains(skipped[0], "bad") {
		t.Fatalf("skipped = %v, want the one malformed tool from server bad", skipped)
	}
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

func TestServer_ListsCatalogToolsWithoutStartingBackends(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	c := connect(t, testCatalog(), pool)

	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]mcp.Tool{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool
		if full := "mcp__mux__" + tool.Name; len(full) > 64 {
			t.Fatalf("exposed name %q is %d chars as registered by Claude Code", full, len(full))
		}
	}
	slack, ok := names["slack__post_message"]
	if !ok || len(names) != 2 {
		t.Fatalf("got tools %v, want slack__post_message and one shortened notebooklm tool", keys(names))
	}
	if !strings.HasPrefix(slack.Description, "[slack]") {
		t.Fatalf("description %q should name the server", slack.Description)
	}
	if fs.count("slack")+fs.count("notebooklm-mcp") != 0 {
		t.Fatal("listing tools started a backend")
	}
}

func TestServer_CallRoutesToBackendWithOriginalName(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	c := connect(t, testCatalog(), pool)

	req := mcp.CallToolRequest{}
	req.Params.Name = "slack__post_message"
	req.Params.Arguments = map[string]any{"text": "hello"}
	res, err := c.CallTool(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("call failed: %v %+v", err, res)
	}
	got := fs.backends[0].lastReq
	if got.Params.Name != "post_message" {
		t.Fatalf("backend got tool %q, want post_message", got.Params.Name)
	}
	if args, _ := got.Params.Arguments.(map[string]any); args["text"] != "hello" {
		t.Fatalf("backend got arguments %v", got.Params.Arguments)
	}
}

func TestServer_ShortenedNameStillRoutes(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	c := connect(t, testCatalog(), pool)

	res, _ := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	var short string
	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "notebooklm-mcp__") {
			short = tool.Name
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = short
	if r, err := c.CallTool(context.Background(), req); err != nil || r.IsError {
		t.Fatalf("calling shortened %q failed: %v %+v", short, err, r)
	}
	if want := strings.Repeat("very_long_tool_name_", 3) + "x"; fs.backends[0].lastReq.Params.Name != want {
		t.Fatalf("backend got %q, want the original long name", fs.backends[0].lastReq.Params.Name)
	}
}

func keys(m map[string]mcp.Tool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
