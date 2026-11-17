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
	"github.com/zanetworker/aimux/internal/toolreg"
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
		HTTP:  map[string]mcpproxy.HTTPServerConfig{"slack": {URL: ts.URL + "/"}},
		Allow: mcpproxy.Allowlist{"slack": {"search_messages"}},
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

	servers, pool, errs, err := mcpproxy.BuildReadonly(ctx, cfg, toolgateway.SnapshotPins{Pins: pins}, nil)
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

// One chain end to end: a real HTTP backend's tools/list_changed reaches
// the pool's backend, SnapshotPins.Changed marks the guard's Verifier stale,
// and the next call lists again and is refused with ErrToolChanged, long
// before the 60 s listing would have expired.
func TestBuildReadonly_GatewayPinsActOnListChanged(t *testing.T) {
	backend := server.NewMCPServer("slack", "1", server.WithToolCapabilities(true))
	handler := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	backend.AddTool(mcp.NewTool("search_messages", mcp.WithString("q")), handler)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	t.Cleanup(ts.Close)

	home := t.TempDir()
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  slack: {type: mcp-http, url: "`+ts.URL+`/"}
sets:
  slack:readonly: {source: slack, tools: [search_messages]}
`)
	store := toolgateway.NewStore(home)
	snap, _, err := store.Reload()
	if err != nil {
		t.Fatal(err)
	}
	cfg := mcpproxy.ReadonlyConfigFrom(snap.Registry())
	cat, errs := mcpproxy.ReadonlyCatalog(context.Background(), cfg)
	if errs["slack"] != nil {
		t.Fatal(errs["slack"])
	}
	pins := toolgateway.Pins{"slack": {}}
	for _, raw := range cat.Allowed(cfg.Allow).Servers["slack"].Tools {
		pin, err := toolgateway.PinTool(raw)
		if err != nil {
			t.Fatal(err)
		}
		pins["slack"]["search_messages"] = pin
	}
	pinStore := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	if err := pinStore.Save(hashOnly(pins)); err != nil {
		t.Fatal(err)
	}
	if snap, _, err = store.Reload(); err != nil {
		t.Fatal(err)
	}

	servers, pool, errs, err := mcpproxy.BuildReadonly(context.Background(), cfg, toolgateway.GatewayPins(snap), nil)
	if err != nil || errs["slack"] != nil || servers["slack"] == nil {
		t.Fatalf("build: %v %v", err, errs)
	}
	t.Cleanup(pool.Close)
	c, err := client.NewInProcessClient(servers["slack"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	call := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "search_messages"}}
	if res, err := c.CallTool(ctx, call); err != nil || res.IsError {
		t.Fatalf("pinned call: %v %+v", err, res)
	}

	start := time.Now()
	// Same name, new schema; AddTool sends tools/list_changed itself.
	backend.AddTool(mcp.NewTool("search_messages", mcp.WithNumber("q")), handler)
	for {
		res, err := c.CallTool(ctx, call)
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			if text := res.Content[0].(mcp.TextContent).Text; !strings.Contains(text, toolgateway.ErrToolChanged.Error()) {
				t.Fatalf("refusal %q, want ErrToolChanged", text)
			}
			break
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("call still allowed 3 s after the backend announced the change")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
