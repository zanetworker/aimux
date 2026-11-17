package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

func loadReg(t *testing.T, home string) toolreg.Registry {
	t.Helper()
	r, err := toolreg.Load(toolreg.DefaultPath(home))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func regBytes(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(toolreg.DefaultPath(home))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// add notes writes the source and its set, prints the approve command and
// approves nothing.
func TestToolsAddNotes_AddsSourceAndSetAndApprovesNothing(t *testing.T) {
	r := newToolsRig(t)
	folder := t.TempDir()
	out, errOut, err := runTools(t, r.env, "add", "notes", "--folder", folder)
	if err != nil {
		t.Fatalf("add: %v\n%s%s", err, out, errOut)
	}
	reg := loadReg(t, r.env.Home)
	if want := (toolreg.Source{Type: toolreg.TypeLocalNotes, Root: folder, Authorship: toolreg.AuthorshipSelf}); reg.Sources["notes"] != want {
		t.Fatalf("source %+v", reg.Sources["notes"])
	}
	if want := (toolreg.Set{Source: "notes", Tools: []string{"wiki_search", "wiki_read"}}); !reflect.DeepEqual(reg.Sets["notes:readonly"], want) {
		t.Fatalf("set %+v", reg.Sets["notes:readonly"])
	}
	if _, ok := reg.Sets["jira:readonly"]; !ok {
		t.Fatal("jira:readonly lost")
	}
	if !strings.Contains(out, "notes:readonly") || !strings.Contains(errOut, "aimux tools approve notes:readonly") {
		t.Errorf("output:\n%s\nstderr:\n%s", out, errOut)
	}
	if _, err := os.Stat(r.pinsPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("add wrote pins: %v", err)
	}
	if fi, _ := os.Stat(toolreg.DefaultPath(r.env.Home)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestToolsAddNotes_NameAndRelativeFolder(t *testing.T) {
	r := newToolsRig(t)
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	if _, _, err := runTools(t, r.env, "add", "notes", "--folder", "docs", "--name", "docs"); err != nil {
		t.Fatal(err)
	}
	reg := loadReg(t, r.env.Home)
	want, _ := filepath.EvalSymlinks(filepath.Join(base, "docs"))
	if got, _ := filepath.EvalSymlinks(reg.Sources["docs"].Root); got != want || !filepath.IsAbs(reg.Sources["docs"].Root) {
		t.Fatalf("root %q, want %q", reg.Sources["docs"].Root, want)
	}
	if reg.Sets["docs:readonly"].Source != "docs" {
		t.Fatalf("sets %+v", reg.Sets)
	}
}

func TestToolsAdd_Refusals(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.md")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"existing name", []string{"add", "notes", "--folder", dir, "--name", "jira"}, "already"},
		{"missing folder", []string{"add", "notes", "--folder", filepath.Join(dir, "nope")}, "folder"},
		{"folder is a file", []string{"add", "notes", "--folder", file}, "not a directory"},
		{"no folder", []string{"add", "notes"}, "--folder"},
		{"bad name", []string{"add", "notes", "--folder", dir, "--name", "a b"}, "name"},
		{"mcp not loopback", []string{"add", "mcp", "--name", "gh", "--url", "https://api.example.com/mcp", "--tools", "a"}, "loopback"},
		{"mcp no tools", []string{"add", "mcp", "--name", "gh", "--url", "http://127.0.0.1:9000/mcp"}, "--tools"},
		{"mcp no name", []string{"add", "mcp", "--url", "http://127.0.0.1:9000/mcp", "--tools", "a"}, "--name"},
		{"mcp userinfo", []string{"add", "mcp", "--name", "gh", "--url", "http://u:p@127.0.0.1:9000/mcp", "--tools", "a"}, "loopback"},
		{"mcp bad tool", []string{"add", "mcp", "--name", "gh", "--url", "http://127.0.0.1:9000/mcp", "--tools", "a,b*"}, "exact name"},
		{"unknown kind", []string{"add", "ftp", "--name", "gh"}, "notes, mcp, cli or http"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newToolsRig(t)
			before := regBytes(t, r.env.Home)
			_, _, err := runTools(t, r.env, c.args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want one containing %q", err, c.want)
			}
			if regBytes(t, r.env.Home) != before {
				t.Fatal("the registry changed")
			}
		})
	}
}

func TestToolsAddMCP_Loopback(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:9000/mcp", "http://localhost:9000/mcp", "http://[::1]:9000/mcp"} {
		r := newToolsRig(t)
		if _, _, err := runTools(t, r.env, "add", "mcp", "--name", "gh", "--url", u, "--tools", "get_issue, list_prs"); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		reg := loadReg(t, r.env.Home)
		if reg.Sources["gh"] != (toolreg.Source{Type: toolreg.TypeMCPHTTP, URL: u}) {
			t.Fatalf("source %+v", reg.Sources["gh"])
		}
		if !reflect.DeepEqual(reg.Sets["gh:readonly"].Tools, []string{"get_issue", "list_prs"}) {
			t.Fatalf("set %+v", reg.Sets["gh:readonly"])
		}
	}
}

// With no registry, add migrates readonly.json first (keeping its wiki),
// or else starts from the defaults.
func TestToolsAdd_MigratesOrStartsFromDefaults(t *testing.T) {
	r := newToolsRig(t)
	if err := os.Remove(toolreg.DefaultPath(r.env.Home)); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	if _, _, err := runTools(t, r.env, "add", "notes", "--folder", folder); err != nil {
		t.Fatal(err)
	}
	reg := loadReg(t, r.env.Home)
	if _, ok := reg.Sets["google:readonly"]; !ok || reg.Sources["notes"].Root != folder {
		t.Fatalf("no registry: %+v", reg.Sets)
	}

	r = newToolsRig(t)
	if err := os.Remove(toolreg.DefaultPath(r.env.Home)); err != nil {
		t.Fatal(err)
	}
	legacy := toolreg.LegacyPath(r.env.Home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"HTTP":{"jira":{"URL":"http://127.0.0.1:8001/mcp"}},"WikiRoot":"/w","Allow":{"jira":["search"],"wiki":["wiki_read"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runTools(t, r.env, "add", "notes", "--folder", folder)
	if err != nil {
		t.Fatal(err)
	}
	reg = loadReg(t, r.env.Home)
	if reg.Sources["wiki"].Root != "/w" || reg.Sets["wiki:readonly"].Source != "wiki" || reg.Sources["notes"].Root != folder {
		t.Fatalf("migrated registry %+v %+v", reg.Sources, reg.Sets)
	}
	if !strings.Contains(errOut, "Migrated") {
		t.Errorf("stderr should say it migrated:\n%s", errOut)
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Errorf("readonly.json not renamed: %v", err)
	}
}

func TestToolsAdd_JSON(t *testing.T) {
	r := newToolsRig(t)
	out, _, err := runTools(t, r.env, "--json", "add", "notes", "--folder", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema string `json:"schema_version"`
		Source string `json:"source"`
		Set    string `json:"set"`
		Path   string `json:"path"`
		Next   string `json:"next"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.Schema != "1" || doc.Source != "notes" || doc.Set != "notes:readonly" || doc.Path != toolreg.DefaultPath(r.env.Home) || doc.Next != "aimux tools approve notes:readonly" {
		t.Fatalf("doc %+v", doc)
	}
}

// remove drops the source and its sets, keeps its pins (status names them
// unused), and asks on a terminal unless --yes.
func TestToolsRemove_RoundTripKeepsPins(t *testing.T) {
	r := newToolsRig(t)
	if _, err := runToolsOK(t, r.env, "approve", "jira:readonly"); err != nil {
		t.Fatal(err)
	}
	pinsBefore, _ := os.ReadFile(r.pinsPath())
	out, _, err := runTools(t, r.env, "remove", "jira", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	reg := loadReg(t, r.env.Home)
	if _, ok := reg.Sources["jira"]; ok {
		t.Fatal("jira still a source")
	}
	if _, ok := reg.Sets["jira:readonly"]; ok {
		t.Fatal("jira:readonly still a set")
	}
	if _, ok := reg.Sets["slack:readonly"]; !ok {
		t.Fatal("slack:readonly lost")
	}
	if !strings.Contains(out, "jira:readonly") || !strings.Contains(out, "kept") {
		t.Errorf("output:\n%s", out)
	}
	if pinsAfter, _ := os.ReadFile(r.pinsPath()); string(pinsAfter) != string(pinsBefore) {
		t.Fatal("remove touched pins.json")
	}
	status, _, err := runTools(t, r.env, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, toolgateway.StatusNotInRegistry) || !strings.Contains(status, "kept but unused") {
		t.Errorf("status after remove:\n%s", status)
	}

	// Added back, the same pins apply again.
	folder := t.TempDir()
	if _, _, err := runTools(t, r.env, "add", "notes", "--folder", folder); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runTools(t, r.env, "remove", "notes", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadReg(t, r.env.Home).Sources["notes"]; ok {
		t.Fatal("notes still a source")
	}
}

func TestToolsRemove_AsksOnATerminal(t *testing.T) {
	r := newToolsRig(t)
	before := regBytes(t, r.env.Home)
	r.env.IsTTY = func() bool { return false }
	if _, _, err := runTools(t, r.env, "remove", "jira"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no terminal, no --yes: %v", err)
	}
	r.env.IsTTY = func() bool { return true }
	r.env.In = func() io.Reader { return strings.NewReader("n\n") }
	out, _, err := runTools(t, r.env, "remove", "jira")
	if err != nil {
		t.Fatal(err)
	}
	if regBytes(t, r.env.Home) != before || !strings.Contains(out, "jira:readonly") {
		t.Fatalf("answered no: registry changed or prompt did not name the sets:\n%s", out)
	}
	r.env.In = func() io.Reader { return strings.NewReader("y\n") }
	if _, _, err := runTools(t, r.env, "remove", "jira"); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadReg(t, r.env.Home).Sources["jira"]; ok {
		t.Fatal("answered yes: jira still a source")
	}
}

func TestToolsRemove_UnknownSource(t *testing.T) {
	r := newToolsRig(t)
	before := regBytes(t, r.env.Home)
	if _, _, err := runTools(t, r.env, "remove", "nope", "--yes"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err %v", err)
	}
	if regBytes(t, r.env.Home) != before {
		t.Fatal("the registry changed")
	}
}

func shortPin(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	return strings.TrimPrefix(pinOf(t, raw), "sha256:")[:12]
}

// approve names each definition it pinned by its short hash, and diff
// shows the same hash so the reviewer can pass it to --expect.
func TestToolsApprove_PrintsPinnedHashes(t *testing.T) {
	r := newToolsRig(t)
	diff, _, err := runTools(t, r.env, "diff", "jira:readonly/search")
	if err != nil {
		t.Fatal(err)
	}
	h := shortPin(t, rawTool("search", "string"))
	if !strings.Contains(diff, "live "+h) {
		t.Errorf("diff lacks the live hash %s:\n%s", h, diff)
	}
	out, _, err := runTools(t, r.env, "approve", "jira:readonly/search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "approved jira:readonly/search "+h) {
		t.Errorf("approve output lacks the pinned hash %s:\n%s", h, out)
	}
	js, _, err := runTools(t, r.env, "--json", "approve", "jira:readonly/get")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js, `"hash": "`+pinOf(t, rawTool("get", "string"))+`"`) {
		t.Errorf("approve --json lacks the hash:\n%s", js)
	}
}

// --expect refuses when the live definition moved since the diff the
// reviewer read, writing nothing; it accepts the short or full hash.
func TestToolsApprove_Expect(t *testing.T) {
	r := newToolsRig(t)
	seen := shortPin(t, rawTool("search", "string"))
	r.live["jira"] = []json.RawMessage{rawTool("search", "number"), rawTool("get", "string")}
	_, _, err := runTools(t, r.env, "approve", "jira:readonly/search", "--expect", seen)
	if err == nil || !strings.Contains(err.Error(), "aimux tools diff") {
		t.Fatalf("moved definition: err %v", err)
	}
	if _, err := os.Stat(r.pinsPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused approve wrote pins: %v", err)
	}
	now := shortPin(t, rawTool("search", "number"))
	out, _, err := runTools(t, r.env, "approve", "jira:readonly/search", "--expect", now)
	if err != nil || !strings.Contains(out, "approved jira:readonly/search "+now) {
		t.Fatalf("matching short hash: %v\n%s", err, out)
	}
	if _, _, err := runTools(t, r.env, "approve", "jira:readonly/get", "--expect", pinOf(t, rawTool("get", "string"))); err != nil {
		t.Fatalf("matching full hash: %v", err)
	}
	for _, args := range [][]string{
		{"approve", "jira:readonly", "--expect", now},
		{"approve", "--all", "--expect", now},
		{"approve", "jira:readonly/search", "jira:readonly/get", "--expect", now},
		{"approve", "jira:readonly/search", "--expect", "abc"},
	} {
		if _, _, err := runTools(t, r.env, args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

// remove --json reports how many pins it kept, like the text does.
func TestToolsRemove_JSONCountsKeptPins(t *testing.T) {
	r := newToolsRig(t)
	if _, err := runToolsOK(t, r.env, "approve", "jira:readonly"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runTools(t, r.env, "--json", "remove", "jira", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema   string   `json:"schema_version"`
		Sets     []string `json:"sets"`
		KeptPins *int     `json:"kept_pins"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.KeptPins == nil || *doc.KeptPins != 2 || !reflect.DeepEqual(doc.Sets, []string{"jira:readonly"}) {
		t.Fatalf("doc %s", out)
	}
	out, _, err = runTools(t, r.env, "--json", "remove", "slack", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"kept_pins": 0`) {
		t.Fatalf("no pins: %s", out)
	}
}

func TestToolsAdd_NotesDefaultsSelf(t *testing.T) {
	r := newToolsRig(t)
	if _, errOut, err := runTools(t, r.env, "add", "notes", "--folder", t.TempDir()); err != nil {
		t.Fatalf("add: %v %s", err, errOut)
	}
	if !strings.Contains(regBytes(t, r.env.Home), "authorship: self") {
		t.Fatalf("no authorship: self in\n%s", regBytes(t, r.env.Home))
	}
}

func TestToolsAdd_MCPDefaultsOutsideOmitted(t *testing.T) {
	r := newToolsRig(t)
	if _, errOut, err := runTools(t, r.env, "add", "mcp", "--name", "x", "--url", "http://127.0.0.1:9/mcp", "--tools", "a"); err != nil {
		t.Fatalf("add: %v %s", err, errOut)
	}
	if strings.Contains(regBytes(t, r.env.Home), "authorship") {
		t.Fatalf("authorship key written for the default:\n%s", regBytes(t, r.env.Home))
	}
}

func TestToolsAdd_AuthorshipFlag(t *testing.T) {
	r := newToolsRig(t)
	if _, errOut, err := runTools(t, r.env, "add", "mcp", "--name", "x", "--url", "http://127.0.0.1:9/mcp", "--tools", "a", "--authorship", "internal"); err != nil {
		t.Fatalf("add: %v %s", err, errOut)
	}
	if got := loadReg(t, r.env.Home).Sources["x"].Authorship; got != "internal" {
		t.Fatalf("authorship = %q", got)
	}
	before := regBytes(t, r.env.Home)
	_, _, err := runTools(t, r.env, "add", "mcp", "--name", "y", "--url", "http://127.0.0.1:9/mcp", "--tools", "a", "--authorship", "bogus")
	if err == nil {
		t.Fatal("bogus authorship accepted")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v, want it to name the bad value", err)
	}
	if regBytes(t, r.env.Home) != before {
		t.Fatal("file changed")
	}
}

func TestToolsAdd_CLI(t *testing.T) {
	r := newToolsRig(t)
	out, errOut, err := runTools(t, r.env, "add", "cli", "gh")
	if err != nil {
		t.Fatalf("add cli: %v\n%s%s", err, out, errOut)
	}
	reg := loadReg(t, r.env.Home)
	if want := (toolreg.Source{Type: toolreg.TypeCLI, Pack: "gh"}); reg.Sources["gh"] != want {
		t.Fatalf("source %+v", reg.Sources["gh"])
	}
	pack, _ := clipack.Builtin("gh")
	var want []string
	for _, c := range pack.Commands {
		want = append(want, "gh_"+c.Name)
	}
	if got := reg.Sets["gh:readonly"].Tools; !reflect.DeepEqual(got, want) || len(want) == 0 {
		t.Fatalf("tools %v, want %v", got, want)
	}
	if strings.Contains(regBytes(t, r.env.Home), "authorship") {
		t.Fatalf("authorship key written for the default:\n%s", regBytes(t, r.env.Home))
	}
	if !strings.Contains(errOut, "aimux tools approve gh:readonly") || !strings.Contains(errOut, "aimux sandbox setup") {
		t.Errorf("stderr:\n%s", errOut)
	}

	// --tools narrows the set, --name renames the source.
	r = newToolsRig(t)
	if _, _, err := runTools(t, r.env, "add", "cli", "gh", "--name", "ghub", "--tools", "gh_"+pack.Commands[0].Name); err != nil {
		t.Fatal(err)
	}
	reg = loadReg(t, r.env.Home)
	if reg.Sources["ghub"].Pack != "gh" || !reflect.DeepEqual(reg.Sets["ghub:readonly"].Tools, []string{"gh_" + pack.Commands[0].Name}) {
		t.Fatalf("registry %+v %+v", reg.Sources["ghub"], reg.Sets["ghub:readonly"])
	}
}

func TestToolsAdd_CLIRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown pack", []string{"add", "cli", "nope"}, "gh"},
		{"no pack", []string{"add", "cli"}, "pack"},
		{"unknown tool", []string{"add", "cli", "gh", "--tools", "gh_issue_create"}, "gh_issue_create"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newToolsRig(t)
			before := regBytes(t, r.env.Home)
			_, _, err := runTools(t, r.env, c.args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want one containing %q", err, c.want)
			}
			if regBytes(t, r.env.Home) != before {
				t.Fatal("the registry changed")
			}
		})
	}
}

const addSpec = `openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /a:
    get: {operationId: getA, responses: {"200": {description: ok}}}
    post: {operationId: postA, responses: {"200": {description: ok}}}
  /b:
    get: {operationId: getB, responses: {"200": {description: ok}}}
  /c:
    get: {operationId: getC, responses: {"200": {description: ok}}}
    delete: {operationId: delC, responses: {"200": {description: ok}}}
`

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestToolsAdd_HTTP(t *testing.T) {
	spec := writeSpec(t, addSpec)
	r := newToolsRig(t)
	out, errOut, err := runTools(t, r.env, "add", "http", "--name", "api", "--spec", spec, "--base-url", "https://api.example.com/v1")
	if err != nil {
		t.Fatalf("add http: %v\n%s%s", err, out, errOut)
	}
	reg := loadReg(t, r.env.Home)
	if want := (toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: spec, BaseURL: "https://api.example.com/v1"}); reg.Sources["api"] != want {
		t.Fatalf("source %+v", reg.Sources["api"])
	}
	if want := []string{"api_get_a", "api_get_b", "api_get_c"}; !reflect.DeepEqual(reg.Sets["api:readonly"].Tools, want) {
		t.Fatalf("tools %v, want %v", reg.Sets["api:readonly"].Tools, want)
	}
	if !strings.Contains(errOut, "aimux tools approve api:readonly") || !strings.Contains(errOut, "aimux sandbox setup") {
		t.Errorf("stderr:\n%s", errOut)
	}
}

func TestToolsAdd_HTTPListsOperations(t *testing.T) {
	spec := writeSpec(t, addSpec)
	r := newToolsRig(t)
	out, _, err := runTools(t, r.env, "add", "http", "--name", "api", "--spec", spec, "--base-url", "https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 GET operations, 2 others ignored") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestToolsAdd_HTTPRefusals(t *testing.T) {
	spec := writeSpec(t, addSpec)
	empty := writeSpec(t, "openapi: 3.0.0\ninfo: {title: t, version: \"1\"}\npaths:\n  /a:\n    post: {operationId: p, responses: {\"200\": {description: ok}}}\n")
	bad := writeSpec(t, "not: openapi")
	const base = "https://api.example.com"
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"relative spec", []string{"--name", "api", "--spec", "spec.yaml", "--base-url", base}, "absolute"},
		{"no spec", []string{"--name", "api", "--base-url", base}, "--spec"},
		{"missing spec", []string{"--name", "api", "--spec", filepath.Join(t.TempDir(), "x.yaml"), "--base-url", base}, "spec"},
		{"http base", []string{"--name", "api", "--spec", spec, "--base-url", "http://api.example.com"}, "https"},
		{"base with userinfo", []string{"--name", "api", "--spec", spec, "--base-url", "https://u:p@api.example.com"}, "userinfo"},
		{"no base", []string{"--name", "api", "--spec", spec}, "--base-url"},
		{"no name", []string{"--spec", spec, "--base-url", base}, "--name"},
		{"credential", []string{"--name", "api", "--spec", spec, "--base-url", base, "--credential", "tok"}, "not supported yet"},
		{"no get operations", []string{"--name", "api", "--spec", empty, "--base-url", base}, "GET"},
		{"not openapi", []string{"--name", "api", "--spec", bad, "--base-url", base}, "spec"},
		{"unknown tool", []string{"--name", "api", "--spec", spec, "--base-url", base, "--tools", "api_getZ"}, "api_getZ"},
		{"localhost base", []string{"--name", "api", "--spec", spec, "--base-url", "https://localhost:8443"}, "localhost"},
		{"ip base", []string{"--name", "api", "--spec", spec, "--base-url", "https://10.0.0.5/api"}, "--base-url"},
		{"internal base", []string{"--name", "api", "--spec", spec, "--base-url", "https://host.containers.internal:7390"}, ".internal"},
		{"model host base", []string{"--name", "api", "--spec", spec, "--base-url", "https://api.anthropic.com"}, "model host"},
		{"credentialed pack host", []string{"--name", "api", "--spec", spec, "--base-url", "https://API.github.com/v3"}, "aimux tools add cli gh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newToolsRig(t)
			before := regBytes(t, r.env.Home)
			_, _, err := runTools(t, r.env, append([]string{"add", "http"}, c.args...)...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want one containing %q", err, c.want)
			}
			if regBytes(t, r.env.Home) != before {
				t.Fatal("the registry changed")
			}
		})
	}
}

func TestToolsAdd_HTTPBaseURLErrorsNeverEchoSecrets(t *testing.T) {
	spec := writeSpec(t, addSpec)
	for _, base := range []string{
		"https://user:secret@x.example.com",
		"https://x.example.com/?key=SECRET",
		"https://x.example.com/#SECRET",
		"https://x.example.com/%zzSECRET",
		"ht!tp://secret",
		"http://x.example.com/?key=SECRET",
	} {
		t.Run(base, func(t *testing.T) {
			r := newToolsRig(t)
			_, _, err := runTools(t, r.env, "add", "http", "--name", "api", "--spec", spec, "--base-url", base)
			if err == nil {
				t.Fatal("accepted")
			}
			if s := strings.ToLower(err.Error()); strings.Contains(s, "secret") {
				t.Fatalf("error echoes the URL: %v", err)
			}
		})
	}
}
