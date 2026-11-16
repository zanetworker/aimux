package mcpproxy_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// versionTools answers --version with its current version and runs every
// other command.
type versionTools struct {
	mu      sync.Mutex
	version string
	runs    int
}

func (v *versionTools) set(s string) { v.mu.Lock(); v.version = s; v.mu.Unlock() }

func (v *versionTools) Ensure(context.Context) error { return nil }

func (v *versionTools) RunTool(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(argv) == 2 && argv[1] == "--version" {
		return []byte("gh version " + v.version + " (2026-01-01)\n"), nil, 0, nil
	}
	v.runs++
	return []byte(`{"nameWithOwner":"cli/cli"}`), nil, 0, nil
}

// A gh upgrade changes x-aimux-binary, so every gh tool's pin no longer
// matches: hidden from a new build, and refused on a running one once the
// backend restarts on the new binary.
func TestRunnerBackend_BinaryVersionChangeIsDrift(t *testing.T) {
	pack, err := clipack.Builtin("gh")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := pack.Tools("2.83.1")
	if err != nil {
		t.Fatal(err)
	}
	var pin string
	for _, raw := range tools {
		if strings.Contains(string(raw), `"name":"gh_repo_view"`) {
			if pin, err = toolgateway.PinTool(raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	reg := toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"github": {Type: toolreg.TypeCLI, Pack: "gh"}},
		Sets: map[string]toolreg.Set{"github:readonly": {Source: "github", Tools: []string{"gh_repo_view"}}}}
	snap := toolreg.NewSnapshot(reg, toolreg.PinTable{"github": {"gh_repo_view": {Hash: pin, Source: "pack:gh"}}}, nil)
	run := &versionTools{version: "2.84.0"}
	cfg := mcpproxy.ReadonlyConfigFrom(snap.Registry())
	cfg.Tools = func(string) mcpproxy.ToolRunner { return run }

	// Built on the new binary: the pinned tool is not served.
	servers, pool, errs, err := mcpproxy.BuildReadonly(context.Background(), cfg, toolgateway.GatewayPins(snap), nil)
	if err != nil || errs["github"] != nil {
		t.Fatalf("build: %v %v", err, errs)
	}
	if listed := toolNames(t, servers["github"]); len(listed) != 0 {
		t.Fatalf("drifted tools served: %v", listed)
	}
	pool.Close()

	// Built on the pinned binary, then gh changes under the running gateway.
	run.set("2.83.1")
	pins := toolgateway.GatewayPins(snap)
	servers, pool, errs, err = mcpproxy.BuildReadonly(context.Background(), cfg, pins, nil)
	if err != nil || errs["github"] != nil {
		t.Fatalf("build: %v %v", err, errs)
	}
	defer pool.Close()
	c := startClient(t, servers["github"])
	call := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gh_repo_view", Arguments: map[string]any{"repo": "cli/cli"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if res, err := c.CallTool(ctx, call); err != nil || res.IsError {
		t.Fatalf("pinned call: %v %+v", err, res)
	}
	// A rebuild on the same running backend (no restart) relists, sees the
	// new binary, and hides the tool.
	run.set("2.84.0")
	rebuilt, errs := mcpproxy.BuildReadonlyBackends(context.Background(), cfg, []string{"github"}, pins, pool, nil)
	if errs["github"] != nil {
		t.Fatal(errs["github"])
	}
	if listed := toolNames(t, rebuilt["github"]); len(listed) != 0 {
		t.Fatalf("a tool on the changed binary is served: %v", listed)
	}
	pool.RetireServer("github")()
	pins.Changed("github")
	res, err := c.CallTool(ctx, call)
	if err != nil || !res.IsError {
		t.Fatalf("drifted call: %v %+v", err, res)
	}
	if text := res.Content[0].(mcp.TextContent).Text; !strings.Contains(text, "changed") {
		t.Fatalf("refusal = %q", text)
	}
	if run.runs != 1 {
		t.Fatalf("gh ran %d times, want only the pinned call", run.runs)
	}
}

func startClient(t *testing.T, srv *server.MCPServer) *client.Client {
	t.Helper()
	if srv == nil {
		t.Fatal("no server")
	}
	c, err := client.NewInProcessClient(srv)
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
	return c
}

// toolNames is what srv lists (nil srv: nothing).
func toolNames(t *testing.T, srv *server.MCPServer) []string {
	t.Helper()
	if srv == nil {
		return nil
	}
	res, err := startClient(t, srv).ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	return out
}
