package toolgateway_test

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// An approval relists a source the gateway serves nothing from (no tool
// approved yet): the real relist lists it afresh too, so a definition that
// changed since startup is what Observed shows.
func TestWorker_RelistListsASourceItServesNothingFrom(t *testing.T) {
	backend := server.NewMCPServer("jira", "1")
	handler := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	backend.AddTool(mcp.NewTool("get", mcp.WithDescription("first")), handler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: server.NewStreamableHTTPServer(backend), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })

	home := t.TempDir()
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://`+ln.Addr().String()+`/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
`)
	store := toolgateway.NewStore(home)
	if _, _, err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	tokens := &toolgateway.Tokens{}
	srv := &toolgateway.ReadonlyServer{Store: store, Token: "admin", Tokens: tokens, Log: log.New(&syncBuffer{}, "", 0),
		Listen: func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		Build:  mcpproxy.BuildReadonly, Serve: mcpproxy.Serve, Rebuild: mcpproxy.BuildReadonlyBackends}
	tokens.Current = srv.Applied
	w := &toolgateway.Worker{Server: srv, Store: store, Tokens: tokens, Log: log.New(&syncBuffer{}, "", 0), RefreshEvery: time.Hour}
	runWorker(t, w, srv)
	desc := func() string { return string(w.Observed()["jira"].Tools["get"]) }
	waitFor(t, 5*time.Second, "jira listed at start", func() bool { return strings.Contains(desc(), "first") })

	backend.AddTool(mcp.NewTool("get", mcp.WithDescription("second")), handler)
	// The startup listing shows before Serve is ready for a relist (which
	// then returns without listing): ask until one lists. The worker's
	// periodic refresh is an hour away, so only a relist can change it.
	waitFor(t, 5*time.Second, "a relist lists jira afresh", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		res, err := w.Relist(ctx, "", []string{"jira"})
		if errors.Is(err, toolgateway.ErrRelistDiscarded) {
			return false // Serve not ready yet
		}
		if err != nil {
			t.Fatal(err)
		}
		// What this relist saw, in its own answer.
		return strings.Contains(string(res.Observed["jira"].Tools["get"]), "second")
	})
	if !strings.Contains(desc(), "second") {
		t.Fatalf("Observed has %s after the relist", desc())
	}
}

// jiraGateway is a worker over a real MCP backend serving get with desc,
// registry jira:readonly; it returns the worker, its server, the backend,
// the store and home.
func jiraGateway(t *testing.T, desc string) (*toolgateway.Worker, *toolgateway.ReadonlyServer, *server.MCPServer, *toolreg.Store, string) {
	t.Helper()
	backend := server.NewMCPServer("jira", "1")
	backend.AddTool(mcp.NewTool("get", mcp.WithDescription(desc)), okHandler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: server.NewStreamableHTTPServer(backend), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })
	home := t.TempDir()
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://`+ln.Addr().String()+`/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
`)
	store := toolgateway.NewStore(home)
	if _, _, err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	tokens := &toolgateway.Tokens{}
	srv := &toolgateway.ReadonlyServer{Store: store, Token: "admin", Tokens: tokens, Log: log.New(&syncBuffer{}, "", 0),
		Listen: func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		Build:  mcpproxy.BuildReadonly, Serve: mcpproxy.Serve, Rebuild: mcpproxy.BuildReadonlyBackends}
	tokens.Current = srv.Applied
	w := &toolgateway.Worker{Server: srv, Store: store, Tokens: tokens, Log: log.New(&syncBuffer{}, "", 0), RefreshEvery: time.Hour}
	return w, srv, backend, store, home
}

func okHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}

// pinLive pins jira's get as it is live now and reloads the store,
// returning the new snapshot's version.
func pinLive(t *testing.T, store *toolreg.Store, home string) string {
	t.Helper()
	snap, _, err := store.Reload()
	if err != nil {
		t.Fatal(err)
	}
	cfg := mcpproxy.ReadonlyConfigFrom(snap.Registry())
	cat, errs := mcpproxy.ReadonlyCatalog(context.Background(), cfg)
	if errs["jira"] != nil {
		t.Fatal(errs["jira"])
	}
	pin, err := toolgateway.PinTool(cat.Allowed(cfg.Allow).Servers["jira"].Tools[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := (&toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(hashOnly(toolgateway.Pins{"jira": {"get": pin}})); err != nil {
		t.Fatal(err)
	}
	snap, _, err = store.Reload()
	if err != nil {
		t.Fatal(err)
	}
	return snap.Version()
}

// An approval right after another names the newer snapshot: the worker
// applies it before listing, as for a lease, instead of answering with the
// old one until its next refresh.
func TestWorker_RelistAppliesTheNamedSnapshotFirst(t *testing.T) {
	w, srv, _, store, home := jiraGateway(t, "first")
	runWorker(t, w, srv)
	waitFor(t, 5*time.Second, "jira listed at start", func() bool { return w.Observed()["jira"].Tools["get"] != nil })
	newer := pinLive(t, store, home) // an approval wrote the pins
	if srv.Applied().Version() == newer {
		t.Fatal("the worker applied the new pins on its own; the test proves nothing")
	}
	var res toolgateway.RelistResult
	waitFor(t, 5*time.Second, "a relist under the newer snapshot", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var err error
		res, err = w.Relist(ctx, newer, []string{"jira"})
		return err == nil
	})
	if res.Version != newer {
		t.Fatalf("relist listed under %s, want the named %s", res.Version, newer)
	}
}

// A pinned source whose live tool no longer matches its pin serves
// nothing, yet stays in the backend inventory, so refreshes keep tracking
// it (no relist has to add it).
func TestReadonlyServer_PinnedButChangedSourceStaysInInventory(t *testing.T) {
	w, srv, backend, store, home := jiraGateway(t, "approved")
	pinLive(t, store, home)
	backend.AddTool(mcp.NewTool("get", mcp.WithDescription("changed")), okHandler)
	runWorker(t, w, srv)
	waitFor(t, 5*time.Second, "jira listed changed", func() bool {
		return strings.Contains(string(w.Observed()["jira"].Tools["get"]), "changed")
	})
	if all, _ := srv.Backends(); !slices.Contains(all, "jira") {
		t.Fatalf("inventory %v lacks the pinned source jira", all)
	}
}
