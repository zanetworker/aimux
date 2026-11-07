package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// setSource is a test's servers file and catalog: Load returns its current
// set, and the test changes it between reloads.
type setSource struct {
	mu      sync.Mutex
	set     ServerSet
	err     error
	loads   int
	applied []ServerSet
}

func (s *setSource) load() (ServerSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return copySet(s.set), s.err
}

func (s *setSource) apply(set ServerSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied = append(s.applied, set)
}

func (s *setSource) change(f func(*ServerSet)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.set)
}

func (s *setSource) appliedSets() []ServerSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ServerSet(nil), s.applied...)
}

func (s *setSource) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *setSource) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func copySet(in ServerSet) ServerSet {
	out := ServerSet{Configs: map[string]ServerConfig{}, Resolved: append([]string(nil), in.Resolved...), Catalog: &Catalog{Servers: map[string]CatalogEntry{}}}
	for k, v := range in.Configs {
		out.Configs[k] = v
	}
	for k, v := range in.Catalog.Servers {
		out.Catalog.Servers[k] = v
	}
	return out
}

func toolJSON(names ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, n := range names {
		out = append(out, json.RawMessage(`{"name":"`+n+`","inputSchema":{"type":"object"}}`))
	}
	return out
}

// newSet serves each server with one tool named after it ("<server>_tool")
// and the command "<server>-mcp".
func newSet(servers ...string) ServerSet {
	set := ServerSet{Configs: map[string]ServerConfig{}, Catalog: &Catalog{}}
	for _, s := range servers {
		set.Configs[s] = ServerConfig{Command: s + "-mcp"}
		set.Catalog.Set(s, toolJSON(s+"_tool"), time.Unix(0, 0))
	}
	return set
}

type reloadDaemon struct {
	base string
	port int
	src  *setSource
	fs   *fakeStarter
	pool *Pool
}

func startReloadDaemon(t *testing.T, fs *fakeStarter, set ServerSet) *reloadDaemon {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	src := &setSource{set: copySet(set)}
	pool := NewPool(fs.start, PoolOptions{})
	mon := &Monitor{Pool: pool, Servers: sortedServerNames(set.Configs), Port: ln.Addr().(*net.TCPAddr).Port}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	opt := DaemonOptions{Status: mon, Reload: &ReloadOptions{Current: copySet(set), Load: src.load, Apply: src.apply}}
	go func() { done <- Serve(ctx, ln, NewServers(set.Catalog, pool), pool, opt) }()
	t.Cleanup(func() { cancel(); <-done })
	return &reloadDaemon{base: "http://" + ln.Addr().String(), port: ln.Addr().(*net.TCPAddr).Port, src: src, fs: fs, pool: pool}
}

func (d *reloadDaemon) reload(t *testing.T) ReloadResult {
	t.Helper()
	res, err := ReloadDaemon(d.port)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func sameNames(got, want []string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

func TestReload_AddsAnEndpointAndLeavesOthersAlone(t *testing.T) {
	fs := newFakeStarter()
	d := startReloadDaemon(t, fs, newSet("slack"))
	slack := httpClient(t, endpointURL(d.base, "slack"))
	defer func() { _ = slack.Close() }()
	mustCall(t, slack, "slack_tool")

	d.src.change(func(s *ServerSet) {
		s.Configs["github"] = ServerConfig{Command: "github-mcp"}
		s.Catalog.Set("github", toolJSON("search"), time.Unix(0, 0))
	})
	res := d.reload(t)
	if !sameNames(res.Added, []string{"github"}) || len(res.Removed) != 0 || len(res.Changed) != 0 {
		t.Fatalf("reload = %+v, want github added only", res)
	}
	gh := httpClient(t, endpointURL(d.base, "github"))
	defer func() { _ = gh.Close() }()
	if got := toolNames(t, gh); !sameNames(got, []string{"search"}) {
		t.Fatalf("/mcp/github lists %v, want [search]", got)
	}
	mustCall(t, gh, "search")
	// The session opened before the reload keeps working on the same backend.
	mustCall(t, slack, "slack_tool")
	if fs.count("slack") != 1 || fs.backends[0].closed.Load() {
		t.Fatalf("slack started %d times (closed %v) across the reload, want its one backend kept", fs.count("slack"), fs.backends[0].closed.Load())
	}
}

func TestReload_RemovesAnEndpointAndClosesItsBackends(t *testing.T) {
	fs := newFakeStarter()
	d := startReloadDaemon(t, fs, newSet("slack", "github"))
	slack := httpClient(t, endpointURL(d.base, "slack"))
	defer func() { _ = slack.Close() }()
	gh := httpClient(t, endpointURL(d.base, "github"))
	defer func() { _ = gh.Close() }()
	mustCall(t, slack, "slack_tool") // backend 0
	mustCall(t, gh, "github_tool")   // backend 1

	d.src.change(func(s *ServerSet) {
		delete(s.Configs, "github")
		delete(s.Catalog.Servers, "github")
	})
	res := d.reload(t)
	if !sameNames(res.Removed, []string{"github"}) || len(res.Added) != 0 || len(res.Changed) != 0 {
		t.Fatalf("reload = %+v, want github removed only", res)
	}
	if !fs.backends[1].closed.Load() {
		t.Fatal("the removed server's backend is still running")
	}
	if code, _ := get(t, "POST", endpointURL(d.base, "github"), ""); code != http.StatusNotFound {
		t.Fatalf("POST /mcp/github after removal: %d, want 404", code)
	}
	mustCall(t, slack, "slack_tool")
	if fs.count("slack") != 1 || fs.backends[0].closed.Load() {
		t.Fatal("removing github touched slack's backend")
	}
}

func TestReload_ChangedDefinitionDrainsInFlightCallsThenRestarts(t *testing.T) {
	fs := newFakeStarter()
	var block atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		if block.CompareAndSwap(true, false) {
			close(started)
			<-release
			return mcp.NewToolResultText("slow"), nil
		}
		return mcp.NewToolResultText("ok"), nil
	}
	d := startReloadDaemon(t, fs, newSet("slack", "github"))
	gh := httpClient(t, endpointURL(d.base, "github"))
	defer func() { _ = gh.Close() }()
	mustCall(t, gh, "github_tool") // backend 0
	slack := httpClient(t, endpointURL(d.base, "slack"))
	defer func() { _ = slack.Close() }()

	block.Store(true)
	type outcome struct {
		res *mcp.CallToolResult
		err error
	}
	inflight := make(chan outcome, 1)
	go func() {
		r, err := slack.CallTool(context.Background(), req("slack_tool")) // backend 1
		inflight <- outcome{r, err}
	}()
	<-started

	d.src.change(func(s *ServerSet) { s.Configs["slack"] = ServerConfig{Command: "slack-mcp", Args: []string{"--v2"}} })
	res := d.reload(t)
	if !sameNames(res.Changed, []string{"slack"}) || len(res.Added) != 0 || len(res.Removed) != 0 {
		t.Fatalf("reload = %+v, want slack changed only", res)
	}
	if fs.backends[1].closed.Load() {
		t.Fatal("the changed server's backend was closed with a call in flight")
	}
	close(release)
	o := <-inflight
	if o.err != nil || o.res.IsError || resultText(t, o.res) != "slow" {
		t.Fatalf("in-flight call across the reload: %v %+v, want it to finish normally", o.err, o.res)
	}
	waitFor(t, "drained backend closed after its call", fs.backends[1].closed.Load)

	// The same client session keeps working; its next call starts the new definition.
	mustCall(t, slack, "slack_tool")
	if fs.count("slack") != 2 {
		t.Fatalf("slack started %d times, want a fresh start after the change", fs.count("slack"))
	}
	if applied := d.src.appliedSets(); len(applied) != 1 || applied[0].Configs["slack"].Args[0] != "--v2" {
		t.Fatalf("Apply got %+v, want the new set once", applied)
	}
	mustCall(t, gh, "github_tool")
	if fs.count("github") != 1 || fs.backends[0].closed.Load() {
		t.Fatal("changing slack touched github's backend")
	}
}

func TestReload_ResolvedFlagCountsAsAChange(t *testing.T) {
	d := startReloadDaemon(t, newFakeStarter(), newSet("slack"))
	d.src.change(func(s *ServerSet) { s.Resolved = []string{"slack"} })
	if res := d.reload(t); !sameNames(res.Changed, []string{"slack"}) {
		t.Fatalf("reload = %+v, want slack changed (its definition is now started without expansion)", res)
	}
}

func TestReload_UnchangedConfigChangesNothing(t *testing.T) {
	fs := newFakeStarter()
	d := startReloadDaemon(t, fs, newSet("slack", "github"))
	slack := httpClient(t, endpointURL(d.base, "slack"))
	defer func() { _ = slack.Close() }()
	mustCall(t, slack, "slack_tool")

	code, body := get(t, "POST", d.base+"/reload", "", probeHdr...)
	if code != http.StatusOK {
		t.Fatalf("POST /reload: %d %s", code, body)
	}
	for _, want := range []string{`"added":[]`, `"removed":[]`, `"changed":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("reload answer %s lacks %s", body, want)
		}
	}
	if fs.backends[0].closed.Load() || fs.count("slack") != 1 {
		t.Fatal("an unchanged reload closed a backend")
	}
	mustCall(t, slack, "slack_tool")
}

func TestReload_ToolsOnlyChangeUpdatesTheListInPlace(t *testing.T) {
	fs := newFakeStarter()
	d := startReloadDaemon(t, fs, newSet("slack"))
	slack := httpClient(t, endpointURL(d.base, "slack"))
	defer func() { _ = slack.Close() }()
	mustCall(t, slack, "slack_tool")

	d.src.change(func(s *ServerSet) { s.Catalog.Set("slack", toolJSON("slack_tool", "post_v2"), time.Unix(1, 0)) })
	if res := d.reload(t); len(res.Added)+len(res.Removed)+len(res.Changed) != 0 {
		t.Fatalf("reload = %+v, want nothing reported for a catalog-only change", res)
	}
	if got := toolNames(t, slack); !sameNames(got, []string{"post_v2", "slack_tool"}) {
		t.Fatalf("open session lists %v after the reload, want the new tools", got)
	}
	if fs.backends[0].closed.Load() {
		t.Fatal("a catalog-only change restarted the backend")
	}
}

func TestReload_NeedsTheProbeHeaderAndIsLocalOnly(t *testing.T) {
	d := startReloadDaemon(t, newFakeStarter(), newSet("slack"))
	if code, _ := get(t, "POST", d.base+"/reload", ""); code != http.StatusForbidden {
		t.Fatalf("reload without %s: %d, want 403", ProbeHeader, code)
	}
	if code, _ := get(t, "POST", d.base+"/reload", "evil.example", probeHdr...); code != http.StatusForbidden {
		t.Fatalf("reload with a foreign Host: %d, want 403", code)
	}
	if code, _ := get(t, "POST", d.base+"/reload", "", "X-Aimux-Probe", "1", "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site reload: %d, want 403", code)
	}
	if code, _ := get(t, "GET", d.base+"/reload", "", probeHdr...); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /reload: %d, want 405", code)
	}
	if n := d.src.loadCount(); n != 0 {
		t.Fatalf("refused reloads read the servers file %d times", n)
	}
}

func TestReload_LoadFailureChangesNothing(t *testing.T) {
	fs := newFakeStarter()
	d := startReloadDaemon(t, fs, newSet("slack"))
	slack := httpClient(t, endpointURL(d.base, "slack"))
	defer func() { _ = slack.Close() }()
	mustCall(t, slack, "slack_tool")

	d.src.change(func(s *ServerSet) { *s = newSet("github") })
	d.src.setErr(errors.New("parse servers file: unexpected end of JSON input"))
	if _, err := ReloadDaemon(d.port); err == nil || !strings.Contains(err.Error(), "unexpected end") {
		t.Fatalf("ReloadDaemon = %v, want the load error", err)
	}
	mustCall(t, slack, "slack_tool")
	if fs.backends[0].closed.Load() || len(d.src.appliedSets()) != 0 {
		t.Fatal("a failed reload changed something")
	}
	// The next good reload compares with what is really served.
	d.src.setErr(nil)
	res := d.reload(t)
	if !sameNames(res.Added, []string{"github"}) || !sameNames(res.Removed, []string{"slack"}) {
		t.Fatalf("reload after a failed one = %+v, want slack removed and github added", res)
	}
}

func TestReload_StatusFollowsTheNewSet(t *testing.T) {
	d := startReloadDaemon(t, newFakeStarter(), newSet("slack", "github"))
	d.src.change(func(s *ServerSet) {
		delete(s.Configs, "github")
		delete(s.Catalog.Servers, "github")
		s.Configs["linear"] = ServerConfig{Command: "linear-mcp"}
		s.Catalog.Set("linear", toolJSON("issue"), time.Unix(0, 0))
	})
	d.reload(t)
	_, body := get(t, "GET", d.base+"/status", "")
	var rep StatusReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range rep.Servers {
		names = append(names, s.Server)
		if s.Server == "linear" && s.Registered != ToolsFingerprint(toolJSON("issue")) {
			t.Fatalf("linear registered %q, want the fingerprint of its served tools", s.Registered)
		}
	}
	if !sameNames(names, []string{"linear", "slack"}) {
		t.Fatalf("/status lists %v after the reload, want [linear slack]", names)
	}
}

func TestReloadDaemon_ReportsWhenNoDaemonAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if _, err := ReloadDaemon(port); err == nil || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("ReloadDaemon with nothing listening = %v, want an error naming the port", err)
	}
}

func TestReloadDaemon_ErrorStatusIsAnError(t *testing.T) {
	// A daemon without reload support (an older binary) answers 404.
	base, _ := startDaemon(t, newFakeStarter(), DaemonOptions{})
	port, _ := strconv.Atoi(base[strings.LastIndex(base, ":")+1:])
	if _, err := ReloadDaemon(port); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("ReloadDaemon against a daemon without /reload = %v, want an error naming 404", err)
	}
}

func TestLiveStarter_UsesTheLatestDefinitions(t *testing.T) {
	l := NewLiveStarter(map[string]ServerConfig{}, nil)
	if _, err := l.Start(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "no MCP server named") {
		t.Fatalf("start of an unconfigured server = %v", err)
	}
	l.Set(map[string]ServerConfig{"x": {Command: "/nonexistent/aimux-test-mcp"}}, nil)
	_, err := l.Start(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "no MCP server named") {
		t.Fatalf("start after Set = %v, want it to try the new definition", err)
	}
}

func TestCatalogUpdater_ReplaceSwapsTheCatalog(t *testing.T) {
	path := t.TempDir() + "/catalog.json"
	old := &Catalog{}
	old.Set("slack", toolJSON("a"), time.Unix(0, 0))
	u := NewCatalogUpdater(old, path)
	next := &Catalog{}
	next.Set("github", toolJSON("b", "c"), time.Unix(0, 0))
	if err := next.Save(path); err != nil { // a reload replaces the copy with the file it read
		t.Fatal(err)
	}
	u.Replace(next)
	if u.ToolCount("github") != 2 || u.ToolCount("slack") != 0 {
		t.Fatalf("after Replace: github %d tools, slack %d; want the new catalog", u.ToolCount("github"), u.ToolCount("slack"))
	}
	next.Set("github", nil, time.Unix(0, 0)) // the caller's copy is not shared
	if u.ToolCount("github") != 2 {
		t.Fatal("Replace kept a reference to the caller's catalog")
	}
	b := &listBackend{tools: toolJSON("d")}
	if err := u.Refresh(context.Background(), "linear", b); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Servers["github"].Tools) != 2 || len(saved.Servers["linear"].Tools) != 1 || len(saved.Servers["slack"].Tools) != 0 {
		t.Fatalf("saved catalog %+v, want the file's catalog plus the refreshed server", saved.Servers)
	}
}

// startServeLike wires an updater and a reloader to one catalog loaded
// from a file, as serve does, and returns the updater, the daemon's base URL
// and its port.
func startServeLike(t *testing.T) (*CatalogUpdater, string, int) {
	t.Helper()
	path := t.TempDir() + "/catalog.json"
	cat := &Catalog{}
	cat.Set("slack", toolJSON("old_tool"), time.Unix(0, 0))
	if err := cat.Save(path); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	configs := map[string]ServerConfig{"slack": {Command: "slack-mcp"}}
	set := ServerSet{Configs: configs, Catalog: cat}
	updater := NewCatalogUpdater(set.Catalog, path)
	load := func() (ServerSet, error) {
		c, err := LoadCatalog(path)
		return ServerSet{Configs: configs, Catalog: c}, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(newFakeStarter().start, PoolOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	opt := DaemonOptions{Reload: &ReloadOptions{Current: set, Load: load, Apply: func(n ServerSet) { updater.Replace(n.Catalog) }}}
	servers := NewServers(set.Catalog, pool) // built before any refresh, as serve does
	go func() { done <- Serve(ctx, ln, servers, pool, opt) }()
	t.Cleanup(func() { cancel(); <-done })
	return updater, "http://" + ln.Addr().String(), ln.Addr().(*net.TCPAddr).Port
}

// A backend start that refreshed the catalog before the first reload must
// not hide the refreshed tools from that reload's diff.
func TestReload_CatalogRefreshedByAStartReachesTheEndpoint(t *testing.T) {
	updater, base, port := startServeLike(t)
	if err := updater.Refresh(context.Background(), "slack", &listBackend{tools: toolJSON("new_tool")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReloadDaemon(port); err != nil {
		t.Fatal(err)
	}
	c := httpClient(t, endpointURL(base, "slack"))
	defer func() { _ = c.Close() }()
	if got := toolNames(t, c); !sameNames(got, []string{"new_tool"}) {
		t.Fatalf("/mcp/slack lists %v after the reload, want [new_tool]", got)
	}
}

// Refreshes and the first reload at once: -race flags a shared catalog.
func TestReload_ConcurrentWithCatalogRefreshes(t *testing.T) {
	updater, base, port := startServeLike(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = updater.Refresh(context.Background(), "slack", &listBackend{tools: toolJSON("new_tool")})
		}
	}()
	if _, err := ReloadDaemon(port); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := ReloadDaemon(port); err != nil {
		t.Fatal(err)
	}
	c := httpClient(t, endpointURL(base, "slack"))
	defer func() { _ = c.Close() }()
	if got := toolNames(t, c); !sameNames(got, []string{"new_tool"}) {
		t.Fatalf("/mcp/slack lists %v after the reloads, want [new_tool]", got)
	}
}

func TestReload_LogsSkippedCatalogTools(t *testing.T) {
	var log syncBuffer
	set := newSet("slack")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	src := &setSource{set: copySet(set)}
	pool := NewPool(newFakeStarter().start, PoolOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	opt := DaemonOptions{Log: &log, Reload: &ReloadOptions{Current: copySet(set), Load: src.load}}
	go func() { done <- Serve(ctx, ln, NewServers(set.Catalog, pool), pool, opt) }()
	t.Cleanup(func() { cancel(); <-done })

	src.change(func(s *ServerSet) {
		s.Configs["bad"] = ServerConfig{Command: "bad-mcp", Env: map[string]string{"TOKEN": "s3cret"}}
		s.Catalog.Set("bad", []json.RawMessage{json.RawMessage(`{"inputSchema":{}}`), toolJSON("ok")[0]}, time.Unix(0, 0))
	})
	if _, err := ReloadDaemon(ln.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "skipped bad: decode tool: missing name") {
		t.Fatalf("log = %q, want one line naming the skipped tool of bad", out)
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("log shows an env value: %q", out)
	}
}

func TestSkippedTools_ListsUndecodableCatalogTools(t *testing.T) {
	got := SkippedTools(testCatalog())
	if len(got) != 1 || got[0] != "bad: decode tool: missing name" {
		t.Fatalf("SkippedTools = %v, want the one malformed tool of bad", got)
	}
	if got := SkippedTools(newSet("slack").Catalog); len(got) != 0 {
		t.Fatalf("SkippedTools of a clean catalog = %v", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
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

// A configured server without an endpoint (no catalog entry) may still run,
// started by a probe. When a reload both changes its definition and gives
// it its first catalog entry, the new endpoint must not forward to that old
// backend: it is retired, and the first call starts the new definition.
func TestReload_ChangedServerGainingItsEndpointRetiresTheOldBackend(t *testing.T) {
	fs := newFakeStarter()
	set := newSet("a")
	set.Configs["b"] = ServerConfig{Command: "b-mcp"}
	d := startReloadDaemon(t, fs, set)
	lease, err := d.pool.Lease(context.Background(), "b") // a probe starts b
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	if fs.count("b") != 1 {
		t.Fatalf("b started %d times", fs.count("b"))
	}

	d.src.change(func(s *ServerSet) {
		s.Configs["b"] = ServerConfig{Command: "b2-mcp"}
		s.Catalog.Set("b", toolJSON("b_tool"), time.Unix(0, 0))
	})
	res := d.reload(t)
	if !sameNames(res.Added, []string{"b"}) {
		t.Fatalf("result %+v, want b added", res)
	}
	b := httpClient(t, endpointURL(d.base, "b"))
	defer func() { _ = b.Close() }()
	mustCall(t, b, "b_tool")
	if fs.count("b") != 2 {
		t.Fatalf("b started %d times, want a fresh start for the new definition", fs.count("b"))
	}
}

// A changed server's old backend is retired before its new tools are
// published: a client calling a newly advertised tool the moment it sees
// tools/list_changed reaches a fresh backend, while a call already running
// on the old one still completes.
func TestReload_ChangedToolsPublishedOnlyAfterTheOldBackendIsRetired(t *testing.T) {
	fs := newFakeStarter()
	release := make(chan struct{})
	var block atomic.Bool
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		if block.Load() {
			<-release
		}
		return mcp.NewToolResultText("ok"), nil
	}
	d := startReloadDaemon(t, fs, newSet("slack"))
	ctx := context.Background()

	block.Store(true)
	inflight := make(chan *mcp.CallToolResult, 1)
	go func() { inflight <- d.pool.Call(ctx, "slack", "", req("slack_tool")) }()
	for deadline := time.Now().Add(5 * time.Second); fs.count("slack") == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first call never started a backend")
		}
	}
	time.Sleep(20 * time.Millisecond) // the call is now waiting inside the old backend
	block.Store(false)

	var atPublish *mcp.CallToolResult
	hook := func(name string) {
		if name == "slack" {
			atPublish = d.pool.Call(ctx, "slack", "", req("new_tool"))
		}
	}
	reloadToolsPublished.Store(&hook)
	defer reloadToolsPublished.Store(nil)
	d.src.change(func(s *ServerSet) {
		s.Configs["slack"] = ServerConfig{Command: "slack2-mcp"}
		s.Catalog.Set("slack", toolJSON("slack_tool", "new_tool"), time.Unix(0, 0))
	})
	res := d.reload(t)
	if !sameNames(res.Changed, []string{"slack"}) || atPublish == nil {
		t.Fatalf("result %+v, call at publish %v", res, atPublish)
	}
	if fs.count("slack") != 2 {
		t.Fatalf("slack started %d times: the call at publish reached the old backend", fs.count("slack"))
	}
	fs.mu.Lock()
	old := fs.backends[0]
	fs.mu.Unlock()
	old.mu.Lock()
	oldGot := old.lastReq.Params.Name
	old.mu.Unlock()
	if oldGot != "slack_tool" {
		t.Fatalf("the old backend received %q", oldGot)
	}
	close(release)
	if got := resultText(t, <-inflight); got != "ok" {
		t.Fatalf("in-flight call on the old backend = %q", got)
	}
}
