package mcpproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mainServerConfig and mainServersFile are the types of the main-branch
// aimux (before HTTP servers moved), copied here: the new file must decode
// with them to only stdio servers, each with a definition.
type mainServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type mainServersFile struct {
	MCPServers        map[string]mainServerConfig `json:"mcpServers"`
	ResolvedByInstall *[]string                   `json:"resolvedByInstall,omitempty"`
}

func newLayoutFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mcp-servers.json")
	defs := map[string]ServerConfig{
		"slack": {Command: "slack-mcp", Args: []string{"--x"}, Env: map[string]string{"K": "v"}},
		"dw":    {Type: "http", URL: "https://mcp.deepwiki.com/mcp"},
		"pp":    {Type: "http", URL: "https://pp.example/mcp", Secrets: keychainHeaderRefs("pp", []string{"Authorization"})},
	}
	must(t, SaveServerFile(p, defs, []string{"slack", "dw", "pp"}))
	return p
}

func TestServersFile_OldBinaryReadsOnlyStdio(t *testing.T) {
	p := newLayoutFile(t)
	b, err := os.ReadFile(p) // #nosec G304 -- the test's temp file
	must(t, err)
	var old mainServersFile
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatalf("main-branch types cannot decode the file: %v", err)
	}
	if len(old.MCPServers) != 1 || old.MCPServers["slack"].Command != "slack-mcp" {
		t.Fatalf("old view of mcpServers: %+v", old.MCPServers)
	}
	if old.ResolvedByInstall == nil || strings.Join(*old.ResolvedByInstall, ",") != "slack" {
		t.Fatalf("old view of resolvedByInstall: %v", old.ResolvedByInstall)
	}
	for _, n := range *old.ResolvedByInstall {
		if old.MCPServers[n].Command == "" {
			t.Fatalf("old view lists %s without a definition", n)
		}
	}
}

func TestServersFile_RoundTripKeepsHTTP(t *testing.T) {
	p := newLayoutFile(t)
	s, resolved, err := LoadServerFile(p)
	must(t, err)
	if s["pp"].Type != "http" || s["dw"].URL != "https://mcp.deepwiki.com/mcp" || s["slack"].Command != "slack-mcp" {
		t.Fatalf("loaded %+v", s)
	}
	if strings.Join(resolved, ",") != "dw,pp,slack" {
		t.Fatalf("resolved %v", resolved)
	}
}

func TestServersFile_ReadsC226LayoutAndRewritesIt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp-servers.json")
	c226 := `{"schema":2,"generatedBy":"aimux mcp proxy install","mcpServers":{"slack":{"command":"slack-mcp","args":null,"env":null},"dw":{"command":"","args":null,"env":null,"type":"http","url":"https://mcp.deepwiki.com/mcp"}},"resolvedByInstall":["dw","slack"]}`
	must(t, os.WriteFile(p, []byte(c226), 0o600))
	s, resolved, err := LoadServerFile(p)
	must(t, err)
	if s["dw"].Type != "http" || strings.Join(resolved, ",") != "dw,slack" {
		t.Fatalf("loaded %+v %v", s, resolved)
	}
	must(t, SaveServerFile(p, s, resolved))
	b, _ := os.ReadFile(p) // #nosec G304 -- the test's temp file
	var old mainServersFile
	must(t, json.Unmarshal(b, &old))
	if _, ok := old.MCPServers["dw"]; ok {
		t.Fatalf("rewrite left HTTP in mcpServers: %s", b)
	}
}

func TestDaemonRefusal_NamesTheLaunchdBinary(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	fx.in.Version, fx.daemonVersion = "1.2.0", "1.1.0"
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp", "args": []}`))
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "/usr/local/bin/aimux") || !strings.Contains(err.Error(), "aimux upgrade mcp-proxy") {
		t.Fatalf("err = %v", err)
	}
}
