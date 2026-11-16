package toolgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestPinStore_SaveWritesV2At0600AndRoundTrips(t *testing.T) {
	s := PinStore{Path: filepath.Join(t.TempDir(), "tools", "pins.json")}
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	want := PinFile{"jira": {"search": {Hash: "sha256:abc", Definition: json.RawMessage(`{"name":"search"}`), ApprovedAt: at, Source: "http://127.0.0.1:8001/mcp"}}}
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
	data, _ := os.ReadFile(s.Path)
	var wire struct {
		Version int                                   `json:"version"`
		Sources map[string]map[string]json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.Version != 2 || wire.Sources["jira"]["search"] == nil {
		t.Fatalf("file is not v2 (%v):\n%s", err, data)
	}
	got, err := s.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	r := got["jira"]["search"]
	if r.Hash != "sha256:abc" || string(r.Definition) != `{"name":"search"}` || !r.ApprovedAt.Equal(at) || r.Source != "http://127.0.0.1:8001/mcp" {
		t.Fatalf("LoadFile = %#v", r)
	}
	hashes, err := s.Load()
	if err != nil || hashes["jira"]["search"] != "sha256:abc" {
		t.Fatalf("Load = %v, %v", hashes, err)
	}
}

// A v1 file (server -> tool -> hash) still loads: hashes only, no
// definition, no approval time.
func TestPinStore_ReadsV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	if err := os.WriteFile(path, []byte(`{"jira":{"search":"sha256:abc"},"version":{"x":"sha256:v"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := (PinStore{Path: path}).LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if r := f["jira"]["search"]; r.Hash != "sha256:abc" || r.Definition != nil || !r.ApprovedAt.IsZero() {
		t.Fatalf("v1 record = %#v", r)
	}
	if f["version"]["x"].Hash != "sha256:v" { // a v1 server named "version" is not a format marker
		t.Fatalf("v1 server named version lost: %#v", f)
	}
	p, err := (PinStore{Path: path}).Load()
	if err != nil || p["jira"]["search"] != "sha256:abc" {
		t.Fatalf("Load = %v, %v", p, err)
	}
}

func TestPinStore_UnknownVersionIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	if err := os.WriteFile(path, []byte(`{"version":3,"sources":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (PinStore{Path: path}).LoadFile(); err == nil || !strings.Contains(err.Error(), "version 3") {
		t.Fatalf("LoadFile = %v, want an error naming version 3", err)
	}
}

// Canonical is exactly what PinTool hashes.
func TestCanonical_IsWhatIsHashed(t *testing.T) {
	raw := json.RawMessage(`{"description":"d","title":"t","name":"s","inputSchema":{"type":"object","properties":{"b":{"type":"number"},"a":{"type":"string"}}},"annotations":{"readOnlyHint":true}}`)
	c, err := Canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(c), "description") || strings.Contains(string(c), `"title"`) {
		t.Fatalf("canonical keeps description or title: %s", c)
	}
	sum := sha256.Sum256(c)
	if want := mustPin(t, string(raw)); "sha256:"+hex.EncodeToString(sum[:]) != want {
		t.Fatalf("hash of canonical %s != PinTool %s", c, want)
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

// The first v2 write over a v1 file keeps the v1 bytes in pins.json.v1-backup
// (older builds read only v1); later writes and v2 files make no backup and
// never touch an existing one.
func TestPinStore_SaveKeepsAV1Backup(t *testing.T) {
	dir := t.TempDir()
	s := PinStore{Path: filepath.Join(dir, "pins.json")}
	backup := s.Path + ".v1-backup"
	if err := s.Save(PinFile{"a": {"t": {Hash: "sha256:x"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("no file before: backup %v", err)
	}
	if err := s.Save(PinFile{"a": {"t": {Hash: "sha256:y"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("over v2: backup %v", err)
	}

	v1 := []byte(`{"wiki":{"wiki_read":"sha256:old"}}`)
	if err := os.WriteFile(s.Path, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(PinFile{"wiki": {"wiki_read": {Hash: "sha256:new"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(backup) //nolint:gosec // G304: test temp path
	if err != nil || string(got) != string(v1) {
		t.Fatalf("backup %q, %v; want the v1 bytes", got, err)
	}
	if fi, _ := os.Stat(backup); fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v", fi.Mode().Perm())
	}
	if err := os.WriteFile(s.Path, []byte(`{"other":{"t":"sha256:z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(PinFile{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(backup); string(got) != string(v1) { //nolint:gosec // G304: test temp path
		t.Fatalf("an existing backup was replaced: %q", got)
	}
}
