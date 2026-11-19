package gatewayworker

import (
	"context"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// The sandbox gateway worker never resolves a secret: servers.yaml's
// references (here a cmd one that would leave a marker) are for the host's
// own proxy, not for anything the sandbox side builds.
func TestWorker_NeverCallsTheSecretResolver(t *testing.T) {
	backend := server.NewMCPServer("fake", "1")
	backend.AddTool(mcp.NewTool("search", mcp.WithString("q")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	t.Cleanup(ts.Close)

	home := t.TempDir()
	marker := filepath.Join(home, "resolved")
	script := filepath.Join(home, "secret.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\necho value\n"), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	cfg := filepath.Dir(toolreg.ServersPath(home))
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	ref := "{cmd: {argv: [" + script + "]}}"
	files := map[string]string{
		toolreg.ServersPath(home): "version: 1\nservers:\n" +
			"  fake:\n    url: " + ts.URL + "/\n    secrets:\n      headers:\n        Authorization: " + ref + "\n" +
			"  local:\n    url: http://127.0.0.1:1/mcp\n    secrets:\n      env:\n        TOKEN: " + ref + "\n",
		toolreg.SetsPath(home): "version: 1\nsets:\n  fake:readonly:\n    server: fake\n    tools: [search]\n",
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := toolreg.ReadHome(home)
	if err != nil {
		t.Fatal(err)
	}
	cat, errs := mcpproxy.ReadonlyCatalog(context.Background(), mcpproxy.ReadonlyConfigFrom(reg))
	if len(errs) != 0 {
		t.Fatalf("catalog: %v", errs)
	}
	pins, _, err := toolgateway.Approve(reg, toolgateway.PinFile{}, toolgateway.LiveFromCatalog(cat, nil), []string{"fake:readonly"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(pins); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var addr string
	listen := func(network, _ string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err == nil {
			mu.Lock()
			addr = ln.Addr().String()
			mu.Unlock()
		}
		return ln, err
	}
	ro, store, _ := NewServer(home, "admin-token", log.New(&syncBuf{}, "", 0), listen)
	stop, err := ro.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// Serving (a real MCP request answers) means the add/remove hooks are
	// set, so a Refresh can apply a snapshot.
	rig := &wiringRig{addr: func() string { mu.Lock(); defer mu.Unlock(); return addr }}
	eventually(t, "the gateway to serve", func() bool { return len(rig.served(t)) > 0 })
	// A changed set forces the rebuild path, not only a relist.
	before := ro.Applied().Version()
	if err := os.WriteFile(toolreg.SetsPath(home), []byte("version: 1\nsets:\n  fake:readonly:\n    server: fake\n    tools: [search, search2]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	ro.Refresh(context.Background())
	if v := ro.Applied().Version(); v == before || v != store.Current().Version() {
		t.Fatalf("the new snapshot was not applied: %s (before %s)", v, before)
	}
	ro.Refresh(context.Background()) // and a relist
	if err := ro.RelistNow(context.Background(), []string{"fake"}); err != nil {
		t.Fatal(err)
	}
	ro.WaitBackground()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the gateway worker ran a secret reference")
	}
}
