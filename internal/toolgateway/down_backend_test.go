package toolgateway_test

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// syncBuffer is a log sink safe for the gateway's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// mcpBackendOn serves one MCP tool, get, over streamable HTTP on addr until
// the returned stop.
func mcpBackendOn(t *testing.T, addr string) (stop func()) {
	t.Helper()
	backend := server.NewMCPServer("jira", "1")
	backend.AddTool(mcp.NewTool("get", mcp.WithString("key")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: server.NewStreamableHTTPServer(backend), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	var once sync.Once
	return func() { once.Do(func() { _ = hs.Close() }) }
}

// downBackendWorker is a worker over the real build, serve and rebuild,
// refreshing every period, whose jira backend (an mcp-http source on a
// loopback port) refuses connections when the gateway starts. It returns
// the worker, its server, its log, and jira's address.
func downBackendWorker(t *testing.T, period time.Duration) (*toolgateway.Worker, *toolgateway.ReadonlyServer, *syncBuffer, string) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	home := t.TempDir()
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://`+addr+`/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
`)
	// Pin jira's tool while it is up, as aimux tools approve does, then
	// take it down before the gateway starts.
	stop := mcpBackendOn(t, addr)
	store := toolgateway.NewStore(home)
	snap, _, err := store.Reload()
	if err != nil {
		t.Fatal(err)
	}
	cfg := mcpproxy.ReadonlyConfigFrom(snap.Registry())
	cat, errs := mcpproxy.ReadonlyCatalog(context.Background(), cfg)
	if errs["jira"] != nil {
		t.Fatal(errs["jira"])
	}
	pins := toolgateway.Pins{"jira": {}}
	for _, raw := range cat.Allowed(cfg.Allow).Servers["jira"].Tools {
		pin, err := toolgateway.PinTool(raw)
		if err != nil {
			t.Fatal(err)
		}
		pins["jira"]["get"] = pin
	}
	if err := (&toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(hashOnly(pins)); err != nil {
		t.Fatal(err)
	}
	stop()
	if _, _, err := store.Reload(); err != nil {
		t.Fatal(err)
	}

	logw := &syncBuffer{}
	l := log.New(logw, "", 0)
	tokens := &toolgateway.Tokens{}
	srv := &toolgateway.ReadonlyServer{Store: store, Token: "admin", Tokens: tokens, Log: l,
		Listen: func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		Build:  mcpproxy.BuildReadonly, Serve: mcpproxy.Serve, Rebuild: mcpproxy.BuildReadonlyBackends}
	tokens.Current = srv.Applied
	w := &toolgateway.Worker{Server: srv, Store: store, Tokens: tokens, Log: l, RefreshEvery: period}
	return w, srv, logw, addr
}

// runWorker runs w until t ends.
func runWorker(t *testing.T, w *toolgateway.Worker, srv *toolgateway.ReadonlyServer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- toolgateway.RunWorker(ctx, w, srv.Start, tmpSock(t)) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("RunWorker = %v", err)
		}
	})
}

// tmpSock is a control socket path short enough for a unix socket.
func tmpSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, toolgateway.ControlSocketName)
}

// A backend that is down when the worker starts and comes up later is
// served within a refresh period or two: no restart, Status no longer lists
// it down, and the log says it is available.
func TestRunWorker_ServesABackendThatComesUpAfterStart(t *testing.T) {
	const period = 200 * time.Millisecond
	w, srv, logw, addr := downBackendWorker(t, period)
	runWorker(t, w, srv)

	waitFor(t, 5*time.Second, "jira reported down at start", func() bool {
		_, down := srv.Backends()
		return slices.Contains(down, "jira")
	})
	time.Sleep(4 * period) // a few refreshes while it is still down

	stop := mcpBackendOn(t, addr)
	t.Cleanup(stop)
	waitFor(t, 5*time.Second, "jira served once it came up", func() bool {
		_, down := srv.Backends()
		return !slices.Contains(down, "jira") && strings.Contains(logw.String(), "backend jira now available")
	})
	if st := w.Status(); slices.Contains(st.Down, "jira") {
		t.Fatalf("status down %v after jira came up", st.Down)
	}
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", what)
}

// A down backend's failed retries are logged, but at most once per
// interval: the log shows the gateway still tries without a line a minute.
func TestRunWorker_LogsADownBackendsFailedRetriesOncePerInterval(t *testing.T) {
	const period = 100 * time.Millisecond
	w, srv, logw, _ := downBackendWorker(t, period)
	toolgateway.SetDownLogEvery(t, time.Hour)
	runWorker(t, w, srv)

	const line = "read-only: backend jira still unavailable (retried):"
	waitFor(t, 5*time.Second, "a failed retry logged", func() bool { return strings.Contains(logw.String(), line) })
	time.Sleep(8 * period) // several more failed retries within the hour
	if n := strings.Count(logw.String(), line); n != 1 {
		t.Fatalf("%d retry lines within one interval, want 1:\n%s", n, logw.String())
	}
	if !strings.Contains(logw.String(), "connection refused") {
		t.Fatalf("the retry line does not say why:\n%s", logw.String())
	}

	toolgateway.SetDownLogEvery(t, 0) // every failed retry is due again
	waitFor(t, 5*time.Second, "the next interval's retry logged", func() bool { return strings.Count(logw.String(), line) >= 2 })
}
