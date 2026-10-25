package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
