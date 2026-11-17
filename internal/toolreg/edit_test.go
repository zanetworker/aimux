package toolreg

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A registry with a comment, a flow-style set and an entry Load drops (a
// relative notes root): the edits must keep all three as they are.
const editBase = `# my own note about jira
version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:8001/mcp"}
  broken: {type: local-notes, root: relative/dir}
sets:
  jira:readonly: {source: jira, tools: [search_issues_jql, get_comments]}
  broken:readonly: {source: broken, tools: [wiki_read]}
`

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestAddSource_KeepsEveryOtherEntry(t *testing.T) {
	p := regFile(t, editBase)
	set, err := AddSource(p, "notes", Source{Type: TypeLocalNotes, Root: "/Users/me/notes"}, []string{"wiki_search", "wiki_read"})
	if err != nil {
		t.Fatal(err)
	}
	if set != "notes:readonly" {
		t.Fatalf("set %q, want notes:readonly", set)
	}
	data, _ := readFile(p)
	got := string(data)
	for _, keep := range []string{"# my own note about jira", "broken:", "relative/dir", "[search_issues_jql, get_comments]"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Source{Type: TypeLocalNotes, Root: "/Users/me/notes"}); r.Sources["notes"] != want {
		t.Errorf("notes source %+v", r.Sources["notes"])
	}
	if want := (Set{Source: "notes", Tools: []string{"wiki_search", "wiki_read"}}); !reflect.DeepEqual(r.Sets["notes:readonly"], want) {
		t.Errorf("notes set %+v", r.Sets["notes:readonly"])
	}
	if _, ok := r.Sets["jira:readonly"]; !ok {
		t.Error("jira:readonly lost")
	}
	if r.SourceErrors["broken"] == nil || r.SetErrors["broken:readonly"] == nil {
		t.Error("the broken entry should still be in the file, still dropped by Load")
	}
	if m := mode(t, p); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
}

func TestAddSource_MissingFileStartsFromDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg", "tool-sources.yaml")
	if _, err := AddSource(p, "notes", Source{Type: TypeLocalNotes, Root: "/n"}, []string{"wiki_search", "wiki_read"}); err != nil {
		t.Fatal(err)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for n := range Default().Sets {
		if _, ok := r.Sets[n]; !ok {
			t.Errorf("default set %s missing", n)
		}
	}
	if _, ok := r.Sets["notes:readonly"]; !ok {
		t.Error("notes:readonly missing")
	}
	if m := mode(t, p); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
}

func TestAddSource_Refusals(t *testing.T) {
	notes := Source{Type: TypeLocalNotes, Root: "/n"}
	tools := []string{"wiki_search", "wiki_read"}
	cases := []struct {
		name, body, source string
		src                Source
		tools              []string
		want               string
	}{
		{"existing source", editBase, "jira", notes, tools, "already"},
		{"existing broken source", editBase, "broken", notes, tools, "already"},
		{"existing set only", editBase + "  orphan:readonly: {source: jira, tools: [get_comments]}\n", "orphan", notes, tools, "already"},
		{"bad name", editBase, "no/slash", notes, tools, "name"},
		{"relative root", editBase, "notes", Source{Type: TypeLocalNotes, Root: "rel"}, tools, "absolute"},
		{"no tools", editBase, "notes", notes, nil, "no tools"},
		{"bad tool name", editBase, "notes", notes, []string{"a*"}, "exact name"},
		{"unknown key", "version: 1\nsourcez: {}\n", "notes", notes, tools, "sourcez"},
		{"second document", editBase + "---\nversion: 1\n", "notes", notes, tools, "more than one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := regFile(t, c.body)
			_, err := AddSource(p, c.source, c.src, c.tools)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want one containing %q", err, c.want)
			}
			if data, _ := readFile(p); string(data) != c.body {
				t.Fatalf("the file changed:\n%s", data)
			}
		})
	}
}

func TestAddSource_ExistingNameIsErrExists(t *testing.T) {
	// lonely is a source with no set: the source name alone is taken.
	p := regFile(t, strings.Replace(editBase, "sets:\n", "  lonely: {type: mcp-http, url: \"http://127.0.0.1:9/mcp\"}\nsets:\n", 1))
	for _, name := range []string{"jira", "lonely"} {
		_, err := AddSource(p, name, Source{Type: TypeLocalNotes, Root: "/n"}, []string{"wiki_read"})
		if !errors.Is(err, ErrExists) {
			t.Fatalf("%s: err %v, want ErrExists", name, err)
		}
	}
}

func TestAddSource_FollowsALinkToItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.yaml")
	writeFile(t, target, editBase)
	link := filepath.Join(dir, "tool-sources.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AddSource(link, "notes", Source{Type: TypeLocalNotes, Root: "/n"}, []string{"wiki_read"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	if data, _ := readFile(target); !strings.Contains(string(data), "notes:readonly") {
		t.Fatalf("target not edited:\n%s", data)
	}
}

func TestRemoveSource_RemovesItsSetsOnly(t *testing.T) {
	p := regFile(t, editBase+"  jira:extra: {source: jira, tools: [get_comments]}\n")
	if _, err := AddSource(p, "notes", Source{Type: TypeLocalNotes, Root: "/n"}, []string{"wiki_read"}); err != nil {
		t.Fatal(err)
	}
	sets, err := RemoveSource(p, "jira")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sets, []string{"jira:extra", "jira:readonly"}) {
		t.Fatalf("removed sets %v", sets)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Sources["jira"]; ok {
		t.Error("jira still a source")
	}
	if _, ok := r.Sets["notes:readonly"]; !ok {
		t.Error("notes:readonly lost")
	}
	data, _ := readFile(p)
	if !strings.Contains(string(data), "# my own note about jira") || !strings.Contains(string(data), "relative/dir") {
		t.Errorf("lost an unrelated line:\n%s", data)
	}
	if m := mode(t, p); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
}

func TestRemoveSource_BrokenSourceToo(t *testing.T) {
	p := regFile(t, editBase)
	sets, err := RemoveSource(p, "broken")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sets, []string{"broken:readonly"}) {
		t.Fatalf("removed sets %v", sets)
	}
	r, err := Load(p)
	if err != nil || len(r.SourceErrors) != 0 || len(r.SetErrors) != 0 {
		t.Fatalf("after removing the broken entry: %v %v %v", err, r.SourceErrors, r.SetErrors)
	}
}

func TestRemoveSource_Refusals(t *testing.T) {
	p := regFile(t, editBase)
	if _, err := RemoveSource(p, "nope"); !errors.Is(err, ErrNoSource) {
		t.Fatalf("unknown source: %v, want ErrNoSource", err)
	}
	if data, _ := readFile(p); string(data) != editBase {
		t.Fatal("the file changed")
	}
	missing := filepath.Join(t.TempDir(), "tool-sources.yaml")
	if _, err := RemoveSource(missing, "jira"); err == nil {
		t.Fatal("removing from a missing registry should fail")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("remove created the registry")
	}
	bad := regFile(t, "version: 2\n")
	if _, err := RemoveSource(bad, "jira"); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("bad file: %v", err)
	}
}

func TestAddToSet_AppendsKeepsComments(t *testing.T) {
	p := regFile(t, editBase)
	if _, err := AddToSet(p, "jira:readonly", "new_tool"); err != nil {
		t.Fatal(err)
	}
	data, _ := readFile(p)
	if !strings.Contains(string(data), "# my own note about jira") || !strings.Contains(string(data), "relative/dir") {
		t.Fatalf("lost other content:\n%s", data)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"search_issues_jql", "get_comments", "new_tool"}; !reflect.DeepEqual(r.Sets["jira:readonly"].Tools, want) {
		t.Fatalf("tools %v, want %v", r.Sets["jira:readonly"].Tools, want)
	}
}

func TestAddToSet_NoOpWhenPresent(t *testing.T) {
	p := regFile(t, editBase)
	before, _ := readFile(p)
	if _, err := AddToSet(p, "jira:readonly", "get_comments"); err != nil {
		t.Fatal(err)
	}
	after, _ := readFile(p)
	if string(before) != string(after) {
		t.Fatalf("file changed for a tool already in the set:\n%s", after)
	}
}

func TestAddToSet_UnknownSet(t *testing.T) {
	p := regFile(t, editBase)
	if _, err := AddToSet(p, "nope:readonly", "x"); err == nil || !errors.Is(err, ErrNoSet) {
		t.Fatalf("AddToSet on an unknown set = %v, want ErrNoSet", err)
	}
}

func TestAddToSet_RefusesSharedAnchor(t *testing.T) {
	p := regFile(t, `version: 1
sources:
  jira: {type: mcp-http, url: "http://127.0.0.1:8001/mcp"}
sets:
  jira:readonly: {source: jira, tools: &shared [search]}
  jira:triage: {source: jira, tools: *shared}
  base: &base {source: jira, tools: [get]}
  jira:merged: {<<: *base}
`)
	before, _ := readFile(p)
	for _, set := range []string{"jira:readonly", "jira:triage", "jira:merged"} {
		if _, err := AddToSet(p, set, "delete_issue"); err == nil || !strings.Contains(err.Error(), "YAML anchor") {
			t.Fatalf("%s: err %v, want the anchor refusal", set, err)
		}
	}
	after, _ := readFile(p)
	if string(before) != string(after) {
		t.Fatal("file changed")
	}
}

func TestAddToSet_UndoRestoresOnlyItsOwnEdit(t *testing.T) {
	p := regFile(t, editBase)
	orig, _ := readFile(p)
	undo, err := AddToSet(p, "jira:readonly", "new_tool")
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if now, _ := readFile(p); string(now) != string(orig) {
		t.Fatalf("undo did not restore the file:\n%s", now)
	}
	undo, err = AddToSet(p, "jira:readonly", "new_tool")
	if err != nil {
		t.Fatal(err)
	}
	other := []byte(string(orig) + "# someone else's edit\n")
	if err := os.WriteFile(p, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err == nil {
		t.Fatal("undo overwrote an edit made after it")
	}
	if now, _ := readFile(p); string(now) != string(other) {
		t.Fatal("the later edit was lost")
	}
}
