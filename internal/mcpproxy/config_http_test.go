package mcpproxy

import (
	"os"
	"path/filepath"
	"testing"
)

func writeClaude(t *testing.T, servers string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":`+servers+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func keptReason(kept []KeptDirect, name string) string {
	for _, k := range kept {
		if k.Server == name {
			return k.Reason
		}
	}
	return ""
}

func TestLoadClaudeHTTP_NoAuthAndStaticHeadersMove(t *testing.T) {
	p := writeClaude(t, `{"deepwiki":{"type":"http","url":"https://mcp.deepwiki.com/mcp"},
		"pp":{"type":"http","url":"https://pp.example/mcp","headers":{"Authorization":"Bearer abc"}},
		"local":{"command":"x"}}`)
	got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
	if err != nil || len(kept) != 0 || len(got) != 2 {
		t.Fatalf("got %v kept %v err %v", got, kept, err)
	}
	if got["pp"].Headers["Authorization"] != "Bearer abc" || got["pp"].Literals["Authorization"] != `"Bearer abc"` || got["pp"].URL != "https://pp.example/mcp" {
		t.Fatalf("pp = %+v", got["pp"])
	}
	if len(got["deepwiki"].Headers) != 0 || string(got["deepwiki"].Raw) != `{"type":"http","url":"https://mcp.deepwiki.com/mcp"}` {
		t.Fatalf("deepwiki = %+v", got["deepwiki"])
	}
}

func TestLoadClaudeHTTP_OAuthFieldKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"brand":{"type":"http","url":"https://b/mcp","oauth":{"clientId":"c"}}}`)
	got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
	if err != nil || len(got) != 0 || keptReason(kept, "brand") != "needs OAuth (later)" {
		t.Fatalf("got %v kept %v err %v", got, kept, err)
	}
}

func TestLoadClaudeHTTP_SSEKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"s":{"type":"sse","url":"https://s/sse"}}`)
	_, kept, _ := LoadClaudeHTTPServers(p, 7381, true)
	if keptReason(kept, "s") != "SSE transport (not supported behind the proxy)" {
		t.Fatalf("kept %v", kept)
	}
}

func TestLoadClaudeHTTP_HeadersHelperKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"h":{"type":"http","url":"https://h","headersHelper":"/bin/x"}}`)
	_, kept, _ := LoadClaudeHTTPServers(p, 7381, true)
	if keptReason(kept, "h") != "headersHelper (headers made by a script)" {
		t.Fatalf("kept %v", kept)
	}
}

func TestLoadClaudeHTTP_NonStringHeaderKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"n":{"type":"http","url":"https://n","headers":{"A":1}}}`)
	_, kept, _ := LoadClaudeHTTPServers(p, 7381, true)
	if keptReason(kept, "n") != "headers is not a map of strings" {
		t.Fatalf("kept %v", kept)
	}
}

func TestLoadClaudeHTTP_ProxyEntriesIgnored(t *testing.T) {
	p := writeClaude(t, `{"x":{"type":"http","url":"http://127.0.0.1:7381/mcp/other"}}`)
	got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
	if err != nil || len(got) != 0 || len(kept) != 0 {
		t.Fatalf("got %v kept %v err %v", got, kept, err)
	}
}

func TestLoadClaudeHTTP_ProxyURLThroughVariableIgnored(t *testing.T) {
	t.Setenv("AIMUX_TEST_MCP_URL", "http://127.0.0.1:7381/mcp/other")
	p := writeClaude(t, `{"x":{"type":"http","url":"${AIMUX_TEST_MCP_URL}"}}`)
	got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
	if err != nil || len(got) != 0 || len(kept) != 0 {
		t.Fatalf("got %v kept %v err %v", got, kept, err)
	}
}

func TestLoadClaudeHTTP_VariablesResolved(t *testing.T) {
	t.Setenv("TOK", "abc123")
	p := writeClaude(t, `{"v":{"type":"http","url":"https://v/${PATHPART:-mcp}","headers":{"Authorization":"Bearer ${TOK}"}}}`)
	got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
	if err != nil || len(kept) != 0 {
		t.Fatalf("kept %v err %v", kept, err)
	}
	v := got["v"]
	if v.Headers["Authorization"] != "Bearer abc123" || v.Literals["Authorization"] != `"Bearer ${TOK}"` || v.URL != "https://v/mcp" {
		t.Fatalf("v = %+v", v)
	}
}

func TestLoadClaudeHTTP_UnsetVariableKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"u":{"type":"http","url":"https://u","headers":{"A":"${MISSING_AIMUX_TEST_VAR}"}}}`)
	got, kept, _ := LoadClaudeHTTPServers(p, 7381, true)
	if len(got) != 0 || keptReason(kept, "u") != "uses ${MISSING_AIMUX_TEST_VAR}, which is unset in this shell" {
		t.Fatalf("got %v kept %v", got, kept)
	}
}

func TestLoadClaudeHTTP_HeadersWithoutSecretStoreKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"a":{"type":"http","url":"https://a"},"b":{"type":"http","url":"https://b","headers":{"K":"v"}}}`)
	got, kept, _ := LoadClaudeHTTPServers(p, 7381, false)
	if _, ok := got["a"]; !ok || keptReason(kept, "b") != "headers need the macOS Keychain" {
		t.Fatalf("got %v kept %v", got, kept)
	}
}

func TestLoadClaudeHTTP_BadNameWithHeadersKeptDirect(t *testing.T) {
	p := writeClaude(t, `{"a b":{"type":"http","url":"https://a","headers":{"K":"v"}},"c d":{"type":"http","url":"https://c"}}`)
	got, kept, _ := LoadClaudeHTTPServers(p, 7381, true)
	if _, ok := got["c d"]; !ok || keptReason(kept, "a b") != "name has characters a Keychain item name cannot carry" {
		t.Fatalf("got %v kept %v", got, kept)
	}
}

func TestLoadClaudeServers_StillStdioOnly(t *testing.T) {
	p := writeClaude(t, `{"a":{"type":"http","url":"https://a"},"s":{"command":"x","args":[]}}`)
	got, err := LoadClaudeServers(p, nil)
	if err != nil || len(got) != 1 || got["s"].Command != "x" {
		t.Fatalf("got %v err %v", got, err)
	}
}
