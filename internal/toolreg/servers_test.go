package toolreg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTransport_FromFields(t *testing.T) {
	cases := map[string]Server{
		"stdio":        {Command: []string{"/bin/srv"}},
		"http":         {URL: "http://127.0.0.1:1/mcp"},
		"managed-http": {Command: []string{"/bin/srv"}, URL: "http://127.0.0.1:1/mcp"},
		"local-notes":  {Kind: TypeLocalNotes, Root: "/n"},
		"cli":          {Kind: TypeCLI, Pack: "gh"},
	}
	for want, s := range cases {
		if got := s.Transport(); got != want {
			t.Errorf("%+v: got %q, want %q", s, got, want)
		}
	}
	if (Server{Kind: TypeMCPHTTP, URL: "http://x/mcp"}).Transport() != "http" {
		t.Error("type mcp-http with a url is http")
	}
}

func TestLoadInventory_UnknownFieldIsServerError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "servers.yaml")
	writeYAML(t, p, "version: 1\nservers:\n  good:\n    url: http://127.0.0.1:1/mcp\n  bad:\n    url: http://127.0.0.1:2/mcp\n    colour: red\n  empty: {}\n")
	inv, err := LoadInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Servers["good"]; !ok {
		t.Fatal("good server dropped")
	}
	for _, n := range []string{"bad", "empty"} {
		if _, ok := inv.Servers[n]; ok || inv.Errors[n] == nil {
			t.Errorf("%s: want a server error, got servers=%v errors=%v", n, inv.Servers, inv.Errors)
		}
	}
}

func TestSets_UnknownServerIsSetError(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  jira:readonly:\n    server: jira\n    tools: [search]\n  ghost:readonly:\n    server: ghost\n    tools: [x]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sets["jira:readonly"]; !ok {
		t.Fatalf("jira set missing: %+v", reg.Sets)
	}
	if reg.SetErrors["ghost:readonly"] == nil {
		t.Fatalf("ghost set: want an error, got %v", reg.SetErrors)
	}
}

func TestLoad_LocalOnlyServersNeverReachSources(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n  slack-write:\n    command: [/bin/slack]\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  jira:readonly:\n    server: jira\n    tools: [search]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sources["slack-write"]; ok {
		t.Fatal("a server no set references must never be a gateway source")
	}
	if s := reg.Sources["jira"]; s.Type != TypeMCPHTTP || s.URL != "http://127.0.0.1:8001/mcp" {
		t.Fatalf("jira source: %+v", s)
	}
}

func TestLoad_StdioServerCannotBackASet(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  s:\n    command: [/bin/s]\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  s:readonly:\n    server: s\n    tools: [x]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if reg.SetErrors["s:readonly"] == nil || !strings.Contains(reg.SetErrors["s:readonly"].Error(), "stdio") {
		t.Fatalf("want a stdio set error, got %v", reg.SetErrors)
	}
}

const legacyFixture = `version: 1
# notes kept
sources:
  jira:
    type: mcp-http
    url: http://127.0.0.1:8001/mcp
    authorship: internal
  wiki:
    type: local-notes
    root: /tmp/wiki
sets:
  jira:readonly:
    source: jira
    tools: [search, get]
  wiki:readonly:
    source: wiki
    tools: [wiki_search]
`

func TestLoad_LegacyToolSources(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tool-sources.yaml")
	writeYAML(t, p, legacyFixture)
	reg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Sources["jira"].URL != "http://127.0.0.1:8001/mcp" || len(reg.Sets) != 2 {
		t.Fatalf("legacy load: %+v", reg)
	}
}

func TestLoad_NewFilesWinOverLegacy(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  other:\n    url: http://127.0.0.1:9/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  other:readonly:\n    server: other\n    tools: [x]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sources["jira"]; ok {
		t.Fatal("tool-sources.yaml must be ignored once the new files exist")
	}
	if _, ok := reg.Sets["other:readonly"]; !ok {
		t.Fatalf("new files not read: %+v", reg)
	}
}

func TestMigrateSplit_RoundTripAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tool-sources.yaml")
	writeYAML(t, p, legacyFixture)
	before, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSplit(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".migrated"); err != nil {
		t.Fatalf("legacy file not kept as .migrated: %v", err)
	}
	after, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Sources, after.Sources) || !reflect.DeepEqual(before.Sets, after.Sets) {
		t.Fatalf("registry changed:\nbefore %+v\nafter  %+v", before, after)
	}
	srv, _ := os.ReadFile(filepath.Join(dir, "servers.yaml"))    //nolint:gosec // test temp file
	sets, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml")) //nolint:gosec // test temp file
	if !strings.Contains(string(srv), "authorship: internal") || !strings.Contains(string(sets), "source: jira") && !strings.Contains(string(sets), "server: jira") {
		t.Fatalf("split files:\n%s\n%s", srv, sets)
	}
	if err := MigrateSplit(dir); err == nil {
		t.Fatal("a second migrate must refuse (files exist)")
	}
}

func TestSnapshotVersion_CoversBothFiles(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  jira:readonly:\n    server: jira\n    tools: [a]\n")
	r1, _ := Load(filepath.Join(dir, "tool-sources.yaml"))
	v1 := NewSnapshot(r1, nil, nil).Version()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8002/mcp\n")
	r2, _ := Load(filepath.Join(dir, "tool-sources.yaml"))
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  jira:readonly:\n    server: jira\n    tools: [a, b]\n")
	r3, _ := Load(filepath.Join(dir, "tool-sources.yaml"))
	v2, v3 := NewSnapshot(r2, nil, nil).Version(), NewSnapshot(r3, nil, nil).Version()
	if v1 == v2 || v2 == v3 {
		t.Fatalf("versions must change with either file: %s %s %s", v1, v2, v3)
	}
}

func TestAddToSet_SplitModeEditsToolSets(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# keep me\nsets:\n  jira:readonly:\n    server: jira\n    tools: [search]\n")
	if _, err := AddToSet(filepath.Join(dir, "tool-sources.yaml"), "jira:readonly", "get"); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Sets["jira:readonly"].Tools; !reflect.DeepEqual(got, []string{"search", "get"}) {
		t.Fatalf("tools: %v", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml")) //nolint:gosec // test temp file
	if !strings.Contains(string(b), "# keep me") {
		t.Fatal("comment lost")
	}
	if _, err := os.Stat(filepath.Join(dir, "tool-sources.yaml")); err == nil {
		t.Fatal("the legacy file must not be created")
	}
}

func TestAddSource_SplitModeRefuses(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers: {}\n")
	if _, err := AddSource(filepath.Join(dir, "tool-sources.yaml"), "x", Source{Type: TypeMCPHTTP, URL: "http://127.0.0.1:1/mcp"}, []string{"a"}); err == nil {
		t.Fatal("want a refusal in split mode")
	}
}

func TestReadHome_SplitAfterMigrate(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "aimux")
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	if err := MigrateSplit(dir); err != nil {
		t.Fatal(err)
	}
	reg, err := ReadHome(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sets["jira:readonly"]; !ok {
		t.Fatalf("ReadHome ignored the split files: %+v", reg.Sets)
	}
	if _, err := MigrateReport(LegacyPath(home), DefaultPath(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DefaultPath(home)); err == nil {
		t.Fatal("setup must not recreate tool-sources.yaml after the split")
	}
}

func TestLoadInventory_ErrorHasNoValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "servers.yaml")
	writeYAML(t, p, "version: 1\nservers:\n  s:\n    url: http://127.0.0.1:1/mcp\n    env: literal-sentinel-xyz\n")
	inv, err := LoadInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if e := inv.Errors["s"]; e == nil || strings.Contains(e.Error(), "literal-sentinel-xyz") {
		t.Fatalf("error must exist and hold no value: %v", e)
	}
}

func TestSplit_UnknownTopKeyRefused(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservrs: {}\n")
	if _, err := Load(filepath.Join(dir, "tool-sources.yaml")); err == nil {
		t.Fatal("a misspelled top-level key must be an error")
	}
}

func TestMigrateSplit_CrashBeforeServersKeepsLegacy(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	// A crash after tool-sets.yaml was written and before servers.yaml.
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets: {}\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Sets) != 2 {
		t.Fatalf("without servers.yaml the legacy file must still rule: %+v", reg.Sets)
	}
	if err := MigrateSplit(dir); err != nil {
		t.Fatalf("migrate must finish over a leftover tool-sets.yaml: %v", err)
	}
	after, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil || len(after.Sets) != 2 {
		t.Fatalf("after: %+v, %v", after.Sets, err)
	}
}

func TestLoadInventory_AliasesResolve(t *testing.T) {
	p := filepath.Join(t.TempDir(), "servers.yaml")
	writeYAML(t, p, "version: 1\nservers:\n  a: &base\n    url: http://127.0.0.1:1/mcp\n  b: *base\n")
	inv, err := LoadInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Servers["b"].URL != "http://127.0.0.1:1/mcp" {
		t.Fatalf("alias not resolved: %+v errors=%v", inv.Servers, inv.Errors)
	}
}

func TestSplit_DanglingSetsLinkIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers: {}\n")
	if err := os.Symlink(filepath.Join(dir, "missing.yaml"), filepath.Join(dir, "tool-sets.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "tool-sources.yaml")); err == nil {
		t.Fatal("a dangling tool-sets.yaml link must be an error")
	}
}

func TestSplit_RejectedSetKeepsItsServerOut(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  only:\n    url: http://127.0.0.1:1/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  only:readonly:\n    server: only\n    tools: []\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if reg.SetErrors["only:readonly"] == nil {
		t.Skip("an empty tool list is accepted; nothing to check")
	}
	if _, ok := reg.Sources["only"]; ok {
		t.Fatal("a server named only by a rejected set must not reach the gateway")
	}
}

func TestLoadInventory_BadURLNotInError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "servers.yaml")
	writeYAML(t, p, "version: 1\nservers:\n  s:\n    url: ftp://user:pw-sentinel@host/x\n")
	inv, _ := LoadInventory(p)
	if e := inv.Errors["s"]; e == nil || strings.Contains(e.Error(), "pw-sentinel") {
		t.Fatalf("want an error without the url: %v", e)
	}
}

func TestReadHome_LeftoverReadonlyJSONDoesNotBlockSplit(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "aimux")
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, LegacyPath(home), "{not json")
	if err := MigrateSplit(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHome(home); err != nil {
		t.Fatalf("a leftover readonly.json must not matter in split mode: %v", err)
	}
	if _, err := AddToSet(DefaultPath(home), "jira:readonly", "new"); err != nil {
		t.Fatalf("approve into a set must work in split mode: %v", err)
	}
}

func TestMigrateSplit_KeepsAnExistingToolSets(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# hand-written\nsets: {}\n")
	if err := MigrateSplit(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "tool-sets.yaml.before-migrate")) //nolint:gosec // test temp file
	if err != nil || !strings.Contains(string(b), "hand-written") {
		t.Fatalf("the existing tool-sets.yaml must be kept: %v %s", err, b)
	}
}

func TestMigrateSplit_SecondRunWhileLockedRefuses(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	unlock, err := lockMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := MigrateSplit(dir); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("want a refusal while another migrate runs, got %v", err)
	}
}
