package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadCatalog_MissingFileIsEmpty(t *testing.T) {
	c, err := LoadCatalog(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || c == nil || len(c.Servers) != 0 {
		t.Fatalf("got %v, %v; want empty catalog and nil error", c, err)
	}
}

func TestCatalog_SaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	c := &Catalog{}
	c.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"post","inputSchema":{"type":"object"}}`)}, at)
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog file mode %v, err %v; want 0600", info.Mode().Perm(), err)
	}
	if left, _ := filepath.Glob(path + ".tmp*"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	got, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Servers["slack"]
	if len(e.Tools) != 1 || !e.FetchedAt.Equal(at) || !strings.Contains(string(e.Tools[0]), `"post"`) {
		t.Fatalf("round trip lost data: %+v", e)
	}
}

func TestLoadCatalog_CorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("got err %v, want an error naming %s", err, path)
	}
}

func TestDecodeTool_KeepsRawSchemas(t *testing.T) {
	raw := json.RawMessage(`{"name":"pick","description":"d","inputSchema":{"type":"object","anyOf":[{"required":["a"]},{"required":["b"]}]},"outputSchema":{"type":"object","properties":{"x":{"type":"string"}}}}`)
	tool, err := DecodeTool(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name != "pick" || tool.Description != "d" {
		t.Fatalf("got name %q desc %q", tool.Name, tool.Description)
	}
	if !strings.Contains(string(tool.RawInputSchema), "anyOf") {
		t.Fatalf("top-level anyOf dropped from input schema: %s", tool.RawInputSchema)
	}
	if !strings.Contains(string(tool.RawOutputSchema), `"x"`) {
		t.Fatalf("output schema dropped: %s", tool.RawOutputSchema)
	}
	out, err := json.Marshal(tool)
	if err != nil || !strings.Contains(string(out), "anyOf") {
		t.Fatalf("re-encoded tool lost anyOf: %s, %v", out, err)
	}
}

func TestDecodeTool_RequiresName(t *testing.T) {
	if _, err := DecodeTool(json.RawMessage(`{"inputSchema":{"type":"object"}}`)); err == nil {
		t.Fatal("tool without a name decoded without error")
	}
	if _, err := DecodeTool(json.RawMessage(`[]`)); err == nil {
		t.Fatal("non-object decoded without error")
	}
}

type listingBackend struct {
	fakeBackend
	tools []json.RawMessage
	err   error
}

func (l *listingBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	return l.tools, l.err
}

func TestCatalogUpdater_RecordsToolsWhenABackendStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cat := &Catalog{}
	cat.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"old_tool"}`)}, old)
	update := NewCatalogUpdater(cat, path)

	if err := update.Refresh(context.Background(), "slack", &listingBackend{tools: []json.RawMessage{
		json.RawMessage(`{"name":"post"}`), json.RawMessage(`{"name":"new_tool"}`),
	}}); err != nil {
		t.Fatal(err)
	}

	saved, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	e := saved.Servers["slack"]
	if len(e.Tools) != 2 || !strings.Contains(string(e.Tools[1]), "new_tool") || !e.FetchedAt.After(old) {
		t.Fatalf("saved entry = %+v, want the two fresh tools", e)
	}
	if got := update.Snapshot().Servers["slack"]; len(got.Tools) != 2 {
		t.Fatalf("in-memory catalog not updated: %+v", got)
	}
}

func TestCatalogUpdater_FailedListingKeepsOldEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	cat := &Catalog{}
	cat.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"old_tool"}`)}, time.Now())
	update := NewCatalogUpdater(cat, path)
	if err := update.Refresh(context.Background(), "slack", &listingBackend{err: errors.New("broken pipe")}); err == nil || !strings.Contains(err.Error(), "slack") {
		t.Fatalf("got %v, want an error naming the server", err)
	}
	if got := update.Snapshot().Servers["slack"]; len(got.Tools) != 1 {
		t.Fatalf("a failed listing replaced the entry: %+v", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed listing still wrote the catalog")
	}
}

func TestCatalogUpdater_ConcurrentRefreshes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	update := NewCatalogUpdater(&Catalog{}, path)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := update.Refresh(context.Background(), fmt.Sprintf("s%d", i), &listingBackend{tools: []json.RawMessage{json.RawMessage(`{"name":"t"}`)}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	saved, err := LoadCatalog(path)
	if err != nil || len(saved.Servers) != 10 {
		t.Fatalf("saved %d servers (%v), want all 10", len(saved.Servers), err)
	}
}

func TestCatalog_OnlyFiltersACopy(t *testing.T) {
	c := &Catalog{Servers: map[string]CatalogEntry{
		"keep": {Tools: []json.RawMessage{json.RawMessage(`{"name":"a"}`)}},
		"drop": {Tools: []json.RawMessage{json.RawMessage(`{"name":"b"}`)}},
	}}
	got := c.Only(func(s string) bool { return s == "keep" })
	if len(got.Servers) != 1 || len(got.Servers["keep"].Tools) != 1 {
		t.Fatalf("filtered catalog %+v, want only keep", got.Servers)
	}
	if len(c.Servers) != 2 {
		t.Fatalf("original catalog changed to %+v; it is the one saved to disk", c.Servers)
	}
	if empty := c.Only(func(string) bool { return false }); empty.Servers == nil || len(empty.Servers) != 0 {
		t.Fatalf("nothing kept: got %+v, want an empty, non-nil map", empty.Servers)
	}
}

func TestCatalogUpdater_ToolCount(t *testing.T) {
	cat := &Catalog{}
	cat.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"a"}`), json.RawMessage(`{"name":"b"}`)}, time.Now())
	u := NewCatalogUpdater(cat, filepath.Join(t.TempDir(), "c.json"))
	if got := u.ToolCount("slack"); got != 2 {
		t.Fatalf("ToolCount(slack) = %d, want 2", got)
	}
	if got := u.ToolCount("unknown"); got != 0 {
		t.Fatalf("ToolCount(unknown) = %d, want 0", got)
	}
	if err := u.Refresh(context.Background(), "slack", &stubLister{n: 3}); err != nil {
		t.Fatal(err)
	}
	if got := u.ToolCount("slack"); got != 3 {
		t.Fatalf("ToolCount after refresh = %d, want 3", got)
	}
}

type stubLister struct {
	fakeBackend
	n int
}

func (s *stubLister) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, s.n)
	for i := range out {
		out[i] = json.RawMessage(`{"name":"t"}`)
	}
	return out, nil
}

func TestToolsFingerprint(t *testing.T) {
	a := []json.RawMessage{json.RawMessage(`{"name":"a","inputSchema":{"type":"object"}}`)}
	spaced := []json.RawMessage{json.RawMessage("{ \"name\": \"a\",\n  \"inputSchema\": {\"type\": \"object\"} }")}
	if ToolsFingerprint(a) != ToolsFingerprint(spaced) {
		t.Fatal("formatting changed the fingerprint")
	}
	b := []json.RawMessage{json.RawMessage(`{"name":"a","inputSchema":{"type":"string"}}`)}
	if ToolsFingerprint(a) == ToolsFingerprint(b) {
		t.Fatal("a schema change kept the fingerprint")
	}
	if ToolsFingerprint(nil) == "" || ToolsFingerprint(nil) == ToolsFingerprint(a) {
		t.Fatal("no tools needs a fingerprint of its own")
	}
	two := []json.RawMessage{a[0], b[0]}
	swapped := []json.RawMessage{b[0], a[0]}
	if ToolsFingerprint(two) == ToolsFingerprint(swapped) {
		t.Fatal("order is part of what the daemon registers")
	}
}

// install's CLI refresh writes the catalog file while the daemon runs; a
// backend starting in the daemon before /reload must not erase that entry
// by saving its older in-memory copy.
func TestCatalogUpdater_RefreshKeepsEntriesWrittenSinceItsCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	cat := &Catalog{}
	cat.Set("slack", toolJSON("post"), time.Now())
	must(t, cat.Save(path))
	update := NewCatalogUpdater(cat, path)

	onDisk, err := LoadCatalog(path) // install's refresh adds a server
	must(t, err)
	onDisk.Set("pypi", toolJSON("search"), time.Now())
	must(t, onDisk.Save(path))

	must(t, update.Refresh(context.Background(), "slack", &listingBackend{tools: toolJSON("post", "read")}))
	saved, err := LoadCatalog(path)
	must(t, err)
	if len(saved.Servers["pypi"].Tools) != 1 || len(saved.Servers["slack"].Tools) != 2 {
		t.Fatalf("saved %v, want pypi kept and slack refreshed", saved.Servers)
	}
	if _, ok := update.Snapshot().Servers["pypi"]; ok {
		t.Fatal("the served copy changed; only a reload replaces it")
	}
}

// defBackend reports the definition it was started with.
type defBackend struct {
	listingBackend
	def ServerConfig
}

func (d *defBackend) Definition() ServerConfig { return d.def }

// A backend started with definition A finishes its startup refresh after
// the server was changed to B (edit or reload): A's tools must not replace
// B's, in memory or on disk.
func TestCatalogUpdater_RefreshFromASupersededDefinitionIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	defA, defB := ServerConfig{Command: "a-mcp"}, ServerConfig{Command: "b-mcp"}
	current := defA
	cat := &Catalog{}
	cat.Set("s", toolJSON("a_tool"), time.Now())
	must(t, cat.Save(path))
	u := NewCatalogUpdater(cat, path)
	u.Current = func(server string) (ServerConfig, bool) { return current, server == "s" }

	// The server changes to B with its tools (edit wrote them, a reload read them).
	current = defB
	next := &Catalog{}
	next.Set("s", toolJSON("b_tool"), time.Now())
	must(t, next.Save(path))
	u.Replace(next)

	must(t, u.Refresh(context.Background(), "s", &defBackend{listingBackend{tools: toolJSON("a_tool")}, defA}))
	saved, err := LoadCatalog(path)
	must(t, err)
	if got := string(saved.Servers["s"].Tools[0]); !strings.Contains(got, "b_tool") {
		t.Fatalf("disk has %s, want B's tools", got)
	}
	if got := string(u.Snapshot().Servers["s"].Tools[0]); !strings.Contains(got, "b_tool") {
		t.Fatalf("memory has %s, want B's tools", got)
	}

	// A backend started with B records its tools.
	must(t, u.Refresh(context.Background(), "s", &defBackend{listingBackend{tools: toolJSON("b_tool", "b_new")}, defB}))
	if saved, _ = LoadCatalog(path); len(saved.Servers["s"].Tools) != 2 {
		t.Fatalf("a current refresh was dropped: %v", saved.Servers["s"])
	}
}

func TestStdioBackend_ReportsItsDefinition(t *testing.T) {
	cfg := testConfigs()["test"]
	b, err := StdioStarter(map[string]ServerConfig{"test": cfg})(context.Background(), "test")
	must(t, err)
	defer func() { _ = b.Close() }()
	d, ok := b.(interface{ Definition() ServerConfig })
	if !ok || !reflect.DeepEqual(d.Definition(), cfg) {
		t.Fatalf("definition %+v (ok %v), want the one it was started with", d, ok)
	}
}

func TestCatalogUpdater_Changes_SignalsOnlyWhenToolsChange(t *testing.T) {
	cat := &Catalog{}
	cat.Set("slack", toolJSON("post"), time.Unix(0, 0))
	update := NewCatalogUpdater(cat, filepath.Join(t.TempDir(), "catalog.json"))
	signaled := func() bool {
		select {
		case <-update.Changes():
			return true
		default:
			return false
		}
	}

	if err := update.Refresh(context.Background(), "slack", &listingBackend{tools: toolJSON("post")}); err != nil {
		t.Fatal(err)
	}
	if signaled() {
		t.Fatal("an unchanged tool list signalled a change")
	}

	if err := update.Refresh(context.Background(), "slack", &listingBackend{err: errors.New("list failed")}); err == nil {
		t.Fatal("want the listing error")
	}
	if signaled() {
		t.Fatal("a failed listing signalled a change")
	}

	if err := update.Refresh(context.Background(), "slack", &listingBackend{tools: toolJSON("post", "learnings_list")}); err != nil {
		t.Fatal(err)
	}
	if err := update.Refresh(context.Background(), "slack", &listingBackend{tools: toolJSON("post", "learnings_list", "third")}); err != nil {
		t.Fatal(err)
	}
	if !signaled() {
		t.Fatal("a changed tool list did not signal")
	}
	if signaled() {
		t.Fatal("two changes before a read should collapse into one signal")
	}
}

// A refresh whose save fails must not count its tools as published: the
// next refresh that saves them still signals the change (Codex review P2).
func TestCatalogUpdater_FailedSaveKeepsTheChangePending(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cat")
	must(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "catalog.json")
	cat := &Catalog{}
	cat.Set("slack", toolJSON("post"), time.Unix(0, 0))
	must(t, cat.Save(path))
	must(t, os.WriteFile(path+".lock", nil, 0o600)) // the lock still opens; only the save fails
	update := NewCatalogUpdater(cat, path)

	must(t, os.Chmod(dir, 0o500)) // #nosec G302 -- a test directory made unwritable on purpose
	err := update.Refresh(context.Background(), "slack", &listingBackend{tools: toolJSON("post", "new_tool")})
	must(t, os.Chmod(dir, 0o700)) // #nosec G302 -- the test directory made writable again
	if err == nil {
		t.Fatal("want the save error")
	}
	select {
	case <-update.Changes():
		t.Fatal("a refresh that was not saved signalled a change")
	default:
	}

	must(t, update.Refresh(context.Background(), "slack", &listingBackend{tools: toolJSON("post", "new_tool")}))
	select {
	case <-update.Changes():
	default:
		t.Fatal("the change whose first save failed was never signalled")
	}
}
