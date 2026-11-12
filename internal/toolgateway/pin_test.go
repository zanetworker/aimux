package toolgateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

func mustPin(t *testing.T, raw string) string {
	t.Helper()
	p, err := PinTool(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("PinTool(%s): %v", raw, err)
	}
	return p
}

func TestPinTool_IgnoresDescriptionTitleAndKeyOrder(t *testing.T) {
	a := mustPin(t, `{"name":"search","description":"Find things","title":"Search","inputSchema":{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"number"}}},"annotations":{"readOnlyHint":true}}`)
	b := mustPin(t, `{ "annotations":{"readOnlyHint":true}, "inputSchema":{"properties":{"limit":{"type":"number"},"query":{"type":"string"}},"type":"object"}, "title":"Other", "description":"Changed text", "name":"search" }`)
	if a != b {
		t.Fatalf("pins differ: %s vs %s", a, b)
	}
	if len(a) < len("sha256:")+64 || a[:7] != "sha256:" {
		t.Fatalf("pin format = %q", a)
	}
}

func TestPinTool_ChangesWithSchemaOrAnnotations(t *testing.T) {
	base := `{"name":"search","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}},"annotations":{"readOnlyHint":true}}`
	variants := map[string]string{
		"input type":  `{"name":"search","inputSchema":{"type":"object","properties":{"query":{"type":"number"}}},"annotations":{"readOnlyHint":true}}`,
		"output":      `{"name":"search","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}},"outputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`,
		"annotations": `{"name":"search","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}},"annotations":{"readOnlyHint":false}}`,
	}
	want := mustPin(t, base)
	for name, raw := range variants {
		if got := mustPin(t, raw); got == want {
			t.Errorf("%s: pin did not change", name)
		}
	}
}

func TestPinTool_RejectsNoName(t *testing.T) {
	for _, raw := range []string{`{"inputSchema":{}}`, `{"name":""}`, `not json`} {
		if _, err := PinTool(json.RawMessage(raw)); err == nil {
			t.Errorf("PinTool(%s) = nil error", raw)
		}
	}
}

func catalogOf(server string, tools ...string) *mcpproxy.Catalog {
	c := &mcpproxy.Catalog{}
	var raws []json.RawMessage
	for _, tl := range tools {
		raws = append(raws, json.RawMessage(tl))
	}
	c.Set(server, raws, time.Unix(0, 0))
	return c
}

func TestPins_FilterDropsChangedAndUnpinned(t *testing.T) {
	search := `{"name":"wiki_search","inputSchema":{"type":"object"}}`
	read := `{"name":"wiki_read","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}`
	readChanged := `{"name":"wiki_read","inputSchema":{"type":"object","properties":{"path":{"type":"number"}}}}`
	newTool := `{"name":"new_tool","inputSchema":{"type":"object"}}`
	pins := Pins{"wiki": {"wiki_search": mustPin(t, search), "wiki_read": mustPin(t, read)}}

	got := pins.Filter(catalogOf("wiki", search, readChanged, newTool))
	tools := got.Servers["wiki"].Tools
	if len(tools) != 1 {
		t.Fatalf("kept %d tools: %s", len(tools), tools)
	}
	if p := mustPin(t, string(tools[0])); p != pins["wiki"]["wiki_search"] {
		t.Fatalf("kept the wrong tool: %s", tools[0])
	}

	if empty := (Pins{}).Filter(catalogOf("wiki", search)); len(empty.Servers) != 0 {
		t.Fatalf("empty pins kept servers: %v", empty.Servers)
	}
}

func TestPinStore_MissingFileIsEmpty(t *testing.T) {
	s := PinStore{Path: filepath.Join(t.TempDir(), "tools", "pins.json")}
	p, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || len(p) != 0 {
		t.Fatalf("Load = %#v, want empty non-nil Pins", p)
	}
}

func TestPinStore_SaveIs0600AndRoundTrips(t *testing.T) {
	s := PinStore{Path: filepath.Join(t.TempDir(), "tools", "pins.json")}
	want := Pins{"jira": {"search": "sha256:abc"}}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	got, err := s.Load()
	if err != nil || got["jira"]["search"] != "sha256:abc" {
		t.Fatalf("Load = %v, %v", got, err)
	}
}

func TestPinStore_BadJSONIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (PinStore{Path: path}).Load(); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load = %v, want a parse error", err)
	}
}
