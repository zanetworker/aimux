package toolgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// Observed is what the gateway itself listed, with no listing of its own:
// nothing before it starts; after Start each built backend's allowed tools
// (or why it is down) with the endpoint it listed them from; a Refresh that
// drops a source drops what was observed of it.
func TestReadonlyServer_Observed(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, home := readonlyRig(t, &logw)
	notes := t.TempDir()
	if err := os.WriteFile(filepath.Join(notes, "a.md"), []byte("# a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  notes: {type: local-notes, root: "`+notes+`"}
  jira: {type: mcp-http, url: "http://127.0.0.1:1/mcp"}
sets:
  notes:readonly: {source: notes, tools: [wiki_search]}
  jira:readonly: {source: jira, tools: [search]}
`)
	r.Build, r.Rebuild = mcpproxy.BuildReadonly, mcpproxy.BuildReadonlyBackends
	if got := r.Observed(); len(got) != 0 {
		t.Fatalf("before Start: observed %v, want nothing (unknown)", got)
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	opt := <-served
	opt.AddServers(func(string, *server.MCPServer) {}, func(string) bool { return true })

	got := r.Observed()
	n, j := got["notes"], got["jira"]
	if n.Err != nil || n.Endpoint != notes || !slices.Equal(keys(n.Tools), []string{"wiki_search"}) {
		t.Fatalf("notes observed %+v, want wiki_search listed from %s (wiki_read is not in the set)", n, notes)
	}
	if j.Err == nil || j.Endpoint != "http://127.0.0.1:1/mcp" || len(j.Tools) != 0 {
		t.Fatalf("jira observed %+v, want down at its endpoint", j)
	}

	writeHomeFile(t, toolreg.DefaultPath(home), `version: 1
sources:
  notes: {type: local-notes, root: "`+notes+`"}
sets:
  notes:readonly: {source: notes, tools: [wiki_search, wiki_read]}
`)
	reload(t, r)
	r.Refresh(context.Background())
	got = r.Observed()
	if _, ok := got["jira"]; ok {
		t.Fatalf("after dropping jira: observed %v, want it gone", got)
	}
	if !slices.Equal(keys(got["notes"].Tools), []string{"wiki_read", "wiki_search"}) {
		t.Fatalf("after the set grew: notes observed %v", keys(got["notes"].Tools))
	}
	got["notes"].Tools["x"] = nil
	if _, leaked := r.Observed()["notes"].Tools["x"]; leaked {
		t.Fatal("Observed returned the gateway's own map")
	}
}

// A Build that never lists (no catalog seen) observes only what is down:
// a backend it did not list stays unknown rather than listing no tools.
func TestReadonlyServer_ObservedWithoutListing(t *testing.T) {
	var logw bytes.Buffer
	r, served, _, _ := readonlyRig(t, &logw) // its Build reports jira down and lists nothing
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	<-served
	got := r.Observed()
	if len(got) != 1 || got["jira"].Err == nil {
		t.Fatalf("observed %+v, want only jira, down", got)
	}
}

// LiveFor drops what the gateway listed from another endpoint than reg now
// names (the observation says nothing about the new one): unknown.
func TestLiveFor(t *testing.T) {
	reg := toolreg.Registry{Sources: map[string]toolreg.Source{
		"jira":  {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
		"slack": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8002/mcp"},
	}}
	obs := map[string]toolgateway.Observed{
		"jira":  {LiveSource: toolgateway.LiveSource{Tools: map[string]json.RawMessage{}}, Endpoint: "http://127.0.0.1:8001/mcp"},
		"slack": {LiveSource: toolgateway.LiveSource{Tools: map[string]json.RawMessage{}}, Endpoint: "http://127.0.0.1:9999/mcp"},
		"gone":  {Endpoint: "http://127.0.0.1:7/mcp"},
	}
	live := toolgateway.LiveFor(reg, obs)
	if _, ok := live["jira"]; !ok || len(live) != 1 {
		t.Fatalf("LiveFor = %v, want jira only (slack moved, gone is not in the registry)", live)
	}
}

func keys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
