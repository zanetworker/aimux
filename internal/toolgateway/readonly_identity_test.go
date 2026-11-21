package toolgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// A server whose identity changes at the same URL (here a new secret name;
// for a managed server also a new binary or args) is taken
// down before the rebuild, like a moved endpoint: no call reaches it
// through a server built under the old identity while the rebuild runs.
// One whose identity is the same stays up.
func TestReadonlyServer_IdentityChangeDownDuringRebuild(t *testing.T) {
	var logw bytes.Buffer
	r, _, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.ServersPath(home), "version: 1\nservers:\n"+
		"  jira:\n    url: http://127.0.0.1:8001/mcp\n"+
		"  google:\n    url: http://127.0.0.1:8000/mcp\n")
	writeHomeFile(t, toolreg.SetsPath(home), "version: 1\nsets:\n"+
		"  jira:readonly: {server: jira, tools: [get]}\n  google:readonly: {server: google, tools: [doc]}\n")
	var built mcpproxy.ToolPins
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		built = pins
		out := map[string]*server.MCPServer{}
		for n := range cfg.HTTP {
			out[n] = server.NewMCPServer(n, "1")
		}
		return out, mcpproxy.NewPool((&backendLog{}).starter(cfg), mcpproxy.PoolOptions{}), nil, nil
	}
	served := make(chan mcpproxy.DaemonOptions, 1)
	r.Serve = func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, p *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
		served <- opt
		<-ctx.Done()
		p.Close()
		return ln.Close()
	}
	live := map[string]bool{}
	var midRebuild map[string]bool
	r.Rebuild = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		midRebuild = maps.Clone(live)
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			out[n] = server.NewMCPServer(n, "2")
		}
		return out, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	opt := <-served
	live["jira"], live["google"] = true, true
	opt.AddServers(func(n string, _ *server.MCPServer) { live[n] = true }, func(n string) bool { ok := live[n]; delete(live, n); return ok })

	// Same URL, another identity: jira now sends an Authorization secret.
	writeHomeFile(t, toolreg.ServersPath(home), "version: 1\nservers:\n"+
		"  jira:\n    url: http://127.0.0.1:8001/mcp\n    secrets:\n      headers:\n        Authorization: {env: {name: JIRA_AUTH}}\n"+
		"  google:\n    url: http://127.0.0.1:8000/mcp\n")
	reload(t, r)
	r.Refresh(context.Background())
	if midRebuild == nil {
		t.Fatal("an identity change must rebuild")
	}
	if midRebuild["jira"] || !midRebuild["google"] {
		t.Fatalf("endpoints mid-rebuild %v: want jira down, google up", midRebuild)
	}
	if !live["jira"] || !live["google"] {
		t.Fatalf("endpoints after rebuild %v: want both served", live)
	} // A server built at start, under the old identity, takes no call now.
	guard := built.Guard(func(context.Context, string) ([]json.RawMessage, error) { return nil, nil })
	if err := guard(context.Background(), "jira", "get"); !errors.Is(err, toolgateway.ErrSourceChanged) {
		t.Fatalf("old build's guard: %v, want ErrSourceChanged", err)
	}
}

// A backend the pool started but the gateway serves nothing from (no tool
// pinned at start) is retired too when its server's identity changes, so a
// later relist never reaches the old process's session under the new
// identity.
func TestReadonlyServer_IdentityChangeRetiresUnservedBackend(t *testing.T) {
	var logw bytes.Buffer
	r, _, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.ServersPath(home), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n")
	writeHomeFile(t, toolreg.SetsPath(home), "version: 1\nsets:\n  jira:readonly: {server: jira, tools: [get]}\n")
	backends := &backendLog{}
	r.Starter = backends.starter
	var pool *mcpproxy.Pool
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		pool = mcpproxy.NewPool(backends.starter(cfg), mcpproxy.PoolOptions{})
		return map[string]*server.MCPServer{}, pool, nil, nil // listed, nothing pinned: not served
	}
	served := make(chan mcpproxy.DaemonOptions, 1)
	r.Serve = func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, p *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
		served <- opt
		<-ctx.Done()
		p.Close()
		return ln.Close()
	}
	r.Rebuild = func(context.Context, mcpproxy.ReadonlyConfig, []string, mcpproxy.ToolPins, *mcpproxy.Pool, func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		return map[string]*server.MCPServer{}, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	opt := <-served
	opt.AddServers(func(string, *server.MCPServer) {}, func(string) bool { return false })
	lease, err := pool.Lease(context.Background(), "jira") // the build's listing left a session
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	if backends.open() != 1 {
		t.Fatalf("open backends = %d, want 1", backends.open())
	}
	writeHomeFile(t, toolreg.ServersPath(home), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n    secrets:\n      headers:\n        Authorization: {env: {name: JIRA_AUTH}}\n")
	reload(t, r)
	r.Refresh(context.Background())
	if n := backends.open(); n != 0 {
		t.Fatalf("open backends after the identity change = %d, want 0 (old session retired)", n)
	}
}
