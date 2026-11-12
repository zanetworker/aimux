package mcpproxy

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"
)

func allowToolJSON(name string) json.RawMessage {
	return json.RawMessage(`{"name":"` + name + `","inputSchema":{"type":"object"}}`)
}

func allowCatalog() *Catalog {
	c := &Catalog{}
	at := time.Now()
	c.Set("slack", []json.RawMessage{allowToolJSON("search_messages"), allowToolJSON("post_message")}, at)
	c.Set("jira", []json.RawMessage{allowToolJSON("search_issues_jql")}, at)
	return c
}

func entryNames(t *testing.T, e CatalogEntry) []string {
	t.Helper()
	var out []string
	for _, r := range e.Tools {
		tool, err := DecodeTool(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tool.Name)
	}
	return out
}

func TestAllowedKeepsOnlyListedTools(t *testing.T) {
	cat := allowCatalog()
	got := cat.Allowed(Allowlist{"slack": {"search_messages"}})
	if len(got.Servers) != 1 {
		t.Fatalf("servers = %v, want only slack", got.Servers)
	}
	if names := entryNames(t, got.Servers["slack"]); !reflect.DeepEqual(names, []string{"search_messages"}) {
		t.Fatalf("slack tools = %v", names)
	}
	if len(cat.Servers["slack"].Tools) != 2 || len(cat.Servers) != 2 {
		t.Fatal("Allowed changed the receiver")
	}
}

func TestAllowedKeepsCatalogOrder(t *testing.T) {
	got := allowCatalog().Allowed(Allowlist{"slack": {"post_message", "search_messages"}})
	want := []string{"search_messages", "post_message"}
	if names := entryNames(t, got.Servers["slack"]); !reflect.DeepEqual(names, want) {
		t.Fatalf("tools = %v, want catalog order %v", names, want)
	}
}

func TestAllowedIgnoresUnknownTool(t *testing.T) {
	got := allowCatalog().Allowed(Allowlist{"slack": {"search_messages", "nope"}, "ghost": {"x"}})
	if names := entryNames(t, got.Servers["slack"]); !reflect.DeepEqual(names, []string{"search_messages"}) {
		t.Fatalf("slack tools = %v", names)
	}
	if _, ok := got.Servers["ghost"]; ok {
		t.Fatal("server absent from the catalog appeared")
	}
}

func TestAllowedEmptyListAllowsNothing(t *testing.T) {
	if got := allowCatalog().Allowed(Allowlist{"slack": {}}); len(got.Servers) != 0 {
		t.Fatalf("servers = %v, want none", got.Servers)
	}
}

type rejectRec struct {
	mu    sync.Mutex
	calls [][2]string
}

func (r *rejectRec) log(server, tool string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]string{server, tool})
}

func (r *rejectRec) snapshot() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][2]string(nil), r.calls...)
}

func TestServerRejectsUnlistedTool(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	rec := &rejectRec{}
	srvs := NewServers(allowCatalog().Allowed(Allowlist{"slack": {"search_messages"}}), pool, WithRejectLog(rec.log))
	if _, ok := srvs["jira"]; ok {
		t.Fatal("jira server exists")
	}
	c := connectTo(t, srvs["slack"])
	if names := toolNames(t, c); !reflect.DeepEqual(names, []string{"search_messages"}) {
		t.Fatalf("tools/list = %v", names)
	}
	res, err := c.CallTool(context.Background(), req("post_message"))
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("unlisted call succeeded: %+v", res)
	}
	if !reflect.DeepEqual(rec.snapshot(), [][2]string{{"slack", "post_message"}}) {
		t.Fatalf("reject log = %v", rec.snapshot())
	}
	if fs.count("slack") != 0 {
		t.Fatal("rejected call started a backend")
	}
}

func TestServerRejectLogSilentOnSuccess(t *testing.T) {
	fs := newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{})
	defer pool.Close()
	rec := &rejectRec{}
	srvs := NewServers(allowCatalog().Allowed(Allowlist{"slack": {"search_messages"}}), pool, WithRejectLog(rec.log))
	c := connectTo(t, srvs["slack"])
	if res, err := c.CallTool(context.Background(), req("search_messages")); err != nil || res.IsError {
		t.Fatalf("allowed call failed: %v %+v", err, res)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatalf("reject log = %v, want none", rec.snapshot())
	}
}
