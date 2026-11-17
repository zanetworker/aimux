package mcpproxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/jsonedit"
)

// Important 1: a missing item has a way out.

func TestUninstall_MissingItemErrorNamesWithout(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	delete(fx.vault.items, HeaderService("pp"))
	err := fx.in.Uninstall()
	if err == nil || !strings.Contains(err.Error(), "aimux mcp proxy uninstall --without pp") {
		t.Fatalf("err = %v", err)
	}
}

func TestUninstall_WithoutGivesUpOneServer(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	delete(fx.vault.items, HeaderService("pp"))
	fx.in.Without = []string{"pp"}
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	_, members, err := readMCPServers(fx.claude)
	must(t, err)
	if _, ok := members["pp"]; ok {
		t.Fatalf("pp still in config: %s", members["pp"])
	}
	if !jsonEqual(members["deepwiki"], json.RawMessage(`{"type": "http", "url": "https://mcp.deepwiki.com/mcp"}`)) || members["slack"] == nil {
		t.Fatal("other servers not restored")
	}
	if !strings.Contains(fx.out.String(), "pp: its proxy entry was removed") || !strings.Contains(fx.out.String(), "claude mcp add") {
		t.Fatalf("out %s", fx.out.String())
	}
	if _, err := os.Stat(fx.manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manifest kept")
	}
}

func TestUninstall_WithoutUnknownName(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	fx.in.Without = []string{"nope"}
	if err := fx.in.Uninstall(); err == nil || !strings.Contains(err.Error(), "--without nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoveMember_KeepsOtherBytes(t *testing.T) {
	cases := map[string][2]string{
		"middle": {`{"a": 1, "b": {"x": 2}, "c": 3}`, `{"a": 1, "c": 3}`},
		"first":  {"{\n  \"a\": 1,\n  \"b\": 2\n}", "{\n  \"b\": 2\n}"},
		"last":   {`{"a": 1, "b": 2}`, `{"a": 1}`},
		"only":   {`{"a": 1}`, `{}`},
	}
	for name, c := range cases {
		key := "b"
		if name == "first" || name == "only" {
			key = "a"
		}
		got, err := removeMember(json.RawMessage(c[0]), key)
		if err != nil || string(got) != c[1] {
			t.Fatalf("%s: got %q err %v", name, got, err)
		}
	}
}

func TestDefinitionError_NamesWithout(t *testing.T) {
	_, err := httpHeaders(t.Context(), "pp", ServerConfig{Type: "http", URL: "https://x", HeaderNames: []string{"A"}}, secretsOf(nil))
	if err == nil || !strings.Contains(err.Error(), "uninstall --without pp") {
		t.Fatalf("err = %v", err)
	}
}

// Important 2: secrets in the URL.

func TestLoadClaudeHTTP_URLVariableInQueryKeptDirect(t *testing.T) {
	t.Setenv("AIMUX_URL_TOK", "s3cr3t")
	for _, u := range []string{"https://x.example/mcp?key=${AIMUX_URL_TOK}", "https://u:${AIMUX_URL_TOK}@x.example/mcp"} {
		p := writeClaude(t, `{"q":{"type":"http","url":"`+u+`"}}`)
		got, kept, err := LoadClaudeHTTPServers(p, 7381, true)
		if err != nil || len(got) != 0 || keptReason(kept, "q") != "uses ${AIMUX_URL_TOK} in its URL (query or userinfo); keep it direct until URL secrets are supported" {
			t.Fatalf("%s: got %v kept %v err %v", u, got, kept, err)
		}
	}
	p := writeClaude(t, `{"h":{"type":"http","url":"https://${AIMUX_URL_TOK}.example/mcp"}}`)
	if got, _, _ := LoadClaudeHTTPServers(p, 7381, true); len(got) != 1 {
		t.Fatal("a variable in the host should still move")
	}
}

func TestSecretScrubber_URLQueryAndPassword(t *testing.T) {
	sc := secretScrubber("https://user:pw-q7z9x@x.example/mcp?key=k-55aa&x=plain-value", nil)
	for _, v := range []string{"pw-q7z9x", "k-55aa", "plain-value"} {
		if strings.Contains(sc.scrub("error at "+v), v) {
			t.Fatalf("%s not scrubbed", v)
		}
	}
}

// Minor 3: encoded forms.

func TestSecretScrubber_EncodedForms(t *testing.T) {
	v := "a b/c+d\"e"
	sc := secretScrubber("", map[string]string{"X-Key": v})
	esc, _ := json.Marshal(v)
	for name, form := range map[string]string{
		"query": url.QueryEscape(v),
		"path":  url.PathEscape(v),
		"json":  strings.Trim(string(esc), `"`),
	} {
		if strings.Contains(sc.scrub("got "+form), form) {
			t.Fatalf("%s form not scrubbed", name)
		}
	}
}

func TestSecretScrubber_BasicDecoded(t *testing.T) {
	b := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:pw-7731"))
	sc := secretScrubber("", map[string]string{"Authorization": b})
	for _, form := range []string{"alice:pw-7731", "pw-7731"} {
		if strings.Contains(sc.scrub("echo "+form), form) {
			t.Fatalf("%q not scrubbed", form)
		}
	}
}

// Minor 6: delete hint and removed vs changed.

func TestUninstall_KeptItemNoteSaysRemoved(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return removeMember(old, "pp")
	}))
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	out := fx.out.String()
	if !strings.Contains(out, "(its entry was removed since install)") || !strings.Contains(out, "security delete-generic-password -a aimux -s aimux-mcp-proxy/pp") {
		t.Fatalf("out %s", out)
	}
}
