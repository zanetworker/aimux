package toolgateway_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// A backend that changes a tool's schema after it was pinned keeps its name
// but loses its callers: this is the gap name-only filtering left open.
func TestBuildReadonly_RefusesCallAfterBackendChangesSchema(t *testing.T) {
	backend := server.NewMCPServer("slack", "1")
	called := 0
	handler := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called++
		return mcp.NewToolResultText("ok"), nil
	}
	backend.AddTool(mcp.NewTool("search_messages", mcp.WithString("q")), handler)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	defer ts.Close()

	cfg := mcpproxy.ReadonlyConfig{
		HTTP:     map[string]mcpproxy.HTTPServerConfig{"slack": {URL: ts.URL + "/"}},
		WikiRoot: t.TempDir(),
		Allow:    mcpproxy.Allowlist{"slack": {"search_messages"}},
	}
	ctx := context.Background()

	// Pin what the backend serves today, as `aimux browser setup` does.
	cat, errs := mcpproxy.ReadonlyCatalog(ctx, cfg)
	if len(errs) != 0 {
		t.Fatalf("catalog: %v", errs)
	}
	pins := toolgateway.Pins{"slack": {}}
	for _, raw := range cat.Allowed(cfg.Allow).Servers["slack"].Tools {
		pin, err := toolgateway.PinTool(raw)
		if err != nil {
			t.Fatal(err)
		}
		pins["slack"]["search_messages"] = pin
	}

	servers, pool, errs, err := mcpproxy.BuildReadonly(ctx, cfg, pins, nil)
	if err != nil || errs["slack"] != nil {
		t.Fatalf("build: %v %v", err, errs)
	}
	defer pool.Close()
	c, err := client.NewInProcessClient(servers["slack"])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := c.Start(cctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(cctx, init); err != nil {
		t.Fatal(err)
	}

	// The backend swaps the schema under the same name before the first call.
	backend.DeleteTools("search_messages")
	backend.AddTool(mcp.NewTool("search_messages", mcp.WithNumber("q")), handler)

	res, err := c.CallTool(cctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "search_messages"}})
	if err != nil {
		t.Fatalf("want a tool error result, got transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("call to a changed tool succeeded: %+v", res)
	}
	if text := res.Content[0].(mcp.TextContent).Text; !strings.Contains(text, "search_messages") || !strings.Contains(text, "changed") {
		t.Fatalf("refusal text = %q", text)
	}
	if called != 0 {
		t.Fatalf("backend handled %d calls; the guard must refuse first", called)
	}
}
