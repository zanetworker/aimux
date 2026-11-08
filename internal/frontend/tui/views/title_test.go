package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

// titleSessions are one session per step of the title rule: a meta title,
// an ai-title only, and only a first message. Each would be listed (more
// than one turn, some cost).
func titleSessions() []history.Session {
	now := time.Now()
	base := func(id string) history.Session {
		return history.Session{ID: id, Provider: "claude", Project: "/p", TurnCount: 9, CostUSD: 0.5,
			StartTime: now.Add(-time.Hour), LastActive: now, Resumable: true}
	}
	meta, ai, first := base("meta-1"), base("ai-2"), base("first-3")
	meta.Title, meta.AITitle, meta.FirstPrompt, meta.LastPrompt = "Meta title", "Meta ai name", "meta first message", "meta last"
	ai.AITitle, ai.FirstPrompt, ai.LastPrompt = "AI only name", "ai first message", "ai last"
	first.FirstPrompt, first.LastPrompt = "plain first message", "plain last"
	return []history.Session{meta, ai, first}
}

func assertTitles(t *testing.T, where, out string) {
	t.Helper()
	for _, want := range []string{"Meta title", "AI only name", "plain first message"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: missing title %q in:\n%s", where, want, out)
		}
	}
	for _, not := range []string{"meta first message", "Meta ai name", "ai first message", "plain last"} {
		if strings.Contains(out, not) {
			t.Errorf("%s: shows %q instead of the title:\n%s", where, not, out)
		}
	}
}

func TestSessionsView_RowsShowTheTitleRule(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(titleSessions())
	v.SetSize(200, 40)
	var rows []string
	for _, s := range titleSessions() {
		rows = append(rows, v.renderSessionRow(s, false, 200))
	}
	assertTitles(t, "rows", strings.Join(rows, "\n"))
}

func TestSessionsView_DeletePromptShowsTheTitleRule(t *testing.T) {
	var out []string
	for i := range titleSessions() {
		v := NewSessionsView()
		v.SetSessions(titleSessions())
		v.SetSize(200, 40)
		v.cursor = i
		v.deleteMode = true
		out = append(out, v.View())
	}
	all := strings.Join(out, "\n")
	for _, want := range []string{`Delete "Meta title"`, `Delete "AI only name"`, `Delete "plain first message"`} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestSessionsView_CleanupShowsTheTitleRule(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(200, 40)
	for _, s := range titleSessions() {
		v.cleanupItems = append(v.cleanupItems, cleanupItem{session: s, reason: "duplicate"})
	}
	v.cleanupMode = true
	assertTitles(t, "cleanup", v.renderCleanupView(200))
}

func TestSessionTitle_SortKeyIsTheShownTitle(t *testing.T) {
	want := []string{"meta title", "ai only name", "plain first message"}
	for i, s := range titleSessions() {
		if got := sessionTitle(s); got != want[i] {
			t.Errorf("sessionTitle(%s) = %q, want %q", s.ID, got, want[i])
		}
	}
}

func TestLauncher_ResumeShowsTheTitleRule(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{})
	l.resumeSessions = titleSessions()
	l.state = statePickResume
	assertTitles(t, "launcher resume", l.viewResume())
}

func TestPreviewHeader_ShowsTheTitleRule(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	user := func(text string) string {
		return `{"type":"user","message":{"role":"user","content":"` + text + `"}}`
	}
	meta := write("m.jsonl", user("meta first message"), `{"type":"ai-title","aiTitle":"Meta ai name"}`)
	if err := history.UpdateMeta(meta, func(m *history.Meta) { m.Title = "Meta title" }); err != nil {
		t.Fatal(err)
	}
	files := []string{meta,
		write("a.jsonl", user("ai first message"), `{"type":"ai-title","aiTitle":"AI only name"}`),
		write("f.jsonl", user("plain first message"))}
	var out []string
	for i, f := range files {
		p := NewPreviewPane()
		p.SetSize(200, 40)
		p.agent = &agent.Agent{PID: i + 1, Name: "x", SessionFile: f}
		out = append(out, p.renderHeader())
	}
	assertTitles(t, "preview", strings.Join(out, "\n"))
}

func TestSessionMatchesFilter_FindsTheShownTitle(t *testing.T) {
	for _, s := range titleSessions() {
		needle := strings.ToLower(history.ShownTitle(s))
		if !sessionMatchesFilter(s, needle) {
			t.Errorf("%s: filter %q does not find its own title", s.ID, needle)
		}
	}
	if sessionMatchesFilter(titleSessions()[1], "nothing like this") {
		t.Error("filter matches unrelated text")
	}
}

func TestSessionsView_DeletePromptAndCleanupCutByRunes(t *testing.T) {
	s := titleSessions()[0]
	s.Title = strings.Repeat("é", 60)
	v := NewSessionsView()
	v.SetSessions([]history.Session{s})
	v.SetSize(200, 40)
	v.deleteMode = true
	out := v.View()
	prompt := out[strings.Index(out, "Delete \""):] // the list row uses the shared byte-based truncate
	if !utf8.ValidString(prompt) || !strings.Contains(prompt, `Delete "`+strings.Repeat("é", 47)+`..."`) {
		t.Errorf("delete prompt not cut by runes:\n%q", out)
	}
	v = NewSessionsView()
	v.SetSize(200, 40)
	v.cleanupItems = []cleanupItem{{session: s, reason: "duplicate"}}
	v.cleanupMode = true
	if out := v.renderCleanupView(200); !utf8.ValidString(out) || !strings.Contains(out, strings.Repeat("é", 47)+"...") {
		t.Errorf("cleanup not cut by runes:\n%q", out)
	}
}
