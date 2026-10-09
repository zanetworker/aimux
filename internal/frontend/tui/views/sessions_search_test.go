package views

import (
	"errors"
	"strings"
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
	v.SetContentSearch(func(q, _ string) ([]history.ContentMatch, error) {
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
	v.SetContentSearch(func(q, _ string) ([]history.ContentMatch, error) {
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

func startSearch(v *SessionsView, q string) tea.Cmd {
	v.contentSearchMode = true
	v.contentSearchInput.SetValue(q)
	return v.handleContentSearchKey(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestContentSearch_IgnoresStaleResults(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	v.SetContentSearch(func(q, _ string) ([]history.ContentMatch, error) {
		if q == "first" {
			return []history.ContentMatch{{SessionID: "old"}}, nil
		}
		return []history.ContentMatch{{SessionID: "recent"}}, nil
	})
	first := startSearch(v, "first")
	second := startSearch(v, "second")
	v.HandleContentSearchResult(second().(SessionContentSearchResultMsg))
	v.HandleContentSearchResult(first().(SessionContentSearchResultMsg)) // arrives late
	if got := visibleIDs(v); len(got) != 1 || got[0] != "recent" {
		t.Errorf("visible = %v, want only the newer search's result", got)
	}

	// a result arriving after the search was cleared is dropped too
	late := startSearch(v, "first")
	v.clearContentSearch()
	v.HandleContentSearchResult(late().(SessionContentSearchResultMsg))
	if v.HasActiveContentSearch() {
		t.Error("cleared search was revived by a late result")
	}
}

func TestContentSearch_FailureIsShownNotEmpty(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	v.SetSize(160, 40)
	v.SetContentSearch(func(string, string) ([]history.ContentMatch, error) {
		return nil, errors.New("index locked")
	})
	cmd := startSearch(v, "token")
	if !strings.Contains(v.View(), "searching content") {
		t.Error("while a search runs the view should say so")
	}
	v.HandleContentSearchResult(cmd().(SessionContentSearchResultMsg))
	out := v.View()
	if !strings.Contains(out, "content search failed") || !strings.Contains(out, "index locked") {
		t.Errorf("failure not shown:\n%s", out)
	}
}

func TestFilter_NoContentMatchesKeepsSelectedSort(t *testing.T) {
	v := NewSessionsView()
	sessions := rankedSessions()
	for i := range sessions {
		sessions[i].FirstPrompt = "shared word"
	}
	// shuffled input: the expected order must come from sorting, not from input order
	v.SetSessions([]history.Session{sessions[2], sessions[0], sessions[1]})
	v.SetContentSearch(func(string, string) ([]history.ContentMatch, error) { return nil, nil })
	v.filterMode = true
	v.filterInput.SetValue("shared")
	cmd := v.handleFilterKey(tea.KeyMsg{Type: tea.KeyEnter})
	v.HandleContentSearchResult(cmd().(SessionContentSearchResultMsg))
	got := visibleIDs(v)
	if len(got) != 3 || got[0] != "recent" || got[2] != "old" {
		t.Errorf("visible = %v, want metadata matches in the normal (newest first) order", got)
	}
}

func TestContentSearch_PassesTheViewScope(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	var gotDir string
	v.SetContentSearch(func(_ string, dir string) ([]history.ContentMatch, error) {
		gotDir = dir
		return nil, nil
	})

	v.SetShowAll(false)
	v.SetCurrentDir("/Users/me/research")
	want := v.CurrentDir()
	startSearch(v, "token")()
	if gotDir != want {
		t.Errorf("scoped view searched dir %q, want %q (the search must scope before its limit)", gotDir, want)
	}

	v.SetShowAll(true)
	startSearch(v, "token")()
	if gotDir != "" {
		t.Errorf("all-projects view searched dir %q, want all projects", gotDir)
	}
}

func TestContentSearch_SameQueryDifferentScopeKeepsLatest(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(rankedSessions())
	v.SetCurrentDir("/Users/me/research")
	v.SetContentSearch(func(_ string, dir string) ([]history.ContentMatch, error) {
		if dir == "" {
			return []history.ContentMatch{{SessionID: "old"}}, nil
		}
		return []history.ContentMatch{{SessionID: "recent"}}, nil
	})
	v.SetShowAll(true)
	allProjects := startSearch(v, "token")
	v.SetShowAll(false)
	scoped := startSearch(v, "token")
	v.HandleContentSearchResult(scoped().(SessionContentSearchResultMsg))
	v.HandleContentSearchResult(allProjects().(SessionContentSearchResultMsg)) // older, arrives last
	if got := visibleIDs(v); len(got) != 1 || got[0] != "recent" {
		t.Errorf("visible = %v, want the latest search's result only", got)
	}
}
