package mcpproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
