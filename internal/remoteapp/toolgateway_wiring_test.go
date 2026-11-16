package remoteapp

import (
	"bytes"
	"context"
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

// wiringRig is remote serve's real tool gateway (newToolGateway: real
// BuildReadonly, Serve and Rebuild) over a temp HOME whose registry names
// one fake MCP backend, fake:readonly = [search, other], approved as it is
// now.
// The gateway listens on an ephemeral port, never :7390.
type wiringRig struct {
	backend *server.MCPServer
	gw      toolGatewayParts
	addr    func() string
	clock   *time.Time
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
	clock := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	rig := &wiringRig{backend: backend, clock: &clock, logs: logs, addr: func() string {
		mu.Lock()
		defer mu.Unlock()
		return addr
	}}
	rig.gw = newToolGateway(home, "admin-token", log.New(logs, "", 0), listen)
	rig.gw.gw.now = func() time.Time { return *rig.clock }
	t.Cleanup(rig.gw.gw.close)
	if _, _, err := rig.gw.gw.get(); err != nil { // the first sandbox turn starts it
		t.Fatal(err)
	}
	// Start returns before Serve hands the gateway its add/remove hooks (the
	// serve goroutine does, just before it listens). A Refresh before that
	// has nothing to apply a relist with and, rate limited, is not retried,
	// so wait until the endpoint answers: the hooks are set by then.
	eventually(t, "the gateway to serve", func() bool { return len(rig.served(t)) > 0 })
	return rig
}

// status is what GET /v1/tools reports for fake/search: Review over the
// served snapshot and the gateway's own listings, as listTools computes it.
func (r *wiringRig) status() string {
	v := r.gw.tools()
	for _, st := range toolgateway.Review(v.Snapshot.Registry(), toolgateway.PinsOf(v.Snapshot), v.Live) {
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

// With no call at all, the once-a-minute refresh of a turn relists the
// backends that are up and applies a schema change the same way.
func TestToolGateway_SchemaChangeSeenByTheMinuteRefresh(t *testing.T) {
	r := newWiringRig(t)
	if got := r.status(); got != toolgateway.StatusActive {
		t.Fatalf("before: status %q, want active", got)
	}
	r.changeSchema()
	if _, _, err := r.gw.gw.get(); err != nil { // a turn within the minute: no refresh
		t.Fatal(err)
	}
	if got := r.status(); got != toolgateway.StatusActive {
		t.Fatalf("within the minute: status %q, want active (not relisted yet)", got)
	}
	*r.clock = r.clock.Add(61 * time.Second)
	if _, _, err := r.gw.gw.get(); err != nil { // the next turn starts the relist in the background
		t.Fatal(err)
	}
	eventually(t, "GET /v1/tools to say changed", func() bool { return r.status() == toolgateway.StatusChanged })
	eventually(t, "the gateway to serve only other", func() bool { return slices.Equal(r.served(t), []string{"other"}) })
}
