package mcpproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsServerAdd(t *testing.T) {
	yes := []string{
		"claude mcp add foo -- npx foo",
		"claude mcp add -s user foo npx foo",
		"claude mcp add --scope=user -e K=v foo -- npx foo",
		`claude mcp add-json foo '{"command":"x"}'`,
		"claude mcp add-from-claude-desktop",
		"cd x && claude mcp add foo npx foo",
		"/opt/bin/claude mcp add foo x",
		"ls; claude mcp add foo x | tee log",
	}
	no := []string{
		"claude mcp list",
		"claude mcp remove foo",
		"claude mcp get foo",
		`echo "claude mcp add foo"`,
		"# claude mcp add foo",
		"git commit -m 'claude mcp add'",
		"claude mcp",
		"claudette mcp add foo x",
		"claude mcp add 'unclosed",
		"",
	}
	for _, c := range yes {
		if !IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = false, want true", c)
		}
	}
	for _, c := range no {
		if IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = true, want false", c)
		}
	}
}

func TestProjectServers(t *testing.T) {
	dir := t.TempDir()
	w := filepath.Join(dir, "w")
	if err := os.MkdirAll(w, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".claude.json")
	body := `{"mcpServers":{"top":{"command":"x"}},"projects":{"` + w + `":{"mcpServers":{
		"local-stdio":{"type":"stdio","command":"npx","args":["a"]},
		"local-http":{"type":"http","url":"http://x"}}}}}`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, ".mcp.json"), []byte(`{"mcpServers":{"repo-stdio":{"command":"uvx","args":["b"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ProjectServers(cfg, w)
	if err != nil || strings.Join(got, ",") != "local-stdio,repo-stdio" {
		t.Fatalf("ProjectServers = %v, %v", got, err)
	}
	if got, err := ProjectServers(cfg, filepath.Join(dir, "elsewhere")); err != nil || len(got) != 0 {
		t.Fatalf("no project servers: %v, %v", got, err)
	}
	if got, err := ProjectServers(filepath.Join(dir, "missing.json"), w); err != nil || strings.Join(got, ",") != "repo-stdio" {
		t.Fatalf("missing config: %v, %v", got, err)
	}
}

func TestMessages(t *testing.T) {
	cases := map[string]string{
		MovedMessage([]string{"foo"}):                 "foo moved behind the aimux proxy (tools unchanged; it starts on first use). Open sessions keep their direct copy until they restart.",
		MovedMessage([]string{"foo", "bar"}):          "foo, bar moved behind the aimux proxy (tools unchanged; they start on first use). Open sessions keep their direct copy until they restart.",
		FailedMessage([]string{"foo"}, "it broke"):    "foo stays a direct server: it broke. Run aimux mcp proxy install after fixing it.",
		FailedMessage([]string{"foo", "bar"}, "boom"): "foo, bar stay direct servers: boom. Run aimux mcp proxy install after fixing it.",
		ProjectMessage("foo"):                         "foo was added for this project only; the proxy shares user-scope servers. To share it: claude mcp remove foo, then claude mcp add --scope user …",
		CheckMessage([]string{"foo"}):                 "1 MCP server is not behind the aimux proxy: foo. Run aimux mcp proxy install.",
		CheckMessage([]string{"foo", "bar"}):          "2 MCP servers are not behind the aimux proxy: foo, bar. Run aimux mcp proxy install.",
		CheckMessage(nil):                             "",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	}
}

func BenchmarkIsServerAdd_NoMatch(b *testing.B) {
	cmd := "cd /Users/someone/project && go test ./internal/mcpproxy/ -run TestSomething -count=1 -timeout 120s 2>&1 | tail -20 # a typical Bash call that is not an MCP add"
	for i := 0; i < b.N; i++ {
		if IsServerAdd(cmd) {
			b.Fatal("matched")
		}
	}
}
