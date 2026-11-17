package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/keyring"
)

// secretsOf is a SecretReader over items (service -> value).
func secretsOf(items map[string]string) SecretReader {
	return func(_ context.Context, service string) (string, error) {
		v, ok := items[service]
		if !ok {
			return "", fmt.Errorf("keychain %s: %w", service, keyring.ErrNotFound)
		}
		return v, nil
	}
}

func headerItemFor(t *testing.T, headers map[string]string) string {
	t.Helper()
	s := headerSecret{Headers: map[string]headerItem{}}
	for k, v := range headers {
		s.Headers[k] = headerItem{Value: v, Literal: `"` + v + `"`}
	}
	enc, err := encodeHeaderSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func startDef(t *testing.T, st Starter, name string) (Backend, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := st(ctx, name)
	if err == nil {
		t.Cleanup(func() { _ = b.Close() })
	}
	return b, err
}

func TestDefinitionStarter_HTTPSendsKeychainHeaders(t *testing.T) {
	s := server.NewMCPServer("fixture", "1")
	s.AddTool(mcp.NewTool("echo"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("x"), nil
	})
	inner := server.NewStreamableHTTPServer(s)
	got := make(chan string, 32)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("Authorization"):
		default:
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	cfgs := map[string]ServerConfig{"pp": {Type: "http", URL: ts.URL, HeaderNames: []string{"Authorization"}}}
	st := DefinitionStarter(cfgs, nil, secretsOf(map[string]string{HeaderService("pp"): headerItemFor(t, map[string]string{"Authorization": "Bearer tok"})}))
	b, err := startDef(t, st, "pp")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := b.ListToolsRaw(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools %d err %v", len(tools), err)
	}
	if h := <-got; h != "Bearer tok" {
		t.Fatalf("Authorization = %q", h)
	}
}

func TestDefinitionStarter_HeaderlessHTTPNeedsNoItem(t *testing.T) {
	mcp := newHTTPMCPServer(t)
	st := DefinitionStarter(map[string]ServerConfig{"dw": {Type: "http", URL: mcp.URL}}, nil, secretsOf(nil))
	if _, err := startDef(t, st, "dw"); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionStarter_StdioUnchanged(t *testing.T) {
	st := DefinitionStarter(map[string]ServerConfig{}, nil, secretsOf(nil))
	if _, err := startDef(t, st, "nope"); err == nil || !strings.Contains(err.Error(), `no MCP server named "nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestDefinitionStarter_MissingItemErrorNamesItemOnly(t *testing.T) {
	mcp := newHTTPMCPServer(t)
	cfgs := map[string]ServerConfig{
		"pp": {Type: "http", URL: mcp.URL, HeaderNames: []string{"Authorization"}},
		"dw": {Type: "http", URL: mcp.URL},
	}
	st := DefinitionStarter(cfgs, nil, secretsOf(nil))
	_, err := startDef(t, st, "pp")
	if err == nil || !strings.Contains(err.Error(), "keychain item aimux-mcp-proxy/pp is missing or locked") || !strings.Contains(err.Error(), "uninstall and install again") {
		t.Fatalf("err = %v", err)
	}
	if _, err := startDef(t, st, "dw"); err != nil {
		t.Fatalf("other server: %v", err)
	}
}

func TestDefinitionStarter_ItemLacksHeaderIsError(t *testing.T) {
	cfgs := map[string]ServerConfig{"pp": {Type: "http", URL: "http://127.0.0.1:1/mcp", HeaderNames: []string{"Authorization", "X-Other"}}}
	st := DefinitionStarter(cfgs, nil, secretsOf(map[string]string{HeaderService("pp"): headerItemFor(t, map[string]string{"Authorization": "Bearer tok"})}))
	_, err := startDef(t, st, "pp")
	if err == nil || !strings.Contains(err.Error(), "X-Other") || strings.Contains(err.Error(), "tok") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPBackend_ErrorsScrubHeaderValues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied for "+r.Header.Get("Authorization"), http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	cfgs := map[string]ServerConfig{"pp": {Type: "http", URL: ts.URL, HeaderNames: []string{"Authorization"}}}
	st := DefinitionStarter(cfgs, nil, secretsOf(map[string]string{HeaderService("pp"): headerItemFor(t, map[string]string{"Authorization": "Bearer " + sentinelHeader})}))
	_, err := startDef(t, st, "pp")
	if err == nil || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerConfig_StdioJSONUnchanged(t *testing.T) {
	b, _ := json.Marshal(ServerConfig{Command: "x", Args: []string{"a"}, Env: map[string]string{"K": "v"}})
	if string(b) != `{"command":"x","args":["a"],"env":{"K":"v"}}` {
		t.Fatalf("got %s", b)
	}
}

func TestEditServer_RefusesHTTP(t *testing.T) {
	dir := t.TempDir()
	manifest, servers := dir+"/m.json", dir+"/s.json"
	m := &Manifest{Version: manifestVersion, Port: 7381, Servers: map[string]MovedEntry{"dw": {Original: `{}`, Written: `{}`}}}
	if err := saveManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	if err := SaveServerFile(servers, map[string]ServerConfig{"dw": {Type: "http", URL: "https://x"}}, []string{"dw"}); err != nil {
		t.Fatal(err)
	}
	err := EditServer(context.Background(), EditOptions{Server: "dw", ManifestPath: manifest, ServersPath: servers})
	if err == nil || err.Error() != "dw is an HTTP server; aimux mcp proxy edit changes stdio servers only" {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPBackend_ShortHeaderValueScrubbed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+r.Header.Get("X-Api-Key"), http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	cfgs := map[string]ServerConfig{"pp": {Type: "http", URL: ts.URL, HeaderNames: []string{"X-Api-Key"}}}
	st := DefinitionStarter(cfgs, nil, secretsOf(map[string]string{HeaderService("pp"): headerItemFor(t, map[string]string{"X-Api-Key": "q7z"})}))
	_, err := startDef(t, st, "pp")
	if err == nil || strings.Contains(err.Error(), "q7z") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefinitionStarter_HTTPBackendKeepsDefinition(t *testing.T) {
	srv := newHTTPMCPServer(t)
	def := ServerConfig{Type: "http", URL: srv.URL}
	b, err := startDef(t, DefinitionStarter(map[string]ServerConfig{"dw": def}, nil, secretsOf(nil)), "dw")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := b.(interface{ Definition() ServerConfig })
	if !ok || !sameDefinition(d.Definition(), def) {
		t.Fatalf("Definition missing or wrong: %v", ok)
	}
}

func TestReadTempDefinition_RejectsHTTPFields(t *testing.T) {
	p := t.TempDir() + "/def.json"
	if err := os.WriteFile(p, []byte(`{"command":"x","type":"http","url":"https://y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readTempDefinition(p); err == nil || !strings.Contains(err.Error(), "only command, args and env") {
		t.Fatalf("err = %v", err)
	}
}

func TestSystemVault_DarwinOnly(t *testing.T) {
	v := SystemVault()
	if (runtime.GOOS == "darwin") != (v != nil) {
		t.Fatalf("GOOS %s: vault %v", runtime.GOOS, v)
	}
}
