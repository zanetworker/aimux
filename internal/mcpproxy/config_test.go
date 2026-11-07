package mcpproxy

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadClaudeServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	body := `{"mcpServers":{
		"slack":{"command":"npx","args":["-y","slack-mcp"],"env":{"TOKEN":"x"}},
		"pypi":{"type":"stdio","command":"uvx","args":["pypi-mcp"]},
		"jira":{"type":"http","url":"https://example.invalid/mcp"},
		"events":{"type":"sse","url":"https://example.invalid/sse"},
		"mux":{"type":"http","url":"http://127.0.0.1:7381/mcp"},
		"self":{"command":"/usr/local/bin/aimux","args":["mcp","proxy","serve"]},
		"skipme":{"command":"foo"},
		"nocmd":{"type":"stdio"}
	}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClaudeServers(path, []string{"skipme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got servers %v, want only slack and pypi (stdio, not excluded, not the proxy itself)", got)
	}
	if s := got["slack"]; s.Command != "npx" || len(s.Args) != 2 || s.Env["TOKEN"] != "x" {
		t.Fatalf("slack = %+v", s)
	}
	if got["pypi"].Command != "uvx" {
		t.Fatalf("pypi = %+v", got["pypi"])
	}
}

func TestLoadClaudeServers_Errors(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadClaudeServers(filepath.Join(dir, "absent.json"), nil); err == nil {
		t.Fatal("missing config file returned no error")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClaudeServers(bad, nil); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("got %v, want a parse error naming the file", err)
	}
}

func TestServersFile_SaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-servers.json")
	in := map[string]ServerConfig{"slack": {Command: "npx", Args: []string{"-y", "slack-mcp"}, Env: map[string]string{"TOKEN": "x"}}}
	if err := SaveServers(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("servers file mode %v (%v), want 0600: it holds env values such as tokens", info.Mode().Perm(), err)
	}
	out, err := LoadServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := out["slack"]; s.Command != "npx" || len(s.Args) != 2 || s.Env["TOKEN"] != "x" {
		t.Fatalf("round trip: %+v", out)
	}
	if missing, err := LoadServers(filepath.Join(t.TempDir(), "absent.json")); err != nil || len(missing) != 0 {
		t.Fatalf("missing file: %v %v, want empty and no error", missing, err)
	}
}

func TestMergeServers(t *testing.T) {
	saved := map[string]ServerConfig{
		"slack":  {Command: "old-slack"},
		"github": {Command: "gh-mcp"}, // already removed from the Claude config
	}
	claude := map[string]ServerConfig{
		"slack": {Command: "new-slack"},
		"pypi":  {Command: "pypi-mcp"},
	}
	got := MergeServers(saved, claude)
	if len(got) != 3 || got["slack"].Command != "new-slack" || got["github"].Command != "gh-mcp" || got["pypi"].Command != "pypi-mcp" {
		t.Fatalf("merged %+v: want the Claude entry to win, and saved-only entries kept", got)
	}
}

func TestExpandConfig(t *testing.T) {
	t.Setenv("AIMUX_TEST_CRED", "secret123")
	t.Setenv("AIMUX_TEST_DIR", "/data")
	in := ServerConfig{
		Command: "${AIMUX_TEST_BIN:-/usr/local/bin/tool}",
		Args:    []string{"--cred", "${AIMUX_TEST_CRED}", "--dir=${AIMUX_TEST_DIR}/x", "$NOT_EXPANDED", "${AIMUX_TEST_UNSET}"},
		Env:     map[string]string{"TOKEN": "${AIMUX_TEST_CRED}", "PLAIN": "x", "WITH_DEFAULT": "${AIMUX_TEST_UNSET:-fallback}"}, // #nosec G101 -- variable references in a test fixture, not credentials
	}
	got := expandConfig(in)
	if got.Command != "/usr/local/bin/tool" {
		t.Fatalf("command %q, want the default", got.Command)
	}
	wantArgs := []string{"--cred", "secret123", "--dir=/data/x", "$NOT_EXPANDED", ""}
	for i, a := range wantArgs {
		if got.Args[i] != a {
			t.Fatalf("args %q, want %q", got.Args, wantArgs)
		}
	}
	if got.Env["TOKEN"] != "secret123" || got.Env["PLAIN"] != "x" || got.Env["WITH_DEFAULT"] != "fallback" {
		t.Fatalf("env %v", got.Env)
	}
	if in.Env["TOKEN"] != "${AIMUX_TEST_CRED}" {
		t.Fatal("expandConfig modified its input (the saved definitions must keep ${VAR})")
	}
}

func TestServerFile_ResolvedSetSurvivesSaveServers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "servers.json")
	a := ServerConfig{Command: "a"}
	if err := SaveServerFile(p, map[string]ServerConfig{"a": a, "b": {Command: "b"}}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveServers(p, map[string]ServerConfig{"a": a, "b": {Command: "b"}, "c": {Command: "c"}}); err != nil {
		t.Fatal(err)
	}
	servers, resolved, err := LoadServerFile(p)
	if err != nil || len(servers) != 3 || !reflect.DeepEqual(resolved, []string{"a"}) {
		t.Fatalf("servers %v resolved %v err %v", servers, resolved, err)
	}
	if _, resolved, err := LoadServerFile(filepath.Join(t.TempDir(), "missing.json")); err != nil || len(resolved) != 0 {
		t.Fatalf("missing file: %v %v", resolved, err)
	}
}

func TestMergeServers_ResolvedByInstallWins(t *testing.T) {
	saved := map[string]ServerConfig{
		"slack": {Command: "npx", Env: map[string]string{"TOKEN": "resolved"}},
		"gh":    {Command: "old"},
	}
	fromClaude := map[string]ServerConfig{
		"slack": {Command: "npx", Env: map[string]string{"TOKEN": "${TOKEN}"}},
		"gh":    {Command: "new"},
	}
	got := MergeServers(saved, fromClaude, "slack")
	if got["slack"].Env["TOKEN"] != "resolved" || got["gh"].Command != "new" {
		t.Fatalf("got %+v", got)
	}
	if got := MergeServers(saved, fromClaude); got["slack"].Env["TOKEN"] != "${TOKEN}" {
		t.Fatalf("without the resolved set the Claude config wins: %+v", got)
	}
}

// An empty moved list (installed, nothing served now) is kept apart from
// no list (a manual setup): the daemon serves nothing for the first and
// every configured server for the second.
func TestServerFile_EmptyMovedListIsNotAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	defs := map[string]ServerConfig{"a": {Command: "a-mcp"}}
	if err := SaveServerFile(path, defs, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := LoadServerFile(path); err != nil || resolved == nil || len(resolved) != 0 {
		t.Fatalf("resolved %#v, err %v; want an empty, present list", resolved, err)
	}
	if err := SaveServers(path, defs); err != nil { // refresh keeps it
		t.Fatal(err)
	}
	if _, resolved, _ := LoadServerFile(path); resolved == nil {
		t.Fatal("SaveServers dropped the empty moved list")
	}
	if err := SaveServerFile(path, defs, nil); err != nil {
		t.Fatal(err)
	}
	if _, resolved, _ := LoadServerFile(path); resolved != nil {
		t.Fatalf("resolved %#v, want no list", resolved)
	}
}
