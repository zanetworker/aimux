package cmd

import (
	"bytes"
	"encoding/json"
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
