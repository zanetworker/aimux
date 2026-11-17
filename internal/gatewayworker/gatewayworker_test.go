package gatewayworker

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// syncBuf is a log sink the gateway goroutines may write while the test
// reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// wiringRig is the worker's real tool gateway (NewServer: real
// BuildReadonly, Serve and Rebuild) over a temp HOME whose registry names
// one fake MCP backend, fake:readonly = [search, other], approved as it is
// now.
// The gateway listens on an ephemeral port, never :7390.
type wiringRig struct {
	backend *server.MCPServer
	ro      *toolgateway.ReadonlyServer
	addr    func() string
	logs    *syncBuf
}

func newWiringRig(t *testing.T) *wiringRig {
	t.Helper()
	backend := server.NewMCPServer("fake", "1")
	backend.AddTool(mcp.NewTool("search", mcp.WithString("q")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	backend.AddTool(mcp.NewTool("other", mcp.WithString("id")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	t.Cleanup(ts.Close)

	home := t.TempDir()
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"fake": {Type: toolreg.TypeMCPHTTP, URL: ts.URL + "/"}},
		Sets:    map[string]toolreg.Set{"fake:readonly": {Source: "fake", Tools: []string{"search", "other"}}},
	}
	if err := toolreg.WriteNew(toolreg.DefaultPath(home), reg); err != nil {
		t.Fatal(err)
	}
	cat, errs := mcpproxy.ReadonlyCatalog(context.Background(), mcpproxy.ReadonlyConfigFrom(reg))
	if len(errs) != 0 {
		t.Fatalf("catalog: %v", errs)
	}
	live := toolgateway.LiveFromCatalog(cat, nil)
	pins, _, err := toolgateway.Approve(reg, toolgateway.PinFile{}, live, []string{"fake:readonly"}, time.Now())
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
	logs := &syncBuf{}
	rig := &wiringRig{backend: backend, logs: logs, addr: func() string {
		mu.Lock()
		defer mu.Unlock()
		return addr
	}}
	rig.ro, _, _ = NewServer(home, "admin-token", log.New(logs, "", 0), listen)
	stop, err := rig.ro.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	// Start returns before Serve hands the gateway its add/remove hooks (the
	// serve goroutine does, just before it listens). A Refresh before that
	// has nothing to apply a relist with and, rate limited, is not retried,
	// so wait until the endpoint answers: the hooks are set by then.
	eventually(t, "the gateway to serve", func() bool { return len(rig.served(t)) > 0 })
	return rig
}

// status is what GET /v1/tools reports for fake/search: Review over the
// served snapshot and the gateway's own listings (what the worker sends
// remote serve as Observed).
func (r *wiringRig) status() string {
	snap := r.ro.Applied()
	live := toolgateway.LiveFor(snap.Registry(), r.ro.Observed())
	for _, st := range toolgateway.Review(snap.Registry(), toolgateway.PinsOf(snap), live) {
		if st.Source == "fake" && st.Tool == "search" {
			return st.Status
		}
	}
	return "missing"
}

// client is a fresh MCP session on the gateway's fake endpoint.
func (r *wiringRig) client(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient("http://"+r.addr()+mcpproxy.EndpointPath("fake"),
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer admin-token"}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_LEGACY_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// served is the tool names a fresh session on the fake endpoint lists, or
// nil when the endpoint is gone.
func (r *wiringRig) served(t *testing.T) []string {
	t.Helper()
	c, err := client.NewStreamableHttpClient("http://"+r.addr()+mcpproxy.EndpointPath("fake"),
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer admin-token"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c.Start(ctx) != nil {
		return nil
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_LEGACY_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		return nil
	}
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil
	}
	names := []string{}
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// changeSchema swaps search's input schema under the same name.
func (r *wiringRig) changeSchema() {
	r.backend.DeleteTools("search")
	r.backend.AddTool(mcp.NewTool("search", mcp.WithNumber("q")), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A backend that stays up and changes a tool's schema while serve runs:
// the call's own live listing is enough for GET /v1/tools to say changed
// and for the gateway to stop listing the tool, with no restart and no
// registry or pins edit.
func TestToolGateway_SchemaChangeSeenByACallShowsChangedAndHides(t *testing.T) {
	r := newWiringRig(t)
	if got := r.status(); got != toolgateway.StatusActive {
		t.Fatalf("before: status %q, want active", got)
	}
	if got := r.served(t); !slices.Contains(got, "search") || !slices.Contains(got, "other") {
		t.Fatalf("before: served %v, want search and other", got)
	}
	c := r.client(t)
	r.changeSchema()

	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "search"}})
	if err == nil && !res.IsError {
		t.Fatal("the changed tool was called")
	}
	eventually(t, "GET /v1/tools to say changed", func() bool { return r.status() == toolgateway.StatusChanged })
	eventually(t, "the gateway to serve only other", func() bool { return slices.Equal(r.served(t), []string{"other"}) })
}

// With no call at all, the worker's once-a-minute refresh relists the
// backends that are up and applies a schema change the same way.
func TestToolGateway_SchemaChangeSeenByTheMinuteRefresh(t *testing.T) {
	r := newWiringRig(t)
	if got := r.status(); got != toolgateway.StatusActive {
		t.Fatalf("before: status %q, want active", got)
	}
	r.changeSchema()
	if got := r.status(); got != toolgateway.StatusActive {
		t.Fatalf("before the refresh: status %q, want active (not relisted yet)", got)
	}
	r.ro.Refresh(context.Background()) // the worker's minute tick: starts the relist in the background
	eventually(t, "GET /v1/tools to say changed", func() bool { return r.status() == toolgateway.StatusChanged })
	eventually(t, "the gateway to serve only other", func() bool { return slices.Equal(r.served(t), []string{"other"}) })
}

// A fresh listing for a new lease (RelistNow) sees the change at once.
func TestToolGateway_SchemaChangeSeenByALeaseListing(t *testing.T) {
	r := newWiringRig(t)
	r.changeSchema()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.ro.RelistNow(ctx, []string{"fake"}); err != nil {
		t.Fatal(err)
	}
	if got := r.status(); got != toolgateway.StatusChanged {
		t.Fatalf("after the lease listing: status %q, want changed", got)
	}
	if got := r.served(t); slices.Contains(got, "search") {
		t.Fatalf("served %v: the changed tool is still served after the lease listing", got)
	}
}

// The worker's gateway starts cli sources in the runner sandbox.
func TestNewServer_BindsRunner(t *testing.T) {
	ro, _, _ := NewServer(t.TempDir(), "admin-token", log.New(io.Discard, "", 0), net.Listen)
	if ro.Runner == nil {
		t.Fatal("the tool gateway has no runner binding: cli sources would never start")
	}
	// The gateway's runner ensures aimux-tools (RunnerTools), not the
	// read-only probe status uses, which would never create it.
	tr := ro.Runner(toolreg.Snapshot{})("github")
	if _, ok := tr.(sandboxlaunch.SourceTools); !ok {
		t.Fatalf("the gateway's runner is %T, want sandboxlaunch.SourceTools", tr)
	}
}
