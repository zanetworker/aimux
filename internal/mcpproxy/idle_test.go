package mcpproxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAddIdleTimeout(t *testing.T) {
	cases := []struct {
		name      string
		env       string // "" = no env key
		want      string // "" = env unchanged
		added     string
		wantError bool
	}{
		{name: "no env object", env: "", want: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: idleAddedEnv},
		{name: "env without the key keeps its members", env: `{"ANTHROPIC_DEFAULT_OPUS_MODEL": "x"}`,
			want: `{"ANTHROPIC_DEFAULT_OPUS_MODEL": "x", "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: idleAdded},
		{name: "the owner's own value wins", env: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "600000"}`, added: ""},
		{name: "our value already there", env: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: ""},
		{name: "env not an object", env: `"oops"`, wantError: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var in json.RawMessage
			if c.env != "" {
				in = json.RawMessage(c.env)
			}
			got, added, err := addIdleTimeout(in)
			if c.wantError {
				if err == nil {
					t.Fatal("want an error for a non-object env")
				}
				return
			}
			must(t, err)
			if added != c.added {
				t.Fatalf("added = %q, want %q", added, c.added)
			}
			want := c.want
			if want == "" {
				want = c.env
			}
			if !jsonEqual(got, json.RawMessage(want)) {
				t.Fatalf("env = %s, want %s", got, want)
			}
		})
	}
}

func TestRemoveIdleTimeout(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		added string
		want  string // "<nil>" = remove the env key
	}{
		{name: "ours, others kept", env: `{"A": "1", "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: idleAdded, want: `{"A": "1"}`},
		{name: "env we created goes away", env: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: idleAddedEnv, want: "<nil>"},
		{name: "env we created but the owner added to stays", env: `{"B": "2", "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: idleAddedEnv, want: `{"B": "2"}`},
		{name: "owner changed the value: kept", env: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "900000"}`, added: idleAdded, want: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "900000"}`},
		{name: "not added by install: kept", env: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`, added: "", want: `{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "1800000"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := removeIdleTimeout(json.RawMessage(c.env), c.added)
			must(t, err)
			if c.want == "<nil>" {
				if got != nil {
					t.Fatalf("env = %s, want the key removed", got)
				}
				return
			}
			if !jsonEqual(got, json.RawMessage(c.want)) {
				t.Fatalf("env = %s, want %s", got, c.want)
			}
		})
	}
	if got, err := removeIdleTimeout(nil, idleAdded); err != nil || got != nil {
		t.Fatalf("absent env = %s, %v; want it left absent", got, err)
	}
}

const settingsWithEnv = `{
  "env": {
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5-5"
  },
  "permissions": {"allow": ["mcp__slack__post_message"]}
}
`

func idleValue(t *testing.T, path string) (string, bool) {
	t.Helper()
	var s struct {
		Env map[string]string `json:"env"`
	}
	b, err := os.ReadFile(path) // #nosec G304 -- test temp file
	must(t, err)
	must(t, json.Unmarshal(b, &s))
	v, ok := s.Env[idleTimeoutKey]
	return v, ok
}

// Through the proxy Claude Code treats each server as http and gives up on
// a silent call after 5 minutes instead of stdio's 30; install sets the
// http idle limit to stdio's, and uninstall puts settings.json back.
func TestInstall_SetsIdleTimeout_UninstallRestoresSettingsByteIdentical(t *testing.T) {
	for _, settings := range []string{settingsWithEnv, fixtureSettings} { // env present, env absent
		fx := newInstallFX(t, fixtureClaude, settings)
		fx.in.Settings = fx.settings
		must(t, fx.in.Install(nil))
		if v, ok := idleValue(t, fx.settings); !ok || v != "1800000" {
			t.Fatalf("idle timeout after install = %q (set %v), want 1800000", v, ok)
		}
		if strings.Contains(settings, "ANTHROPIC_DEFAULT_OPUS_MODEL") && !strings.Contains(fx.read(fx.settings), `"ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5-5"`) {
			t.Fatalf("the owner's env member changed:\n%s", fx.read(fx.settings))
		}
		if !strings.Contains(fx.out.String(), "idle") {
			t.Fatalf("install did not say it set the idle limit:\n%s", fx.out.String())
		}
		must(t, fx.in.Uninstall())
		if got := fx.read(fx.settings); got != settings {
			t.Fatalf("settings.json not byte-identical after uninstall:\n%s\nwant:\n%s", got, settings)
		}
	}
}

func TestInstall_IdleTimeout_OwnersValueKept(t *testing.T) {
	settings := `{"env": {"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "600000"}}` + "\n"
	fx := newInstallFX(t, fixtureClaude, settings)
	fx.in.Settings = fx.settings
	must(t, fx.in.Install(nil))
	must(t, fx.in.Uninstall())
	if got := fx.read(fx.settings); got != settings {
		t.Fatalf("the owner's idle timeout was changed:\n%s", got)
	}
}

// An install from before this setting existed gets it on a plain re-run.
func TestInstall_IdleTimeout_AddedByRerunWithNothingToMove(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, settingsWithEnv)
	must(t, fx.in.Install(nil)) // Settings unset: the old behaviour
	if _, ok := idleValue(t, fx.settings); ok {
		t.Fatal("set without a settings path")
	}
	fx.in.Settings = fx.settings
	must(t, fx.in.Install(nil))
	if v, ok := idleValue(t, fx.settings); !ok || v != "1800000" {
		t.Fatalf("re-run did not set the idle timeout (got %q)", v)
	}
	must(t, fx.in.Uninstall())
	if got := fx.read(fx.settings); got != settingsWithEnv {
		t.Fatalf("settings.json not restored:\n%s", got)
	}
}

func TestInstall_IdleTimeout_NoSettingsFileIsANoteNotAFailure(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, "")
	fx.in.Settings = fx.settings
	must(t, fx.in.Install(nil))
	if _, err := os.Stat(fx.settings); !os.IsNotExist(err) {
		t.Fatal("install created settings.json")
	}
	if !strings.Contains(fx.out.String(), "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT") {
		t.Fatalf("no note about the idle timeout:\n%s", fx.out.String())
	}
}
