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

func flagVal(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

func TestClaudePrint_ExactAllowedTools(t *testing.T) {
	s, err := claude(t).Config(Intent{Mode: "print", Tools: map[string][]string{
		"wiki": {"wiki_search", "wiki_read"},
		"jira": {"get_issue_details"},
	}})
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

func TestClaudePrint_NoWebUnlessIntent(t *testing.T) {
	off, _ := claude(t).Config(Intent{Mode: "print"})
	if v, _ := flagVal(off.Argv, "--tools"); v != "" {
		t.Fatalf("--tools = %q, want empty", v)
	}
	on, _ := claude(t).Config(Intent{Mode: "print", Web: true})
	if v, _ := flagVal(on.Argv, "--tools"); v != "WebSearch,WebFetch" {
		t.Fatalf("--tools = %q", v)
	}
}

func TestClaudePrint_NoUserSettings(t *testing.T) {
	s, _ := claude(t).Config(Intent{Mode: "print"})
	if v, ok := flagVal(s.Argv, "--setting-sources"); !ok || v != "" {
		t.Fatalf("--setting-sources = %q ok=%v", v, ok)
	}
	s, _ = claude(t).Config(Intent{Mode: "print", InheritUser: true})
	if _, ok := flagVal(s.Argv, "--setting-sources"); ok {
		t.Fatalf("InheritUser must drop --setting-sources: %q", s.Argv)
	}
}

func TestClaudePrint_ResumeAndModel(t *testing.T) {
	s, _ := claude(t).Config(Intent{Mode: "print", Model: "sonnet", ResumeID: "sess-1"})
	if v, _ := flagVal(s.Argv, "--model"); v != "sonnet" {
		t.Fatalf("model = %q", v)
	}
	if v, _ := flagVal(s.Argv, "--resume"); v != "sess-1" {
		t.Fatalf("resume = %q", v)
	}
	s, _ = claude(t).Config(Intent{Mode: "print"})
	for _, f := range []string{"--model", "--resume"} {
		if _, ok := flagVal(s.Argv, f); ok {
			t.Fatalf("%s set without intent", f)
		}
	}
}

func TestClaudeACP_PageMetaLocksEverything(t *testing.T) {
	s, err := claude(t).Config(Intent{Mode: "acp"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"tools": []any{}, "settingSources": []any{}, "mcpServers": map[string]any{}, "strictMcpConfig": true,
	}}}
	if !reflect.DeepEqual(s.ACPMeta, want) {
		t.Fatalf("meta = %#v", s.ACPMeta)
	}
	s, _ = claude(t).Config(Intent{Mode: "acp", Model: "sonnet"})
	opts := s.ACPMeta["claudeCode"].(map[string]any)["options"].(map[string]any)
	if opts["model"] != "sonnet" {
		t.Fatalf("model = %v", opts["model"])
	}
	if _, err := claude(t).Config(Intent{Mode: "acp", Tools: map[string][]string{"wiki": {"wiki_read"}}}); err == nil {
		t.Fatal("acp with tools must error in plan 1")
	}
	if _, err := claude(t).Config(Intent{Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestClaudeMCPConfig_OnlyIntentServers(t *testing.T) {
	s, _ := claude(t).Config(Intent{Mode: "print", Tools: map[string][]string{"wiki": {"wiki_read"}}})
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
		if _, err := claude(t).Config(Intent{Mode: "print", Tools: in}); err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
}

func TestClaudePrint_RejectsCommaName(t *testing.T) {
	for _, in := range []map[string][]string{
		{"wiki": {"x,WebFetch"}}, {"wiki,Bash": {"t"}}, {"wiki": {"a b"}},
	} {
		_, err := claude(t).Config(Intent{Mode: "print", Tools: in})
		if err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
	_, err := claude(t).Config(Intent{Mode: "print", Tools: map[string][]string{"wiki": {"x,WebFetch"}}})
	if !strings.Contains(err.Error(), "x,WebFetch") {
		t.Fatalf("error should name the bad value: %v", err)
	}
}

func TestClaudePrint_RejectsEmptyName(t *testing.T) {
	for _, in := range []map[string][]string{
		{"": {"t"}}, {"wiki": {""}},
	} {
		if _, err := claude(t).Config(Intent{Mode: "print", Tools: in}); err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
}

// A new print-mode session can be given its id up front (--session-id), so
// an engine can name a sandbox session before its first turn.
func TestClaudePrint_SessionID(t *testing.T) {
	id := "4bcd525c-3970-43f1-8420-b5e0d81d30b6"
	s, err := claude(t).Config(Intent{Mode: "print", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := flagVal(s.Argv, "--session-id"); v != id {
		t.Fatalf("session-id = %q in %q", v, s.Argv)
	}
	if _, err := claude(t).Config(Intent{Mode: "print", SessionID: id, ResumeID: id}); err == nil {
		t.Fatal("SessionID with ResumeID accepted")
	}
	if _, err := claude(t).Config(Intent{Mode: "print", SessionID: "not-a-uuid"}); err == nil {
		t.Fatal("non-UUID session id accepted")
	}
	s, _ = claude(t).Config(Intent{Mode: "print"})
	if _, ok := flagVal(s.Argv, "--session-id"); ok {
		t.Fatal("--session-id set without intent")
	}
}
