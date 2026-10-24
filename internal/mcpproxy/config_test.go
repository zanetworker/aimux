package mcpproxy

import (
	"os"
	"path/filepath"
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
