package sessionmcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/search"
)

const (
	sidDeck  = "aaaaaaaa-0000-0000-0000-000000000001"
	sidShell = "aaaaaaaa-1111-0000-0000-000000000002"
	sidAuto  = "cccccccc-0000-0000-0000-000000000003"
)

func writeSession(t *testing.T, root, project, sid string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func userLine(cwd, text string) string {
	return `{"type":"user","entrypoint":"cli","cwd":"` + cwd + `","message":{"content":"` + text + `"}}`
}

func replyLine(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

// newFixture indexes two human sessions sharing the prefix aaaaaaaa and one
// automated session, and returns the server plus the projects dir.
func newFixture(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	writeSession(t, dir, "-Users-me-research", sidDeck,
		userLine("/Users/me/research", "add the hypotheses slide to the Ying deck"), replyLine("Added after the jobs section."))
	writeSession(t, dir, "-Users-me-OpenShell", sidShell,
		userLine("/Users/me/OpenShell", "why does the sandbox need service accounts"), replyLine("Short-lived tokens."))
	writeSession(t, dir, "-private-var-folders-T-tmp", sidAuto,
		`{"type":"user","entrypoint":"sdk-cli","cwd":"/private/var/folders/T/tmp","message":{"content":"nightly job about service accounts"}}`,
		replyLine("done"))
	var notes bytes.Buffer
	return New(&search.Service{DBPath: filepath.Join(t.TempDir(), "search.db"), ProjectsDir: dir, Notes: &notes}), dir
}

func call(t *testing.T, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) (string, bool) {
	t.Helper()
	res, err := h(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	return res.Content[0].(mcp.TextContent).Text, res.IsError
}

func TestTools_Registered(t *testing.T) {
	s, _ := newFixture(t)
	srv := server.NewMCPServer("test", "0")
	s.Register(srv)
	var names []string
	for name := range srv.ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)
	want := "continue_session,create_virtual_session,get_session,list_sessions,search_sessions"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("tools = %s, want %s", got, want)
	}
}

func TestTools_RoutingDescriptions(t *testing.T) {
	s, _ := newFixture(t)
	srv := server.NewMCPServer("test", "0")
	s.Register(srv)
	cont := srv.GetTool("continue_session").Tool.Description
	get := srv.GetTool("get_session").Tool.Description
	if !strings.Contains(cont, "UUID") || !strings.Contains(cont, "claude --resume") {
		t.Errorf("continue_session description lacks routing guidance: %s", cont)
	}
	for name, d := range map[string]string{"get_session": get, "continue_session": cont} {
		if !strings.Contains(d, "~/.claude/projects") {
			t.Errorf("%s description should warn against building paths", name)
		}
	}
}

func TestSearch_KeywordWithoutKey(t *testing.T) {
	s, _ := newFixture(t)
	out, isErr := call(t, s.handleSearch, map[string]any{"query": "Ying deck"})
	if isErr || !strings.Contains(out, sidDeck) || !strings.Contains(out, "keyword") {
		t.Errorf("search: isErr=%v\n%s", isErr, out)
	}
	if out, isErr := call(t, s.handleSearch, map[string]any{"query": "x", "mode": "fuzzy"}); !isErr {
		t.Errorf("bad mode should be a tool error: %s", out)
	}
	if _, isErr := call(t, s.handleSearch, map[string]any{}); !isErr {
		t.Error("missing query should be a tool error")
	}
}

func TestSearch_HidesAutomatedUnlessAsked(t *testing.T) {
	s, _ := newFixture(t)
	if out, _ := call(t, s.handleSearch, map[string]any{"query": "service accounts", "limit": 10}); strings.Contains(out, sidAuto) {
		t.Errorf("automated session shown by default:\n%s", out)
	}
	if out, _ := call(t, s.handleSearch, map[string]any{"query": "service accounts", "limit": 10, "include_automated": true}); !strings.Contains(out, sidAuto) {
		t.Errorf("include_automated should show it:\n%s", out)
	}
}

func TestGet_AutomatedByExplicitID(t *testing.T) {
	s, _ := newFixture(t)
	out, isErr := call(t, s.handleGet, map[string]any{"session_id": sidAuto})
	if isErr || !strings.Contains(out, "automated") || !strings.Contains(out, "nightly job") {
		t.Errorf("get automated: isErr=%v\n%s", isErr, out)
	}
}

func TestGet_AmbiguousIsToolError(t *testing.T) {
	s, _ := newFixture(t)
	out, isErr := call(t, s.handleGet, map[string]any{"session_id": "aaaaaaaa"})
	if !isErr || !strings.Contains(out, sidDeck) || !strings.Contains(out, sidShell) {
		t.Errorf("ambiguous: isErr=%v\n%s", isErr, out)
	}
	if _, isErr := call(t, s.handleGet, map[string]any{"session_id": "zzzz"}); !isErr {
		t.Error("no match should be a tool error")
	}
}

func TestContinue_HasResumeLine(t *testing.T) {
	s, _ := newFixture(t)
	out, isErr := call(t, s.handleContinue, map[string]any{"session_id": "aaaaaaaa-0000"})
	if isErr || !strings.Contains(out, "cd /Users/me/research && claude --resume "+sidDeck) {
		t.Errorf("continue: isErr=%v\n%s", isErr, out)
	}
}

func TestVirtual_NeedsTwo(t *testing.T) {
	s, _ := newFixture(t)
	if _, isErr := call(t, s.handleVirtual, map[string]any{"session_ids": []any{sidDeck}}); !isErr {
		t.Error("one ID should be a tool error")
	}
	out, isErr := call(t, s.handleVirtual, map[string]any{"session_ids": []any{sidDeck, sidShell}})
	if isErr || strings.Count(out, "## Session ") != 2 {
		t.Errorf("virtual: isErr=%v\n%s", isErr, out)
	}
	if _, isErr := call(t, s.handleVirtual, map[string]any{"session_ids": []any{sidDeck, "aaaaaaaa"}}); !isErr {
		t.Error("an ambiguous ID among several should be a tool error")
	}
}

func TestList_DaysFilter(t *testing.T) {
	s, dir := newFixture(t)
	old := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(filepath.Join(dir, "-Users-me-OpenShell", sidShell+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	out, _ := call(t, s.handleList, map[string]any{})
	if strings.Contains(out, sidShell) || !strings.Contains(out, sidDeck) || strings.Contains(out, sidAuto) {
		t.Errorf("default list (30 days, no automated):\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"days": 60}); !strings.Contains(out, sidShell) {
		t.Errorf("days=60 should include the 40-day-old session:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"project_filter": "/Users/me/OpenShell", "days": 60}); strings.Contains(out, sidDeck) || !strings.Contains(out, sidShell) {
		t.Errorf("project_filter:\n%s", out)
	}
}
