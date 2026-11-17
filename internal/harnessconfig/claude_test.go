package harnessconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func claude(t *testing.T) Adapter {
	t.Helper()
	a, err := For("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// mapGrant is a ToolGrantView over a plain map, for tests only (a real
// grant is profile.ToolGrant, which copies).
type mapGrant map[string][]string

func (g mapGrant) Servers() []string {
	var out []string
	for s := range g {
		out = append(out, s)
	}
	return out
}

func (g mapGrant) Tools(server string) []string { return g[server] }

func flagVal(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

func TestClaudePrint_ExactAllowedTools(t *testing.T) {
	s, err := claude(t).Config(Capabilities{Grant: mapGrant(map[string][]string{
		"wiki": {"wiki_search", "wiki_read"},
		"jira": {"get_issue_details"},
	})}, Launch{Mode: ModeHeadless})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--tools", "", "--strict-mcp-config", "--mcp-config", "wingman-mcp-config-file",
		"--allowedTools", "mcp__jira__get_issue_details,mcp__wiki__wiki_read,mcp__wiki__wiki_search",
		"--setting-sources", ""}
	if !reflect.DeepEqual(s.Argv, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", s.Argv, want)
	}
	for _, a := range s.Argv {
		if strings.Contains(a, "*") {
			t.Fatalf("wildcard in argv: %q", a)
		}
	}
}

func TestClaudePrint_NoWebUnlessGranted(t *testing.T) {
	off, _ := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	if v, _ := flagVal(off.Argv, "--tools"); v != "" {
		t.Fatalf("--tools = %q, want empty", v)
	}
	on, _ := claude(t).Config(Capabilities{Web: true}, Launch{Mode: ModeHeadless})
	if v, _ := flagVal(on.Argv, "--tools"); v != "WebSearch,WebFetch" {
		t.Fatalf("--tools = %q", v)
	}
}

func TestClaudePrint_NoUserSettings(t *testing.T) {
	s, _ := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	if v, ok := flagVal(s.Argv, "--setting-sources"); !ok || v != "" {
		t.Fatalf("--setting-sources = %q ok=%v", v, ok)
	}
	s, _ = claude(t).Config(Capabilities{InheritUser: true}, Launch{Mode: ModeHeadless})
	if _, ok := flagVal(s.Argv, "--setting-sources"); ok {
		t.Fatalf("InheritUser must drop --setting-sources: %q", s.Argv)
	}
}

func TestClaudePrint_ResumeAndModel(t *testing.T) {
	s, _ := claude(t).Config(Capabilities{Model: "sonnet"}, Launch{Mode: ModeHeadless, ResumeID: "sess-1"})
	if v, _ := flagVal(s.Argv, "--model"); v != "sonnet" {
		t.Fatalf("model = %q", v)
	}
	if v, _ := flagVal(s.Argv, "--resume"); v != "sess-1" {
		t.Fatalf("resume = %q", v)
	}
	s, _ = claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	for _, f := range []string{"--model", "--resume"} {
		if _, ok := flagVal(s.Argv, f); ok {
			t.Fatalf("%s set without being asked", f)
		}
	}
}

func TestClaudeACP_PageMetaLocksEverything(t *testing.T) {
	s, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"tools": []any{}, "settingSources": []any{}, "mcpServers": map[string]any{}, "strictMcpConfig": true,
	}}}
	if !reflect.DeepEqual(s.ACPMeta, want) {
		t.Fatalf("meta = %#v", s.ACPMeta)
	}
	s, _ = claude(t).Config(Capabilities{Model: "sonnet"}, Launch{Mode: ModeInteractive})
	opts := s.ACPMeta["claudeCode"].(map[string]any)["options"].(map[string]any)
	if opts["model"] != "sonnet" {
		t.Fatalf("model = %v", opts["model"])
	}
	if _, err := claude(t).Config(Capabilities{Grant: mapGrant(map[string][]string{"wiki": {"wiki_read"}})}, Launch{Mode: ModeInteractive}); err == nil {
		t.Fatal("acp with tools must error in plan 1")
	}
	if _, err := claude(t).Config(Capabilities{}, Launch{Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestClaudeMCPConfig_OnlyGrantedServers(t *testing.T) {
	s, _ := claude(t).Config(Capabilities{Grant: mapGrant(map[string][]string{"wiki": {"wiki_read"}})}, Launch{Mode: ModeHeadless})
	js := s.MCPConfig("http://host:7390/", "tok")
	var cfg map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(js), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg["mcpServers"]) != 1 {
		t.Fatalf("servers: %s", js)
	}
	e := cfg["mcpServers"]["wiki"]
	if e["type"] != "http" || e["url"] != "http://host:7390/mcp/wiki" {
		t.Fatalf("entry: %v", e)
	}
	if h := e["headers"].(map[string]any); h["Authorization"] != "Bearer tok" {
		t.Fatalf("headers: %v", h)
	}
	if strings.Contains(js, "jira") || strings.Contains(js, "\n") {
		t.Fatalf("bad config: %s", js)
	}
}

func TestClaudePrint_RejectsWildcardName(t *testing.T) {
	for _, in := range []map[string][]string{
		{"wiki": {"wiki_*"}}, {"w*": {"wiki_read"}}, {"wiki": {"*"}},
	} {
		if _, err := claude(t).Config(Capabilities{Grant: mapGrant(in)}, Launch{Mode: ModeHeadless}); err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
}

func TestClaudePrint_RejectsCommaName(t *testing.T) {
	for _, in := range []map[string][]string{
		{"wiki": {"x,WebFetch"}}, {"wiki,Bash": {"t"}}, {"wiki": {"a b"}},
	} {
		_, err := claude(t).Config(Capabilities{Grant: mapGrant(in)}, Launch{Mode: ModeHeadless})
		if err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
	_, err := claude(t).Config(Capabilities{Grant: mapGrant(map[string][]string{"wiki": {"x,WebFetch"}})}, Launch{Mode: ModeHeadless})
	if !strings.Contains(err.Error(), "x,WebFetch") {
		t.Fatalf("error should name the bad value: %v", err)
	}
}

func TestClaudePrint_RejectsEmptyName(t *testing.T) {
	for _, in := range []map[string][]string{
		{"": {"t"}}, {"wiki": {""}},
	} {
		if _, err := claude(t).Config(Capabilities{Grant: mapGrant(in)}, Launch{Mode: ModeHeadless}); err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
}

// A new print-mode session can be given its id up front (--session-id), so
// an engine can name a sandbox session before its first turn.
func TestClaudePrint_SessionID(t *testing.T) {
	id := "4bcd525c-3970-43f1-8420-b5e0d81d30b6"
	s, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless, SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := flagVal(s.Argv, "--session-id"); v != id {
		t.Fatalf("session-id = %q in %q", v, s.Argv)
	}
	if _, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless, SessionID: id, ResumeID: id}); err == nil {
		t.Fatal("SessionID with ResumeID accepted")
	}
	if _, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless, SessionID: "not-a-uuid"}); err == nil {
		t.Fatal("non-UUID session id accepted")
	}
	s, _ = claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	if _, ok := flagVal(s.Argv, "--session-id"); ok {
		t.Fatal("--session-id set without being asked")
	}
}

func TestClaudePrint_WebAllowsBothTools(t *testing.T) {
	s, err := claude(t).Config(Capabilities{Web: true}, Launch{Mode: ModeHeadless})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := flagVal(s.Argv, "--tools"); v != "WebSearch,WebFetch" {
		t.Fatalf("--tools = %q", v)
	}
	if v, _ := flagVal(s.Argv, "--allowedTools"); v != "WebFetch,WebSearch" {
		t.Fatalf("--allowedTools = %q", v)
	}
}

func TestClaudePrint_WebPlusMCP(t *testing.T) {
	s, err := claude(t).Config(Capabilities{Web: true, Grant: mapGrant(map[string][]string{"wiki": {"wiki_search"}})}, Launch{Mode: ModeHeadless})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := flagVal(s.Argv, "--allowedTools"); v != "WebFetch,WebSearch,mcp__wiki__wiki_search" {
		t.Fatalf("--allowedTools = %q", v)
	}
}

func TestClaudePrint_NoWebNoWebTools(t *testing.T) {
	s, err := claude(t).Config(Capabilities{Grant: mapGrant(map[string][]string{"wiki": {"wiki_search"}})}, Launch{Mode: ModeHeadless})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Argv {
		if strings.Contains(a, "WebSearch") || strings.Contains(a, "WebFetch") {
			t.Fatalf("web tool in argv: %q", s.Argv)
		}
	}
}

func TestClaudePrint_EnvDisablesTelemetry(t *testing.T) {
	want := map[string]string{"DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1"}
	for _, c := range []Capabilities{{}, {Web: true}} {
		s, err := claude(t).Config(c, Launch{Mode: ModeHeadless})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(s.Env, want) {
			t.Fatalf("Env = %v, want %v", s.Env, want)
		}
	}
}

// No grant and an empty grant are the same: no MCP tools, an MCP config
// listing no servers.
func TestClaudePrint_NilGrantIsNoTools(t *testing.T) {
	for _, g := range []ToolGrantView{nil, mapGrant{}} {
		s, err := claude(t).Config(Capabilities{Grant: g}, Launch{Mode: ModeHeadless})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := flagVal(s.Argv, "--allowedTools"); ok {
			t.Errorf("grant %v: --allowedTools in %q", g, s.Argv)
		}
		if got := s.MCPConfig("http://h", "tok"); got != `{"mcpServers":{}}` {
			t.Errorf("grant %v: MCP config %s", g, got)
		}
	}
	if _, err := claude(t).Config(Capabilities{Grant: mapGrant{}}, Launch{Mode: ModeInteractive}); err != nil {
		t.Errorf("acp with an empty grant: %v", err)
	}
}

// File grants carry the read and write paths (the policy enforces them);
// Claude Code needs no flag for them, so its launch is the same with or
// without.
func TestClaudePrint_FileGrantDoesNotChangeArgv(t *testing.T) {
	files := FileGrant{Read: []string{"/data/notes"}, Write: []string{"/data/out"}}
	if !reflect.DeepEqual(files.Read, []string{"/data/notes"}) || !reflect.DeepEqual(files.Write, []string{"/data/out"}) {
		t.Fatalf("file grant = %+v", files)
	}
	with, err := claude(t).Config(Capabilities{Files: files}, Launch{Mode: ModeHeadless})
	if err != nil {
		t.Fatal(err)
	}
	without, _ := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	if !reflect.DeepEqual(with.Argv, without.Argv) {
		t.Errorf("argv with files %q, without %q", with.Argv, without.Argv)
	}
}

// Launch modes are harness-neutral: Claude Code's own names for them
// ("print" for claude -p, "acp") are not modes.
func TestClaude_ModesAreHarnessNeutral(t *testing.T) {
	for _, m := range []string{"print", "acp", ""} {
		if _, err := claude(t).Config(Capabilities{}, Launch{Mode: m}); err == nil {
			t.Errorf("mode %q accepted", m)
		}
	}
	h, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	if err != nil || len(h.Argv) < 2 || h.Argv[0] != "claude" || h.Argv[1] != "-p" || h.ACPMeta != nil {
		t.Errorf("headless = %+v %v, want claude -p", h, err)
	}
	i, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeInteractive})
	if err != nil || i.ACPMeta == nil || i.Argv != nil {
		t.Errorf("interactive = %+v %v, want ACP metadata", i, err)
	}
}

// A turn with an image reads its prompt as one stream-json user message;
// a text-only turn keeps the plain text stdin (no --input-format).
func TestClaudePrint_StreamJSONInput(t *testing.T) {
	s, err := claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless, StreamJSONInput: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := flagVal(s.Argv, "--input-format"); v != "stream-json" {
		t.Fatalf("--input-format = %q in %q", v, s.Argv)
	}
	if v, _ := flagVal(s.Argv, "--output-format"); v != "stream-json" {
		t.Fatalf("--output-format = %q", v)
	}
	s, _ = claude(t).Config(Capabilities{}, Launch{Mode: ModeHeadless})
	if _, ok := flagVal(s.Argv, "--input-format"); ok {
		t.Fatalf("--input-format without an image: %q", s.Argv)
	}
}
