package cmd

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// hookHome lays out an installed proxy with one stdio server still direct.
func hookHome(t *testing.T) mcpproxy.Installer {
	t.Helper()
	home := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(home, ".aimux"), 0o700))
	must(os.WriteFile(filepath.Join(home, ".aimux", "mcp-proxy-install.json"), []byte(`{"version":2,"port":7381,"servers":{}}`), 0o600))
	must(os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"pypi":{"command":"pypi-mcp"}}}`), 0o600))
	return proxyInstaller(home, 7381, &bytes.Buffer{})
}

func TestProxyHooks_CheckAnswersJSON(t *testing.T) {
	var out bytes.Buffer
	if err := answerProxyHook("check", strings.NewReader(`{"hook_event_name":"SessionStart","cwd":"/w"}`), &out, hookHome(t)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %q (%v)", out.String(), err)
	}
	want := mcpproxy.CheckMessage([]string{"pypi"})
	if got.SystemMessage != want || got.HookSpecificOutput.HookEventName != "SessionStart" || got.HookSpecificOutput.AdditionalContext != want {
		t.Fatalf("answer %+v", got)
	}
}

func TestProxyHooks_SilentCases(t *testing.T) {
	in := hookHome(t)
	for name, tc := range map[string]struct{ event, stdin string }{
		"not an add":      {"added", `{"hook_event_name":"PostToolUse","tool_input":{"command":"ls"},"cwd":"/w"}`},
		"malformed stdin": {"check", `{not json`},
		"empty stdin":     {"added", ``},
		"unknown event":   {"bogus", `{}`},
	} {
		var out bytes.Buffer
		if err := answerProxyHook(tc.event, strings.NewReader(tc.stdin), &out, in); err != nil || out.Len() != 0 {
			t.Errorf("%s: err %v, output %q; want silent success", name, err, out.String())
		}
	}
}

// With a moved server and nothing listening on the proxy's address, the
// SessionStart answer leads with the proxy line, to the owner and Claude.
func TestProxyHooks_CheckSaysProxyIsDown(t *testing.T) {
	home := t.TempDir()
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry := `{"type":"http","url":"` + mcpproxy.EntryURL(7381, "ctx") + `"}`
	write(filepath.Join(home, ".aimux", "mcp-proxy-install.json"), `{"version":2,"port":7381,"servers":{"ctx":{"original":"{\"command\":\"ctx-mcp\"}","written":`+strconvQuote(entry)+`}}}`)
	write(filepath.Join(home, ".claude.json"), `{"mcpServers":{"ctx":`+entry+`}}`)
	in := proxyInstaller(home, 7381, &bytes.Buffer{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	in.ProxyAddr = ln.Addr().String()
	_ = ln.Close()
	var out bytes.Buffer
	if err := answerProxyHook("check", strings.NewReader(`{"cwd":"/w"}`), &out, in); err != nil {
		t.Fatal(err)
	}
	var got hookOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %q (%v)", out.String(), err)
	}
	want := mcpproxy.ProxyDownMessage(in.ProxyAddr)
	if got.SystemMessage != want || got.HookSpecificOutput.AdditionalContext != want {
		t.Fatalf("answer %+v, want %q", got, want)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
