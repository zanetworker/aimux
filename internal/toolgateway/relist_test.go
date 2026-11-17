package toolgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// relistRig serves wiki and jira, each built from a catalog Filter sees
// (so Observed holds their tools), with Rebuild replaced by rebuild.
func relistRig(t *testing.T, rebuild func(ctx context.Context, names []string, pins mcpproxy.ToolPins) (map[string]*server.MCPServer, map[string]error)) (*toolgateway.ReadonlyServer, *toolgateway.PinStore, *atomic.Int32) {
	t.Helper()
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  wiki: {type: mcp-http, url: "http://127.0.0.1:1/wiki"}
  jira: {type: mcp-http, url: "http://127.0.0.1:1/jira"}
sets:
  wiki:readonly: {source: wiki, tools: [search]}
  jira:readonly: {source: jira, tools: [get]}
`)
	store := &toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	def := func(n string) json.RawMessage {
		return json.RawMessage(`{"name":"` + n + `","inputSchema":{"type":"object"}}`)
	}
	pin := func(n string) string { p, _ := toolgateway.PinTool(def(n)); return p }
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": pin("search")}, "jira": {"get": pin("get")}})); err != nil {
		t.Fatal(err)
	}
	catalog := func(names []string) *mcpproxy.Catalog {
		cat := &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{}}
		for _, n := range names {
			tool := map[string]string{"wiki": "search", "jira": "get"}[n]
			cat.Set(n, []json.RawMessage{def(tool)}, time.Now())
		}
		return cat
	}
	r.Build = func(_ context.Context, _ mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		pins.Filter(catalog([]string{"wiki", "jira"}))
		return map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1"), "jira": server.NewMCPServer("jira", "1")}, nil, nil, nil
	}
	calls := &atomic.Int32{}
	r.Rebuild = func(ctx context.Context, _ mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		calls.Add(1)
		servers, errs := rebuild(ctx, names, pins)
		var ok []string
		for _, n := range names {
			if errs[n] == nil {
				ok = append(ok, n)
			}
		}
		pins.Filter(catalog(ok))
		return servers, errs
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	opt := <-served
	opt.AddServers(func(string, *server.MCPServer) {}, func(string) bool { return true })
	return r, store, calls
}

func servers(names []string) map[string]*server.MCPServer {
	out := map[string]*server.MCPServer{}
	for _, n := range names {
		out[n] = server.NewMCPServer(n, "2")
	}
	return out
}

// The minute relist never holds up a turn: Refresh returns at once while a
// slow relist (a backend taking 10 s) runs, at most one relist is in
// flight, and a turn that brings a newer snapshot is applied without
// waiting for it.
func TestReadonlyServer_SlowRelistNeverBlocksATurn(t *testing.T) {
	slow := make(chan struct{})
	var once sync.Once
	r, store, calls := relistRig(t, func(ctx context.Context, names []string, _ mcpproxy.ToolPins) (map[string]*server.MCPServer, map[string]error) {
		first := false
		once.Do(func() { first = true })
		if first {
			select {
			case <-slow:
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
			}
		}
		return servers(names), nil
	})
	defer close(slow)

	start := time.Now()
	r.Refresh(context.Background()) // starts the slow relist
	r.Refresh(context.Background()) // a second turn: no second relist
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("turns waited %v for the relist", d)
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d relists in flight, want 1", n)
	}

	// A re-pin arrives while the relist is still running: that turn's
	// rebuild is applied at once.
	before := r.Applied().Version()
	if err := store.Save(hashOnly(toolgateway.Pins{"wiki": {"search": "sha256:other"}})); err != nil {
		t.Fatal(err)
	}
	reload(t, r)
	start = time.Now()
	r.Refresh(context.Background())
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("the newer snapshot waited %v for the relist", d)
	}
	if r.Applied().Version() == before || r.Stale() {
		t.Fatal("the newer snapshot was not applied")
	}
}

// A relist that fails for one backend marks only that one: it is still
// served, Observed keeps its last tools flagged RelistFailed, LiveFor
// leaves it out (unknown), and the backend that listed is updated.
func TestReadonlyServer_RelistFailureMarksOnlyThatBackend(t *testing.T) {
	r, _, calls := relistRig(t, func(_ context.Context, names []string, _ mcpproxy.ToolPins) (map[string]*server.MCPServer, map[string]error) {
		errs := map[string]error{}
		var ok []string
		for _, n := range names {
			if n == "jira" {
				errs[n] = context.DeadlineExceeded
				continue
			}
			ok = append(ok, n)
		}
		return servers(ok), errs
	})
	before := r.Observed()
	if before["jira"].Tools == nil || before["wiki"].Tools == nil {
		t.Fatalf("start observed %+v", before)
	}
	r.Refresh(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 || r.Observed()["jira"].RelistFailed == false {
		if time.Now().After(deadline) {
			t.Fatalf("relist did not record: %+v", r.Observed())
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := r.Observed()
	j, w := got["jira"], got["wiki"]
	if !j.RelistFailed || j.Err != nil || len(j.Tools) != 1 {
		t.Fatalf("jira observed %+v, want its last tools, flagged relist failed, not down", j)
	}
	if w.RelistFailed || w.Err != nil || len(w.Tools) != 1 {
		t.Fatalf("wiki observed %+v, want listed", w)
	}
	reg := r.Applied().Registry()
	live := toolgateway.LiveFor(reg, got)
	if _, ok := live["jira"]; ok {
		t.Fatal("LiveFor kept jira: its relist failed, so it is unknown")
	}
	if _, ok := live["wiki"]; !ok {
		t.Fatal("LiveFor dropped wiki")
	}
	if failed := toolgateway.RelistFailedFor(reg, got); !failed["jira"] || failed["wiki"] {
		t.Fatalf("RelistFailedFor = %v, want jira only", failed)
	}
}

// Turns during a running relist start none of their own, not even queued:
// once it finishes, exactly one relist has run.
func TestReadonlyServer_RelistIsNotQueuedBehindARunningOne(t *testing.T) {
	release := make(chan struct{})
	r, _, calls := relistRig(t, func(ctx context.Context, names []string, _ mcpproxy.ToolPins) (map[string]*server.MCPServer, map[string]error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return servers(names), nil
	})
	for range 5 {
		r.Refresh(context.Background())
	}
	close(release)
	r.WaitBackground()
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d relists ran for 5 turns during one, want 1", n)
	}
}

// A lease's fresh listing retries its backend when it was down at start (a
// backend that came up after the worker started), not only the minute tick.
func TestReadonlyServer_RelistNowRetriesALeasesDownBackend(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  wiki: {type: mcp-http, url: "http://127.0.0.1:1/wiki"}
  jira: {type: mcp-http, url: "http://127.0.0.1:1/jira"}
sets:
  wiki:readonly: {source: wiki, tools: [search]}
  jira:readonly: {source: jira, tools: [get]}
`)
	r.Build = func(_ context.Context, _ mcpproxy.ReadonlyConfig, _ mcpproxy.ToolPins, _ func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		return map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1")}, nil, map[string]error{"jira": errors.New("connection refused")}, nil
	}
	var mu sync.Mutex
	var rebuilt [][]string
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		mu.Lock()
		rebuilt = append(rebuilt, append([]string(nil), names...))
		mu.Unlock()
		return servers(names), nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	opt := <-served
	var added []string
	opt.AddServers(func(n string, _ *server.MCPServer) { mu.Lock(); added = append(added, n); mu.Unlock() }, func(string) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.RelistNow(ctx, []string{"jira"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, names := range rebuilt {
		found = found || slices.Contains(names, "jira")
	}
	if !found {
		t.Fatalf("rebuilt %v: the lease's down backend jira was not retried", rebuilt)
	}
	if !slices.Contains(added, "jira") {
		t.Fatalf("added %v: the recovered backend's endpoint was not published", added)
	}
	if _, down := r.Backends(); slices.Contains(down, "jira") {
		t.Fatalf("down %v: jira still marked down after it recovered", down)
	}
}
