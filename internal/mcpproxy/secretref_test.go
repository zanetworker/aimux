package mcpproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveSecret_KeychainField(t *testing.T) {
	item := headerItemFor(t, map[string]string{"Authorization": "Bearer tok", "X.Dotted": "d-1"})
	read := secretsOf(map[string]string{"aimux-mcp-proxy/pp": item})
	for field, want := range map[string]string{"headers.Authorization.value": "Bearer tok", "headers.X.Dotted.value": "d-1"} {
		got, err := resolveSecret(t.Context(), SecretRef{Keychain: &KeychainRef{Item: "aimux-mcp-proxy/pp", Field: field}}, read)
		if err != nil || got != want {
			t.Fatalf("%s: got %q err %v", field, got, err)
		}
	}
}

func TestResolveSecret_EmptyOrUnknownRefIsError(t *testing.T) {
	var ref SecretRef
	must(t, json.Unmarshal([]byte(`{"vault":{"path":"x"}}`), &ref))
	for _, r := range []SecretRef{{}, ref, {Keychain: &KeychainRef{Item: "aimux-mcp-proxy/pp"}}} {
		if _, err := resolveSecret(t.Context(), r, secretsOf(nil)); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}

func TestResolveSecret_MissingFieldNamesFieldNotValue(t *testing.T) {
	item := headerItemFor(t, map[string]string{"Authorization": "Bearer " + sentinelValue})
	_, err := resolveSecret(t.Context(), SecretRef{Keychain: &KeychainRef{Item: "aimux-mcp-proxy/pp", Field: "headers.Other.value"}}, secretsOf(map[string]string{"aimux-mcp-proxy/pp": item}))
	if err == nil || !strings.Contains(err.Error(), "headers.Other.value") || strings.Contains(err.Error(), sentinelValue) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPHeaders_UnknownRefNamesServerAndHeader(t *testing.T) {
	cfg := ServerConfig{Type: "http", URL: "https://x", Secrets: &SecretRefs{Headers: map[string]SecretRef{"X-Key": {}}}}
	_, err := httpHeaders(t.Context(), "pp", cfg, secretsOf(nil))
	if err == nil || !strings.Contains(err.Error(), "pp") || !strings.Contains(err.Error(), "X-Key") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretRefs_EnvRoundTrip(t *testing.T) {
	cfg := ServerConfig{Command: "x", Secrets: &SecretRefs{Env: map[string]SecretRef{"TOKEN": {Keychain: &KeychainRef{Item: "i", Field: "f"}}}}}
	b, err := json.Marshal(cfg)
	must(t, err)
	var back ServerConfig
	must(t, json.Unmarshal(b, &back))
	if back.Secrets == nil || back.Secrets.Env["TOKEN"].Keychain.Item != "i" {
		t.Fatalf("round trip %s", b)
	}
}

func TestInstall_SavesKeychainRefs(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	servers, _, err := LoadServerFile(fx.servers)
	must(t, err)
	ref := servers["pp"].Secrets.Headers["Authorization"].Keychain
	if ref == nil || ref.Item != "aimux-mcp-proxy/pp" || ref.Field != "headers.Authorization.value" {
		t.Fatalf("ref %+v", ref)
	}
	if servers["deepwiki"].Secrets != nil {
		t.Fatal("headerless server got refs")
	}
}

func TestLoadClaudeHTTP_AnyURLVariableKeptDirect(t *testing.T) {
	t.Setenv("AIMUX_HOST", "x")
	p := writeClaude(t, `{"h":{"type":"http","url":"https://${AIMUX_HOST}.example/mcp"}}`)
	got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
	if err != nil || len(got) != 0 || keptReason(kept, "h") != "uses ${AIMUX_HOST} in its URL; keep it direct until Plan 4c" {
		t.Fatalf("got %v kept %v err %v", got, kept, err)
	}
}

func TestInstall_SavedURLIsAsWritten(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	b, err := os.ReadFile(fx.servers)
	must(t, err)
	if strings.Contains(string(b), "${") || !strings.Contains(string(b), "https://pp.example/mcp") {
		t.Fatalf("servers file %s", b)
	}
}

func TestServersFile_SchemaAndGeneratedBy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	must(t, SaveServerFile(p, map[string]ServerConfig{"a": {Command: "x"}}, nil))
	var raw map[string]json.RawMessage
	b, _ := os.ReadFile(p) // #nosec G304 -- the test's temp file
	must(t, json.Unmarshal(b, &raw))
	if string(raw["schema"]) != "2" || string(raw["generatedBy"]) != `"aimux mcp proxy install"` {
		t.Fatalf("file %s", b)
	}
}

func TestServersFile_NewerSchemaRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	must(t, os.WriteFile(p, []byte(`{"schema":3,"mcpServers":{}}`), 0o600))
	if _, _, err := LoadServerFile(p); err == nil || !strings.Contains(err.Error(), "written by a newer aimux") {
		t.Fatalf("err = %v", err)
	}
}

func TestServersFile_LegacyHeaderNamesBecomeRefs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	must(t, os.WriteFile(p, []byte(`{"mcpServers":{"pp":{"command":"","args":null,"env":null,"type":"http","url":"https://x","headerNames":["Authorization"]}}}`), 0o600))
	s, _, err := LoadServerFile(p)
	must(t, err)
	got := s["pp"]
	if !reflect.DeepEqual(got.Secrets, keychainHeaderRefs("pp", []string{"Authorization"})) || got.LegacyHeaderNames != nil {
		t.Fatalf("pp %+v", got)
	}
}

func TestResolveSecret_HeaderAndDottedSibling(t *testing.T) {
	item := headerItemFor(t, map[string]string{"X": "plain-x", "X.value": "dotted"})
	read := secretsOf(map[string]string{"i": item})
	for field, want := range map[string]string{"headers.X.value": "plain-x", "headers.X.value.value": "dotted"} {
		got, err := resolveSecret(t.Context(), SecretRef{Keychain: &KeychainRef{Item: "i", Field: field}}, read)
		if err != nil || got != want {
			t.Fatalf("%s: got %q err %v", field, got, err)
		}
	}
}

func TestServersFile_NoSchemaAccepted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	must(t, os.WriteFile(p, []byte(`{"mcpServers":{"a":{"command":"x","args":null,"env":null}}}`), 0o600))
	s, _, err := LoadServerFile(p)
	if err != nil || s["a"].Command != "x" {
		t.Fatalf("s %v err %v", s, err)
	}
}
