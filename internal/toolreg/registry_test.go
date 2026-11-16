package toolreg

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(p string) ([]byte, error) {
	return os.ReadFile(p) //nolint:gosec // G304: test temp paths
}

func regFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool-sources.yaml")
	writeFile(t, p, body)
	return p
}

const goodRegistry = `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:8001/mcp"}
  notes: {type: local-notes, root: /tmp/notes}
sets:
  jira:readonly: {source: jira, tools: [search_issues_jql, get_comments]}
  notes:readonly: {source: notes, tools: [wiki_search, wiki_read]}
`

func TestLoad_Good(t *testing.T) {
	r, err := Load(regFile(t, goodRegistry))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 1 || len(r.SourceErrors) != 0 || len(r.SetErrors) != 0 {
		t.Fatalf("registry = %+v", r)
	}
	want := map[string]Source{
		"jira":  {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
		"notes": {Type: TypeLocalNotes, Root: "/tmp/notes"},
	}
	if !reflect.DeepEqual(r.Sources, want) {
		t.Errorf("sources = %+v", r.Sources)
	}
	if got := r.Sets["jira:readonly"]; got.Source != "jira" || !reflect.DeepEqual(got.Tools, []string{"search_issues_jql", "get_comments"}) {
		t.Errorf("jira:readonly = %+v", got)
	}
}

// A typo anywhere is an error naming the file and the key, never ignored.
func TestLoad_Strict(t *testing.T) {
	for name, body := range map[string]string{
		"top-level": strings.Replace(goodRegistry, "sources:", "sorces:", 1),
		"in source": strings.Replace(goodRegistry, "url:", "ulr:", 1),
		"in set":    strings.Replace(goodRegistry, "tools: [wiki_search", "tool: [wiki_search", 1),
	} {
		t.Run(name, func(t *testing.T) {
			p := regFile(t, body)
			_, err := Load(p)
			if err == nil {
				t.Fatal("Load accepted a typo")
			}
			key := map[string]string{"top-level": "sorces", "in source": "ulr", "in set": "tool"}[name]
			if !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), key) {
				t.Errorf("error %q should name %s and %q", err, p, key)
			}
		})
	}
}

func TestLoad_Version(t *testing.T) {
	for _, body := range []string{"", "sources: {}\n", "version: 2\n"} {
		p := regFile(t, body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "version") || !strings.Contains(err.Error(), p) {
			t.Errorf("body %q: err = %v, want a version error naming the file", body, err)
		}
	}
}

func TestLoad_Missing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

// A set naming an unknown source is dropped with an error naming it; the
// other sets keep working.
func TestLoad_SetUnknownSource(t *testing.T) {
	body := goodRegistry + "  slack:readonly: {source: slack, tools: [search_messages]}\n"
	r, err := Load(regFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	e := r.SetErrors["slack:readonly"]
	if e == nil || !strings.Contains(e.Error(), "slack:readonly") || !strings.Contains(e.Error(), `unknown source "slack"`) {
		t.Errorf("slack:readonly error = %v", e)
	}
	if _, ok := r.Sets["slack:readonly"]; ok {
		t.Error("the broken set is still in Sets")
	}
	if _, ok := r.Sets["jira:readonly"]; !ok {
		t.Error("jira:readonly was dropped with the broken set")
	}
	if _, ok := r.Sets["notes:readonly"]; !ok {
		t.Error("notes:readonly was dropped with the broken set")
	}
}

// Each malformed entry is a per-entry error; a set on a malformed source
// says so; the good entries survive.
func TestLoad_MalformedEntries(t *testing.T) {
	sources := map[string]string{
		"nourl":    `{type: mcp-http}`,
		"badurl":   `{type: mcp-http, url: "ftp://x/mcp"}`,
		"nohost":   `{type: mcp-http, url: "http:///mcp"}`,
		"httproot": `{type: mcp-http, url: "http://x/mcp", root: /tmp}`,
		"relroot":  `{type: local-notes, root: notes}`,
		"noroot":   `{type: local-notes}`,
		"notesurl": `{type: local-notes, root: /tmp, url: "http://x"}`,
		"what":     `{type: carrier-pigeon}`,
		"bad.name": `{type: mcp-http, url: "http://x/mcp"}`,
	}
	sets := map[string]string{
		"nocolon":           `{source: jira, tools: [a]}`,
		"jira:":             `{source: jira, tools: [a]}`,
		"jira:readonly:x":   `{source: jira, tools: [a]}`,
		"empty:readonly":    `{source: jira, tools: []}`,
		"glob:readonly":     `{source: jira, tools: ["search_*"]}`,
		"dup:readonly":      `{source: jira, tools: [a, a]}`,
		"nosource:readonly": `{tools: [a]}`,
		"onbad:readonly":    `{source: nourl, tools: [a]}`,
	}
	var b strings.Builder
	b.WriteString("version: 1\nsources:\n  jira: {type: mcp-http, url: \"http://127.0.0.1:8001/mcp\"}\n")
	for n, s := range sources {
		b.WriteString("  " + n + ": " + s + "\n")
	}
	b.WriteString("sets:\n  jira:readonly: {source: jira, tools: [search]}\n")
	for n, s := range sets {
		b.WriteString("  \"" + n + "\": " + s + "\n")
	}
	r, err := Load(regFile(t, b.String()))
	if err != nil {
		t.Fatal(err)
	}
	for n := range sources {
		if r.SourceErrors[n] == nil {
			t.Errorf("source %s: no error", n)
		}
		if _, ok := r.Sources[n]; ok {
			t.Errorf("source %s kept", n)
		}
	}
	for n := range sets {
		if r.SetErrors[n] == nil {
			t.Errorf("set %s: no error", n)
		}
		if _, ok := r.Sets[n]; ok {
			t.Errorf("set %s kept", n)
		}
	}
	if e := r.SetErrors["onbad:readonly"]; e == nil || !strings.Contains(e.Error(), `source "nourl" is invalid`) {
		t.Errorf("onbad:readonly error = %v", e)
	}
	if _, ok := r.Sets["jira:readonly"]; !ok || len(r.Sources) != 1 {
		t.Errorf("good entries lost: sets %v sources %v", r.Sets, r.Sources)
	}
}

// The default registry for a new install has today's MCP sources and
// allowlist and no notes source: a notes folder is added only when the
// user configures one.
func TestNotesSourceNotDefault(t *testing.T) {
	d := Default()
	for n, s := range d.Sources {
		if s.Type == TypeLocalNotes {
			t.Errorf("default has a notes source %s: %+v", n, s)
		}
	}
	wantURL := map[string]string{
		"slack":  "http://127.0.0.1:7381/mcp/slack",
		"jira":   "http://127.0.0.1:8001/mcp",
		"google": "http://127.0.0.1:8000/mcp",
	}
	if len(d.Sources) != len(wantURL) {
		t.Fatalf("sources = %v", d.Sources)
	}
	for n, u := range wantURL {
		if d.Sources[n] != (Source{Type: TypeMCPHTTP, URL: u}) {
			t.Errorf("%s = %+v", n, d.Sources[n])
		}
	}
	wantTools := map[string]int{"jira:readonly": 4, "slack:readonly": 5, "google:readonly": 11}
	if len(d.Sets) != len(wantTools) {
		t.Fatalf("sets = %v", d.Sets)
	}
	for n, c := range wantTools {
		if len(d.Sets[n].Tools) != c || d.Sets[n].Source != strings.TrimSuffix(n, ":readonly") {
			t.Errorf("%s = %+v, want %d tools", n, d.Sets[n], c)
		}
	}
	// What setup writes loads back unchanged and clean.
	data, err := Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Load(regFile(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Sources, d.Sources) || !reflect.DeepEqual(r.Sets, d.Sets) || len(r.SetErrors)+len(r.SourceErrors) != 0 {
		t.Errorf("round trip = %+v", r)
	}
}

func TestResolve(t *testing.T) {
	r, err := Load(regFile(t, goodRegistry+"  broken:readonly: {source: nope, tools: [a]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s, err := r.Resolve("jira:readonly"); err != nil || s.Source != "jira" {
		t.Errorf("jira:readonly = %+v, %v", s, err)
	}
	// Built-in profiles ask for wiki:readonly; with no such set it is the
	// notes set.
	if s, err := r.Resolve("wiki:readonly"); err != nil || s.Source != "notes" {
		t.Errorf("wiki:readonly = %+v, %v; want the notes set", s, err)
	}
	if _, err := r.Resolve("slack:readonly"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("slack:readonly err = %v, want ErrNotConfigured", err)
	}
	if _, err := r.Resolve("broken:readonly"); !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "unknown source") {
		t.Errorf("broken:readonly err = %v, want ErrNotConfigured with its reason", err)
	}
}

// A registry that defines wiki:readonly itself (a migrated owner) uses it;
// the alias is only a fallback, and a broken wiki set is reported, not
// silently replaced by the notes set.
func TestResolve_WikiSetWinsOverAlias(t *testing.T) {
	body := strings.Replace(goodRegistry, "sets:\n", "  wiki: {type: local-notes, root: /tmp/wiki}\nsets:\n  wiki:readonly: {source: wiki, tools: [wiki_read]}\n", 1)
	r, err := Load(regFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if s, err := r.Resolve("wiki:readonly"); err != nil || s.Source != "wiki" {
		t.Errorf("wiki:readonly = %+v, %v; want the wiki set", s, err)
	}
	broken := strings.Replace(goodRegistry, "sets:\n", "sets:\n  wiki:readonly: {source: gone, tools: [wiki_read]}\n", 1)
	r, err = Load(regFile(t, broken))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("wiki:readonly"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("broken wiki:readonly err = %v, want its own error, not the alias", err)
	}
}

func TestPaths(t *testing.T) {
	if got := DefaultPath("/h"); got != "/h/.config/aimux/tool-sources.yaml" {
		t.Errorf("DefaultPath = %s", got)
	}
	if got := LegacyPath("/h"); got != "/h/.aimux/wingman/readonly.json" {
		t.Errorf("LegacyPath = %s", got)
	}
}

const legacyJSON = `{
  "HTTP": {
    "google": {"URL": "http://127.0.0.1:8000/mcp", "Headers": null},
    "jira": {"URL": "http://127.0.0.1:8001/mcp", "Headers": {}}
  },
  "WikiRoot": "/Users/me/wiki",
  "Allow": {
    "google": ["get_events", "list_calendars"],
    "jira": ["search_issues_jql"],
    "wiki": ["wiki_search", "wiki_read"]
  }
}`

func TestMigrate_FromReadonlyJSON(t *testing.T) {
	home := t.TempDir()
	old, reg := LegacyPath(home), DefaultPath(home)
	writeFile(t, old, legacyJSON)
	migrated, err := Migrate(old, reg)
	if err != nil || !migrated {
		t.Fatalf("Migrate = %v, %v", migrated, err)
	}
	r, err := Load(reg)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := map[string]Source{
		"google": {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8000/mcp"},
		"jira":   {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
		// The server keeps its name: pins, tokens and tool names key on it.
		"wiki": {Type: TypeLocalNotes, Root: "/Users/me/wiki"},
	}
	if !reflect.DeepEqual(r.Sources, wantSources) {
		t.Errorf("sources = %+v", r.Sources)
	}
	wantSets := map[string]Set{
		"google:readonly": {Source: "google", Tools: []string{"get_events", "list_calendars"}},
		"jira:readonly":   {Source: "jira", Tools: []string{"search_issues_jql"}},
		"wiki:readonly":   {Source: "wiki", Tools: []string{"wiki_search", "wiki_read"}},
	}
	if !reflect.DeepEqual(r.Sets, wantSets) {
		t.Errorf("sets = %+v", r.Sets)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old file still in place: %v", err)
	}
	if b, err := readFile(old + ".migrated"); err != nil || string(b) != legacyJSON {
		t.Errorf("old file not kept as .migrated: %v", err)
	}
	fi, err := os.Stat(reg)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("registry mode = %v, %v; want 0600", fi.Mode(), err)
	}
	di, err := os.Stat(filepath.Dir(reg))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("registry dir mode = %v, %v; want 0700", di.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(reg), ".tool-sources-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
	// A second run is a no-op.
	if migrated, err := Migrate(old, reg); err != nil || migrated {
		t.Errorf("second Migrate = %v, %v", migrated, err)
	}
}

func TestMigrate_NeverOverwrites(t *testing.T) {
	home := t.TempDir()
	old, reg := LegacyPath(home), DefaultPath(home)
	writeFile(t, old, legacyJSON)
	writeFile(t, reg, goodRegistry)
	migrated, err := Migrate(old, reg)
	if err != nil || migrated {
		t.Fatalf("Migrate = %v, %v; want a no-op", migrated, err)
	}
	if b, _ := readFile(reg); string(b) != goodRegistry {
		t.Errorf("registry overwritten:\n%s", b)
	}
	if b, err := readFile(old); err != nil || string(b) != legacyJSON {
		t.Errorf("old file touched: %v", err)
	}
	// Even a registry that does not parse is the user's to fix.
	writeFile(t, reg, "sorces: {}\n")
	if migrated, err := Migrate(old, reg); err != nil || migrated {
		t.Errorf("Migrate over a broken registry = %v, %v; want a no-op", migrated, err)
	}
	if b, _ := readFile(reg); string(b) != "sorces: {}\n" {
		t.Errorf("broken registry overwritten:\n%s", b)
	}
}

// Once the registry exists the legacy file is not even read: a leftover
// that no longer parses is no error.
func TestMigrate_RegistryExistsIgnoresLegacy(t *testing.T) {
	home := t.TempDir()
	writeFile(t, LegacyPath(home), "{")
	writeFile(t, DefaultPath(home), goodRegistry)
	if migrated, err := Migrate(LegacyPath(home), DefaultPath(home)); err != nil || migrated {
		t.Fatalf("Migrate = %v, %v; want a no-op", migrated, err)
	}
}

func TestMigrate_NoLegacyFile(t *testing.T) {
	home := t.TempDir()
	migrated, err := Migrate(LegacyPath(home), DefaultPath(home))
	if err != nil || migrated {
		t.Fatalf("Migrate = %v, %v", migrated, err)
	}
	if _, err := os.Stat(DefaultPath(home)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("registry written without a legacy file: %v", err)
	}
}

// A legacy file that cannot be carried over cleanly writes nothing and
// leaves the old file where it is.
func TestMigrate_InvalidLegacyWritesNothing(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `{`,
		"unknown key":   `{"HTTP": {}, "WikiRoot": "", "Allow": {}, "Extra": 1}`,
		"headers":       `{"HTTP": {"jira": {"URL": "http://x/mcp", "Headers": {"Authorization": "x"}}}, "Allow": {"jira": ["a"]}}`,
		"bad url":       `{"HTTP": {"jira": {"URL": "ftp://x"}}, "Allow": {"jira": ["a"]}}`,
		"http wiki":     `{"HTTP": {"wiki": {"URL": "http://x/mcp"}}, "WikiRoot": "/w", "Allow": {}}`,
		"relative root": `{"HTTP": {}, "WikiRoot": "wiki", "Allow": {"wiki": ["wiki_read"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			old, reg := LegacyPath(home), DefaultPath(home)
			writeFile(t, old, body)
			migrated, err := Migrate(old, reg)
			if err == nil || migrated {
				t.Fatalf("Migrate = %v, %v; want an error", migrated, err)
			}
			if !strings.Contains(err.Error(), old) {
				t.Errorf("error %q should name %s", err, old)
			}
			if _, err := os.Stat(reg); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("registry written: %v", err)
			}
			if b, err := readFile(old); err != nil || string(b) != body {
				t.Errorf("old file touched: %v", err)
			}
		})
	}
}

func TestMigrate_HeadersErrorHidesValues(t *testing.T) {
	home := t.TempDir()
	writeFile(t, LegacyPath(home), `{"HTTP": {"jira": {"URL": "http://x/mcp", "Headers": {"Authorization": "Bearer s3cret"}}}, "Allow": {}}`)
	_, err := Migrate(LegacyPath(home), DefaultPath(home))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v; want an error without the header value", err)
	}
}

func TestWriteNew_RefusesExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", "tool-sources.yaml")
	if err := WriteNew(p, Default()); err != nil {
		t.Fatal(err)
	}
	before, _ := readFile(p)
	r := Default()
	delete(r.Sets, "jira:readonly")
	if err := WriteNew(p, r); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second WriteNew err = %v, want fs.ErrExist", err)
	}
	if after, _ := readFile(p); string(after) != string(before) {
		t.Error("existing registry overwritten")
	}
}

func TestWriteNew_RefusesInvalid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tool-sources.yaml")
	r := Default()
	r.Sets["x:readonly"] = Set{Source: "nope", Tools: []string{"a"}}
	if err := WriteNew(p, r); err == nil {
		t.Fatal("WriteNew wrote a registry with a broken set")
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file written: %v", err)
	}
}

// A dangling link (or anything but plain absence) at the registry path is
// an error: never the defaults, never a migration over it.
func TestLoad_DanglingSymlinkIsAnError(t *testing.T) {
	home := t.TempDir()
	reg := DefaultPath(home)
	if err := os.MkdirAll(filepath.Dir(reg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "gone.yaml"), reg); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(reg); err == nil || errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), reg) {
		t.Fatalf("Load = %v, want an error naming %s that is not ErrNotExist", err, reg)
	}
	writeFile(t, LegacyPath(home), legacyJSON)
	if migrated, err := Migrate(LegacyPath(home), reg); err != nil || migrated {
		t.Fatalf("Migrate over a dangling link = %v, %v; want no migration", migrated, err)
	}
	if r, err := ReadHome(home); err == nil || len(r.Sets) != 0 {
		t.Fatalf("ReadHome = %+v, %v; want an error, no defaults", r.Sets, err)
	}
	if _, err := os.Stat(LegacyPath(home)); err != nil {
		t.Errorf("legacy file moved: %v", err)
	}
}

func TestLoad_SecondDocumentIsAnError(t *testing.T) {
	p := regFile(t, goodRegistry+"---\nversion: 1\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("Load = %v, want an error naming the file", err)
	}
}

// An allowlist entry with no backend (the wiki with an empty root) was
// never served; migration drops it with a warning instead of failing every
// load.
func TestMigrate_DropsAllowlistWithoutBackend(t *testing.T) {
	home := t.TempDir()
	writeFile(t, LegacyPath(home), `{"HTTP": {"jira": {"URL": "http://127.0.0.1:8001/mcp"}}, "WikiRoot": "", "Allow": {"jira": ["search"], "wiki": ["wiki_read"], "gone": ["x"]}}`)
	m, err := MigrateReport(LegacyPath(home), DefaultPath(home))
	if err != nil || !m.Migrated {
		t.Fatalf("MigrateReport = %+v, %v", m, err)
	}
	if len(m.Dropped) != 2 || !strings.Contains(m.Dropped[0], `"gone:readonly"`) || !strings.Contains(m.Dropped[1], `"wiki:readonly"`) {
		t.Errorf("dropped = %q", m.Dropped)
	}
	r, err := Load(DefaultPath(home))
	if err != nil || len(r.SetErrors) != 0 {
		t.Fatal(r, err)
	}
	if _, ok := r.Sources["wiki"]; ok || len(r.Sets) != 1 || r.Sets["jira:readonly"].Source != "jira" {
		t.Errorf("registry = %+v", r)
	}
	b, _ := readFile(DefaultPath(home))
	if !strings.Contains(string(b), "wiki:readonly") {
		t.Errorf("the file does not note the dropped set:\n%s", b)
	}
}

func TestMigrate_HeadersMessage(t *testing.T) {
	home := t.TempDir()
	writeFile(t, LegacyPath(home), `{"HTTP": {"jira": {"URL": "http://x/mcp", "Headers": {"A": "b"}}}, "Allow": {}}`)
	_, err := Migrate(LegacyPath(home), DefaultPath(home))
	if err == nil || !strings.Contains(err.Error(), "header support is not carried over") || strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v", err)
	}
}

// ReadHome never writes: absent files give Default, a readonly.json not
// yet migrated is read in memory exactly as Migrate would write it (both
// files left as they were), an existing registry is Load's, and a legacy
// file that does not carry over is an error.
func TestReadHome_NoWrites(t *testing.T) {
	home := t.TempDir()
	r, err := ReadHome(home)
	if err != nil || !reflect.DeepEqual(r.Sets, Default().Sets) {
		t.Fatalf("empty home = %+v, %v; want the defaults", r.Sets, err)
	}
	writeFile(t, LegacyPath(home), legacyJSON)
	before, err := os.Stat(LegacyPath(home))
	if err != nil {
		t.Fatal(err)
	}
	r, err = ReadHome(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(DefaultPath(home)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadHome wrote the registry: %v", err)
	}
	if after, err := os.Stat(LegacyPath(home)); err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("ReadHome touched readonly.json: %v", err)
	}
	other := t.TempDir()
	writeFile(t, LegacyPath(other), legacyJSON)
	if did, err := Migrate(LegacyPath(other), DefaultPath(other)); err != nil || !did {
		t.Fatal(did, err)
	}
	migrated, err := Load(DefaultPath(other))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Sources, migrated.Sources) || !reflect.DeepEqual(r.Sets, migrated.Sets) {
		t.Fatalf("in memory %+v, migrated %+v: want the same registry", r, migrated)
	}
	writeFile(t, DefaultPath(home), goodRegistry)
	if r, err := ReadHome(home); err != nil || r.Sources["notes"].Root == "" {
		t.Fatalf("registry present = %+v, %v; want it loaded", r.Sources, err)
	}
	bad := t.TempDir()
	writeFile(t, LegacyPath(bad), `{"Nope": 1}`)
	if _, err := ReadHome(bad); err == nil || !strings.Contains(err.Error(), LegacyPath(bad)) {
		t.Fatalf("bad legacy err = %v, want one naming the file", err)
	}
}
