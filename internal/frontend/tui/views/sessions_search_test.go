package views

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/history"
)

func rankedSessions() []history.Session {
	now := time.Now()
	mk := func(id string, ago time.Duration) history.Session {
		return history.Session{ID: id, Project: "/p", StartTime: now.Add(-ago), LastActive: now.Add(-ago), TurnCount: 20, CostUSD: 1}
	}
	// newest first by date: recent, middle, old
	return []history.Session{mk("recent", time.Hour), mk("middle", 2*time.Hour), mk("old", 3*time.Hour)}
}

func visibleIDs(v *SessionsView) []string {
	var out []string
	for _, s := range v.visibleSessions() {
		out = append(out, s.ID)
	}
	return out
}

func TestContentSearch_UsesInjectedSearchAndRankOrder(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	v.SetSize(160, 40)
	var gotQuery string
	v.SetContentSearch(func(q string) ([]history.ContentMatch, error) {
		gotQuery = q
		// best match is the oldest session
		return []history.ContentMatch{{SessionID: "old", Snippet: "[token]"}, {SessionID: "recent", Snippet: "token"}}, nil
	})

	v.contentSearchMode = true
	v.contentSearchInput.SetValue("token")
	cmd := v.handleContentSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should start a search")
	}
	msg, ok := cmd().(SessionContentSearchResultMsg)
	if !ok || gotQuery != "token" {
		t.Fatalf("msg=%T query=%q", msg, gotQuery)
	}
	v.HandleContentSearchResult(msg)

	got := visibleIDs(v)
	if len(got) != 2 || got[0] != "old" || got[1] != "recent" {
		t.Errorf("visible = %v, want [old recent] (rank order, not date order)", got)
	}
	if v.ContentSearchSnippet("old") != "[token]" {
		t.Errorf("snippet = %q", v.ContentSearchSnippet("old"))
	}
}

func TestContentSearch_NoSearchConfigured(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	v.contentSearchMode = true
	v.contentSearchInput.SetValue("token")
	cmd := v.handleContentSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should still return a result message")
	}
	if msg := cmd().(SessionContentSearchResultMsg); len(msg.Matches) != 0 {
		t.Errorf("unconfigured search returned %v", msg.Matches)
	}
}

func TestContentSearch_ClearRestoresDateOrder(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	v.HandleContentSearchResult(SessionContentSearchResultMsg{Matches: []history.ContentMatch{{SessionID: "old"}}})
	v.clearContentSearch()
	if got := visibleIDs(v); len(got) != 3 || got[0] != "recent" {
		t.Errorf("after clear: %v, want all sessions newest first", got)
	}
}

func TestFilterKey_DeepSearchUsesIndexAndRanksContentFirst(t *testing.T) {
	v := NewSessionsView()
	sessions := rankedSessions()
	sessions[0].FirstPrompt = "middle ground" // "recent" matches the filter by metadata only
	v.SetSessions(sessions)
	called := false
	v.SetContentSearch(func(q string) ([]history.ContentMatch, error) {
		called = true
		return []history.ContentMatch{{SessionID: "old", Snippet: "[middle]"}}, nil
	})

	v.filterMode = true
	v.filterInput.SetValue("middle")
	cmd := v.handleFilterKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should start a deep search")
	}
	v.HandleContentSearchResult(cmd().(SessionContentSearchResultMsg))
	if !called {
		t.Fatal("the / filter must use the injected index search")
	}
	got := visibleIDs(v)
	if len(got) < 2 || got[0] != "old" {
		t.Errorf("visible = %v, want the ranked content match first, then metadata matches", got)
	}
}
