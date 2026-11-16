package toolgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// readonlyRig is a ReadonlyServer over fakes: an ephemeral listener (never
// :7390), a Build that serves nothing, and a Serve that records its options.
func readonlyRig(t *testing.T, logw *bytes.Buffer) (*toolgateway.ReadonlyServer, chan mcpproxy.DaemonOptions, *string, string) {
	t.Helper()
	served := make(chan mcpproxy.DaemonOptions, 1)
	asked := new(string)
	home := t.TempDir()
	l := log.New(logw, "", 0)
	return &toolgateway.ReadonlyServer{
		Store: toolgateway.NewStore(home),
		Log:   l,
		Listen: func(network, addr string) (net.Listener, error) {
			*asked = addr
			return net.Listen("tcp", "127.0.0.1:0")
		},
		Build: func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
			return map[string]*server.MCPServer{}, nil, map[string]error{"jira": errors.New("down")}, nil
		},
		Serve: func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, _ *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
			served <- opt
			<-ctx.Done()
			return ln.Close()
		},
	}, served, asked, home
}

// The server listens on the read-only address and checks both the admin
// token (aimux browser verify) and the issued session tokens.
func TestReadonlyServer_ServesAdminTokenAndSessionTokens(t *testing.T) {
	var logw bytes.Buffer
	r, served, asked, _ := readonlyRig(t, &logw)
	r.Token = "admin-token"
	r.Tokens = &toolgateway.Tokens{}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opt := <-served
	stop()
	if *asked != toolgateway.ReadonlyAddr || toolgateway.ReadonlyAddr != "127.0.0.1:7390" {
		t.Fatalf("listened on %q, want %s", *asked, toolgateway.ReadonlyAddr)
	}
	if opt.Token != "admin-token" || opt.Tokens != r.Tokens || opt.AddServers == nil {
		t.Fatalf("daemon options: token %q tokens %v addServers %v", opt.Token, opt.Tokens, opt.AddServers != nil)
	}
	if !strings.Contains(logw.String(), "backend jira unavailable") || strings.Contains(logw.String(), "admin-token") {
		t.Fatalf("log %q: want the down backend, never the token", logw.String())
	}
}

// A listen failure is an error and leaves nothing running.
func TestReadonlyServer_ListenFailure(t *testing.T) {
	var logw bytes.Buffer
	r, _, _, _ := readonlyRig(t, &logw)
	r.Listen = func(string, string) (net.Listener, error) { return nil, errors.New("address in use") }
	if _, err := r.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "127.0.0.1:7390") {
		t.Fatalf("Start = %v, want a listen error naming the address", err)
	}
}

// Refresh reloads the pins: a changed pin set rebuilds every backend with
// the new pins (replacing the running servers); unchanged pins with no
// backend down and unchanged listings replace nothing (Refresh relists the
// backends that are up, and only a changed listing replaces one).
func TestReadonlyServer_RefreshReloadsChangedPins(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p1"}})); err != nil {
		t.Fatal(err)
	}
	r.Build = func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		return map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1")}, nil, nil, nil
	}
	var rebuilt [][]string
	var rebuiltPins []mcpproxy.ToolPins
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		rebuilt = append(rebuilt, names)
		rebuiltPins = append(rebuiltPins, pins)
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
	var added []string
	opt.AddServers(func(name string, _ *server.MCPServer) { added = append(added, name) }, func(string) bool { return true })

	reload(t, r)
	r.Refresh(context.Background())
	r.WaitBackground() // an unchanged snapshot relists in the background
	if len(added) != 0 {
		t.Fatalf("unchanged pins, nothing down: replaced %v", added)
	}
	rebuilt, rebuiltPins = nil, nil
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p2"}})); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	r.Refresh(context.Background())
	if len(rebuilt) != 1 || strings.Join(rebuilt[0], ",") != "wiki" || strings.Join(added, ",") != "wiki" {
		t.Fatalf("re-pin: rebuilt %v added %v, want wiki rebuilt and replaced", rebuilt, added)
	}
	if p, _ := rebuiltPins[0].(toolgateway.SnapshotPins); p.Pins["wiki"]["search"] != "p2" {
		t.Fatalf("rebuilt with pins %v, want the new ones", rebuiltPins[0])
	}
}

// refreshRig starts r serving wiki and jira pinned, and records what
// Refresh adds and removes on the running daemon.
func refreshRig(t *testing.T, r *toolgateway.ReadonlyServer, served chan mcpproxy.DaemonOptions, home string) (store toolgateway.PinStore, added, removed *[]string) {
	t.Helper()
	store = toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p1"}, "jira": {"get": "p1"}})); err != nil {
		t.Fatal(err)
	}
	r.Build = func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		return map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1"), "jira": server.NewMCPServer("jira", "1")}, nil, nil, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	opt := <-served
	added, removed = new([]string), new([]string)
	opt.AddServers(func(name string, _ *server.MCPServer) { *added = append(*added, name) },
		func(name string) bool { *removed = append(*removed, name); return true })
	return store, added, removed
}

// A re-pin that leaves a backend with no pinned tool removes its endpoint,
// so tokens already issued can no longer reach the revoked tools.
func TestReadonlyServer_RefreshRemovesBackendWithNoPinnedTools(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			if p, _ := pins.(toolgateway.SnapshotPins); len(p.Pins[n]) > 0 {
				out[n] = server.NewMCPServer(n, "2")
			}
		}
		return out, nil
	}
	store, added, removed := refreshRig(t, r, served, home)
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p1"}})); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	r.Refresh(context.Background())
	if strings.Join(*added, ",") != "wiki" || strings.Join(*removed, ",") != "jira" {
		t.Fatalf("jira un-pinned: added %v removed %v, want wiki replaced and jira removed", *added, *removed)
	}
}

// A re-pin whose rebuild of a backend fails removes that backend's old
// endpoint (fail closed) and keeps it down, so a later Refresh with the
// same pins retries and adds it.
func TestReadonlyServer_RefreshRemovesFailedRebuildAndRetriesIt(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	jiraUp := false
	var rebuilt [][]string
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		rebuilt = append(rebuilt, names)
		out, errs := map[string]*server.MCPServer{}, map[string]error{}
		for _, n := range names {
			if n == "jira" && !jiraUp {
				errs[n] = errors.New("down")
				continue
			}
			out[n] = server.NewMCPServer(n, "2")
		}
		return out, errs
	}
	store, added, removed := refreshRig(t, r, served, home)
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p2"}, "jira": {"get": "p2"}})); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	r.Refresh(context.Background())
	if strings.Join(*added, ",") != "wiki" || strings.Join(*removed, ",") != "jira" {
		t.Fatalf("jira rebuild failed: added %v removed %v, want wiki replaced and jira removed", *added, *removed)
	}
	jiraUp = true
	reload(t, r)
	r.Refresh(context.Background())
	r.WaitBackground() // the same pins: the retry runs with the background relist
	if got := rebuilt[len(rebuilt)-1]; !slices.Contains(got, "jira") {
		t.Fatalf("retry rebuilt %q, want jira (still down) retried", got)
	}
	if strings.Join(*added, ",") != "wiki,jira" || strings.Join(*removed, ",") != "jira" {
		t.Fatalf("retry: added %v removed %v, want jira added back", *added, *removed)
	}
}

// A registry that does not load (a typo) is logged with its reason and the
// gateway still starts, serving no backend; per-set errors are logged and
// the other sets are served.
func TestReadonlyServer_BrokenRegistryStillStarts(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.DefaultPath(home), "version: 1\nsorces: {}\n")
	var got mcpproxy.ReadonlyConfig
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		got = cfg
		return map[string]*server.MCPServer{}, nil, nil, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatalf("Start = %v; want it to start without the registry", err)
	}
	<-served
	stop()
	if len(got.HTTP)+len(got.Notes)+len(got.Allow) != 0 {
		t.Errorf("built %+v from a broken registry", got)
	}
	if !strings.Contains(logw.String(), "sorces") {
		t.Errorf("log %q should name the bad key", logw.String())
	}
}

func TestReadonlyServer_LogsSetErrorsAndServesTheRest(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:8001/mcp"}
sets:
  jira:readonly: {source: jira, tools: [search]}
  slack:readonly: {source: slack, tools: [search_messages]}
`)
	var got mcpproxy.ReadonlyConfig
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		got = cfg
		return map[string]*server.MCPServer{}, nil, nil, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-served
	stop()
	if !reflect.DeepEqual(got.Allow, mcpproxy.Allowlist{"jira": {"search"}}) {
		t.Errorf("allow = %v", got.Allow)
	}
	if !strings.Contains(logw.String(), `"slack:readonly"`) || !strings.Contains(logw.String(), `unknown source "slack"`) {
		t.Errorf("log %q should name the broken set", logw.String())
	}
}

// Start reads a readonly.json not migrated yet in memory, so a gateway on
// an upgraded install keeps its backends, wiki included, and writes
// nothing (remote serve migrates once at start, the CLI too).
func TestReadonlyServer_ReadsLegacyConfigWithoutMigrating(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	wiki := t.TempDir()
	writeHomeFile(t, toolreg.LegacyPath(home), `{"HTTP": {"jira": {"URL": "http://127.0.0.1:8001/mcp"}}, "WikiRoot": "`+wiki+`", "Allow": {"jira": ["search"], "wiki": ["wiki_read"]}}`)
	var got mcpproxy.ReadonlyConfig
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		got = cfg
		return map[string]*server.MCPServer{}, nil, nil, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-served
	stop()
	if got.Notes["wiki"] != wiki || !reflect.DeepEqual(got.Allow["wiki"], []string{"wiki_read"}) {
		t.Errorf("config = %+v", got)
	}
	if _, err := os.Stat(toolreg.LegacyPath(home)); err != nil {
		t.Errorf("readonly.json moved: %v", err)
	}
	if _, err := os.Lstat(toolreg.DefaultPath(home)); err == nil {
		t.Error("the gateway wrote tool-sources.yaml")
	}
}

func writeHomeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hashOnly is p as v1-style records (hash only), for seeding a pin store.
func hashOnly(p toolgateway.Pins) toolgateway.PinFile {
	f := toolgateway.PinFile{}
	for s, tools := range p {
		f[s] = map[string]toolgateway.PinRecord{}
		for t, h := range tools {
			f[s][t] = toolgateway.PinRecord{Hash: h}
		}
	}
	return f
}

// reload is the one Reload a turn triggers in remote serve.
func reload(t *testing.T, r *toolgateway.ReadonlyServer) {
	t.Helper()
	if _, _, err := r.Store.Reload(); err != nil {
		t.Fatal(err)
	}
}

// Refresh applies the store's current snapshot and never reads the files
// itself: a re-pin on disk takes effect only once the one Reload has run,
// and Applied and Stale say which snapshot is served.
func TestReadonlyServer_RefreshAppliesTheStoreSnapshotOnly(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			out[n] = server.NewMCPServer(n, "2")
		}
		return out, nil
	}
	store, added, _ := refreshRig(t, r, served, home)
	started := r.Applied()
	if started.Version() == "" || started.Version() != r.Store.Current().Version() || r.Stale() {
		t.Fatalf("Start applied %q, store has %q", started.Version(), r.Store.Current().Version())
	}
	if !strings.Contains(logw.String(), "snapshot "+started.Short()) {
		t.Errorf("log %q should name the snapshot", logw.String())
	}
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p9"}, "jira": {"get": "p1"}})); err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	if len(*added) != 0 || r.Applied().Version() != started.Version() {
		t.Fatalf("Refresh read the pins on its own: replaced %v", *added)
	}
	reload(t, r)
	if !r.Stale() {
		t.Fatal("after a reload the gateway should be stale")
	}
	r.Refresh(context.Background())
	if strings.Join(*added, ",") != "jira,wiki" || r.Applied().Version() != r.Store.Current().Version() || r.Stale() {
		t.Fatalf("after reload: replaced %v applied=%s current=%s", *added, r.Applied().Short(), r.Store.Current().Short())
	}
}

// fakeBackend is a backend the pool tests count.
type fakeBackend struct {
	url    string
	closed atomic.Bool
}

func (b *fakeBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) { return nil, nil }
func (b *fakeBackend) CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}
func (b *fakeBackend) Close() error { b.closed.Store(true); return nil }

type backendLog struct {
	mu  sync.Mutex
	all []*fakeBackend
}

func (l *backendLog) starter(cfg mcpproxy.ReadonlyConfig) mcpproxy.Starter {
	return func(_ context.Context, name string) (mcpproxy.Backend, error) {
		b := &fakeBackend{url: cfg.HTTP[name].URL}
		l.mu.Lock()
		l.all = append(l.all, b)
		l.mu.Unlock()
		return b, nil
	}
}

func (l *backendLog) open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, b := range l.all {
		if !b.closed.Load() {
			n++
		}
	}
	return n
}

// A registry change (a moved URL, a dropped source) keeps the one pool the
// daemon owns: it switches the pool's starter to the new config, retires
// only the moved or dropped backends (a call in flight on one finishes
// first), rebuilds the servers on that pool, removes the dropped endpoint,
// and the daemon's shutdown closes every backend, the new ones included.
func TestReadonlyServer_RegistryChangeKeepsOnePool(t *testing.T) {
	var logw bytes.Buffer
	r, _, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:8001/mcp"}
  slack: {type: mcp-http, url: "http://127.0.0.1:7381/mcp/slack"}
  google: {type: mcp-http, url: "http://127.0.0.1:8000/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
  slack:readonly: {source: slack, tools: [search]}
  google:readonly: {source: google, tools: [doc]}
`)
	backends := &backendLog{}
	r.Starter = backends.starter
	var pool *mcpproxy.Pool
	builds := 0
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		builds++
		pool = mcpproxy.NewPool(backends.starter(cfg), mcpproxy.PoolOptions{})
		out := map[string]*server.MCPServer{}
		for n := range cfg.HTTP {
			out[n] = server.NewMCPServer(n, "1")
		}
		return out, pool, nil, nil
	}
	served := make(chan mcpproxy.DaemonOptions, 1)
	r.Serve = func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, p *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
		served <- opt
		<-ctx.Done()
		p.Close() // as mcpproxy.Serve does at shutdown
		return ln.Close()
	}
	var rebuiltOn []*mcpproxy.Pool
	var rebuilt []string
	r.Rebuild = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, p *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		rebuiltOn = append(rebuiltOn, p)
		rebuilt = append(rebuilt, names...)
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			if _, ok := cfg.HTTP[n]; ok {
				out[n] = server.NewMCPServer(n, "2")
			}
		}
		return out, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opt := <-served
	var added, removed []string
	opt.AddServers(func(n string, _ *server.MCPServer) { added = append(added, n) }, func(n string) bool { removed = append(removed, n); return true })
	ctx := context.Background()
	inflight, err := pool.Lease(ctx, "jira") // a call in flight on jira
	if err != nil {
		t.Fatal(err)
	}
	google, err := pool.Lease(ctx, "google")
	if err != nil {
		t.Fatal(err)
	}
	_ = google.Close()

	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:9001/mcp"}
  google: {type: mcp-http, url: "http://127.0.0.1:8000/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
  google:readonly: {source: google, tools: [doc]}
`)
	reload(t, r)
	r.Refresh(ctx)
	if builds != 1 || len(rebuiltOn) != 1 || rebuiltOn[0] != pool {
		t.Fatalf("builds=%d rebuilt on %v, want the Start pool only", builds, rebuiltOn)
	}
	if strings.Join(rebuilt, ",") != "google,jira" || strings.Join(removed, ",") != "jira,slack" || strings.Join(added, ",") != "google,jira" {
		t.Fatalf("rebuilt %v added %v removed %v", rebuilt, added, removed)
	}
	old := backends.all[0] // jira at 8001
	if old.url != "http://127.0.0.1:8001/mcp" || old.closed.Load() {
		t.Fatalf("moved backend %+v closed with a call in flight", old)
	}
	if _, err := inflight.ListToolsRaw(ctx); err != nil {
		t.Fatalf("in-flight call failed: %v", err)
	}
	_ = inflight.Close()
	if !old.closed.Load() {
		t.Fatal("moved backend not closed after its call")
	}
	if backends.all[1].closed.Load() {
		t.Fatal("unchanged google backend was retired")
	}
	next, err := pool.Lease(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	_ = next.Close()
	if got := backends.all[len(backends.all)-1].url; got != "http://127.0.0.1:9001/mcp" {
		t.Fatalf("new jira backend at %s, want the moved URL", got)
	}
	stop()
	if n := backends.open(); n != 0 {
		t.Fatalf("%d backends left open at shutdown", n)
	}
}

// The gateway publishes the snapshot it is moving to before it swaps any
// server, so while a rebuild runs a token from an older snapshot has
// already lost the re-pinned tool (fail closed).
func TestReadonlyServer_PublishesSnapshotBeforeSwapping(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	tokens := &toolgateway.Tokens{Current: r.Applied}
	r.Tokens = tokens
	var during map[string][]string
	var tok string
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		_, during, _ = tokens.Lookup(tok) // mid-rebuild, before any server is swapped
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			out[n] = server.NewMCPServer(n, "2")
		}
		return out, nil
	}
	store, _, _ := refreshRig(t, r, served, home)
	tok, _ = tokens.Issue("s", map[string][]string{"wiki": {"search"}, "jira": {"get"}}, r.Applied())
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p2"}, "jira": {"get": "p1"}})); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	r.Refresh(context.Background())
	want := map[string][]string{"wiki": {}, "jira": {"get"}}
	if !reflect.DeepEqual(during, want) {
		t.Fatalf("token during rebuild allowed %v, want %v", during, want)
	}
}

// A pins file that does not read fails closed in the gateway: it logs the
// file and error and serves no tool; fixing the file restores the backends.
func TestReadonlyServer_BrokenPinsServesNothingUntilFixed(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			if p, _ := pins.(toolgateway.SnapshotPins); len(p.Pins[n]) > 0 {
				out[n] = server.NewMCPServer(n, "2")
			}
		}
		return out, nil
	}
	store, added, removed := refreshRig(t, r, served, home)
	writeHomeFile(t, store.Path, "{bad")
	if _, _, err := r.Store.Reload(); err == nil {
		t.Fatal("broken pins must fail the reload")
	}
	r.Refresh(context.Background())
	if strings.Join(*removed, ",") != "jira,wiki" || len(*added) != 0 {
		t.Fatalf("broken pins: added %v removed %v, want every backend removed", *added, *removed)
	}
	if !strings.Contains(logw.String(), "pins.json") || !strings.Contains(logw.String(), "failing closed") {
		t.Errorf("log %q should name the pins file and say it fails closed", logw.String())
	}
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "p1"}, "jira": {"get": "p1"}})); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	r.Refresh(context.Background())
	if strings.Join(*added, ",") != "jira,wiki" {
		t.Fatalf("after the fix: added %v", *added)
	}
}

// A moved source's endpoint is taken down before the rebuild, so mid-rebuild
// no call reaches its new URL under the old snapshot's server; the rebuild
// adds it back. An unchanged source keeps serving throughout.
func TestReadonlyServer_MovedEndpointDownDuringRebuild(t *testing.T) {
	var logw bytes.Buffer
	r, _, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:8001/mcp"}
  google: {type: mcp-http, url: "http://127.0.0.1:8000/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
  google:readonly: {source: google, tools: [doc]}
`)
	r.Build = func(_ context.Context, cfg mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
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
		midRebuild = maps.Clone(live) // the rebuild is paused here
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
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:9001/mcp"}
  google: {type: mcp-http, url: "http://127.0.0.1:8000/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
  google:readonly: {source: google, tools: [doc]}
`)
	reload(t, r)
	r.Refresh(context.Background())
	if midRebuild["jira"] || !midRebuild["google"] {
		t.Fatalf("endpoints mid-rebuild %v: want jira down, google up", midRebuild)
	}
	if !live["jira"] || !live["google"] {
		t.Fatalf("endpoints after rebuild %v: want both served", live)
	}
}

// While the gateway still serves the older snapshot (its refresh has not
// applied the newer one yet), a token issued under the newer snapshot
// cannot reach a tool whose endpoint moved and was re-approved there: the
// servers still running were built for the old endpoint and pin. A tool
// whose pin is the same in both snapshots stays usable.
func TestReadonlyServer_NewerTokenRefusedChangedToolBeforeRefresh(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	tokens := &toolgateway.Tokens{Current: r.Applied}
	r.Tokens = tokens
	reg := func(jiraURL string) string {
		return `version: 1
sources:
  jira: {type: mcp-http, url: "` + jiraURL + `"}
  google: {type: mcp-http, url: "http://127.0.0.1:8000/mcp"}
sets:
  jira:readonly: {source: jira, tools: [get]}
  google:readonly: {source: google, tools: [doc]}
`
	}
	writeHomeFile(t, toolreg.DefaultPath(home), reg("http://127.0.0.1:8001/mcp"))
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	pins := func(jiraHash, jiraSource string) toolgateway.PinFile {
		return toolgateway.PinFile{"jira": {"get": {Hash: jiraHash, Source: jiraSource}},
			"google": {"doc": {Hash: "g1", Source: "http://127.0.0.1:8000/mcp"}}}
	}
	if err := store.Save(pins("h1", "http://127.0.0.1:8001/mcp")); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	<-served
	old := r.Applied()

	// The owner moves jira and re-approves get there; a turn reloads and
	// compiles under the newer snapshot, but no Refresh has applied it.
	writeHomeFile(t, toolreg.DefaultPath(home), reg("http://127.0.0.1:9001/mcp"))
	if err := store.Save(pins("h2", "http://127.0.0.1:9001/mcp")); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	newer := r.Store.Current()
	if newer.Version() == old.Version() || r.Applied().Version() != old.Version() {
		t.Fatal("want the store ahead of the served snapshot")
	}
	tok, err := tokens.Issue("s", map[string][]string{"jira": {"get"}, "google": {"doc"}}, newer)
	if err != nil {
		t.Fatal(err)
	}
	_, allow, ok := tokens.Lookup(tok)
	want := map[string][]string{"jira": {}, "google": {"doc"}}
	if !ok || !reflect.DeepEqual(allow, want) {
		t.Fatalf("newer token against the old servers allowed %v, want %v", allow, want)
	}
}
