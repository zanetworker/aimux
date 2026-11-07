package views

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

// visibilityFixture has one session per reason a list may hide it.
func visibilityFixture() []history.Session {
	t := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := func(id string, cost float64, turns int) history.Session {
		return history.Session{ID: id, Project: "/w", CostUSD: cost, TurnCount: turns, StartTime: t, LastActive: t}
	}
	plain := s("plain", 1, 10)
	empty := s("empty", 0, 2)
	sub := s("sub", 1, 10)
	sub.IsSubagent = true
	subEmpty := s("subEmpty", 0, 2)
	subEmpty.IsSubagent = true
	auto := s("auto", 1, 10)
	auto.Automated = true
	autoEmpty := s("autoEmpty", 0, 2)
	autoEmpty.Automated = true
	hook := s("hook", 1, 10)
	hook.FirstPrompt = "YOU ARE A helper"
	hookTitle := s("hookTitle", 1, 10)
	hookTitle.Title = "Analyze the session transcript of x"
	analyzer := s("analyzer", 1, 10)
	analyzer.FirstPrompt = "please act as a session analyzer now"
	analyzerEmpty := s("analyzerEmpty", 0, 2)
	analyzerEmpty.Title = "the SESSION ANALYZER"
	autoHook := s("autoHook", 1, 10)
	autoHook.Automated = true
	autoHook.FirstPrompt = "YOU ARE A SESSION ANALYZER"
	undated := history.Session{ID: "undated", Project: "/w", CostUSD: 1, TurnCount: 10}
	cheap := s("cheap", 0.01, 1)
	return []history.Session{plain, empty, sub, subEmpty, auto, autoEmpty, hook, hookTitle,
		analyzer, analyzerEmpty, autoHook, undated, cheap}
}

func sortedIDs(ss []history.Session) string {
	ids := make([]string, 0, len(ss))
	for _, s := range ss {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, " ")
}

// TestSessionsView_VisibleSetPinned pins what the sessions list shows for
// each state of H and search, so the shared rule cannot change it.
func TestSessionsView_VisibleSetPinned(t *testing.T) {
	cases := []struct {
		name      string
		h, search bool
		want      string
	}{
		// "analyzer" (SESSION ANALYZER mid-prompt) is hidden since analyzer
		// sessions became one kind on every surface; H does not reveal it.
		{"default", false, false, "cheap plain"},
		{"H", true, false, "auto autoEmpty autoHook cheap plain sub"},
		{"search", false, true, "analyzer analyzerEmpty auto autoEmpty autoHook cheap empty hook hookTitle plain sub subEmpty undated"},
		{"H and search", true, true, "analyzer analyzerEmpty auto autoEmpty autoHook cheap empty hook hookTitle plain sub subEmpty undated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := NewSessionsView()
			v.SetSessions(visibilityFixture())
			v.showSubagents = c.h
			if c.search {
				v.contentSearchIDs = map[string]string{}
				for _, s := range visibilityFixture() {
					v.contentSearchIDs[s.ID] = "x"
				}
			}
			if got := sortedIDs(v.visibleSessions()); got != c.want {
				t.Errorf("visible = %q\nwant      %q", got, c.want)
			}
		})
	}
}

// TestSessionsView_HiddenCountsPinned: the header's "+N agent" and
// "+M automated" count only sessions H would show (a near-empty subagent
// stays hidden with H, so it is not counted).
func TestSessionsView_HiddenCountsPinned(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(visibilityFixture())
	v.SetSize(200, 40)
	out := v.View()
	if !strings.Contains(out, "(+1 agent)") || !strings.Contains(out, "(+3 automated, H to show)") {
		t.Errorf("header counts missing: %q", out[:min(len(out), 400)])
	}
}
