package web

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

// dashboardFixture has one session per reason the dashboard hid it before
// the rule moved to the server: subagents (toggle), analyzers (toggle),
// near-empty (any search), and automated (toggle).
func dashboardFixture() []history.Session {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := func(id string, cost float64, turns int) history.Session {
		return history.Session{ID: id, CostUSD: cost, TurnCount: turns, StartTime: at, LastActive: at}
	}
	sub := s("sub", 1, 10)
	sub.IsSubagent = true
	subEmpty := s("subEmpty", 0, 2)
	subEmpty.IsSubagent = true
	analyzer := s("analyzer", 1, 10)
	analyzer.FirstPrompt = "please act as a session analyzer now"
	analyzerEmpty := s("analyzerEmpty", 0, 2)
	analyzerEmpty.Title = "the SESSION ANALYZER"
	hook := s("hook", 1, 10)
	hook.FirstPrompt = "YOU ARE A helper"
	auto := s("auto", 1, 10)
	auto.Automated = true
	autoEmpty := s("autoEmpty", 0, 2)
	autoEmpty.Automated = true
	autoAnalyzer := s("autoAnalyzer", 1, 10)
	autoAnalyzer.Automated = true
	autoAnalyzer.FirstPrompt = "YOU ARE A SESSION ANALYZER"
	return []history.Session{s("plain", 1, 10), s("empty", 0, 5), s("six", 0, 6), sub, subEmpty,
		analyzer, analyzerEmpty, hook, auto, autoEmpty, autoAnalyzer, {ID: "undated", CostUSD: 1, TurnCount: 10}}
}

func hiddenByID(rows []map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, r := range rows {
		out[r["id"].(string)] = r["hidden"].([]string)
	}
	return out
}

func TestDashboardHistory_HiddenFlag(t *testing.T) {
	// hook moved from {} to analyzers: a hook prompt is an analyzer on every
	// surface. Automated rows are now sent behind the Automated switch.
	want := map[string][]string{
		"plain": {}, "six": {}, "undated": {},
		"empty":         {"search"},
		"sub":           {"subagents"},
		"subEmpty":      {"subagents", "search"},
		"analyzer":      {"analyzers"},
		"analyzerEmpty": {"analyzers", "search"},
		"hook":          {"analyzers"},
		"auto":          {"automated"},
		"autoEmpty":     {"automated"}, // shown whole, like TUI H
		"autoAnalyzer":  {"automated"},
	}
	if got := hiddenByID(dashboardHistory(dashboardFixture())); !reflect.DeepEqual(got, want) {
		t.Errorf("hidden = %v\nwant     %v", got, want)
	}
}

// TestDashboardHistory_ClientSets applies the React app's reading of
// hidden (shown when every named reveal is on) and checks each toggle state
// gives the set the old React rule gave.
func TestDashboardHistory_ClientSets(t *testing.T) {
	rows := dashboardHistory(dashboardFixture())
	shown := func(on ...string) string {
		active := map[string]bool{}
		for _, r := range on {
			active[r] = true
		}
		var ids []string
		for _, r := range rows {
			ok := true
			for _, need := range r["hidden"].([]string) {
				ok = ok && active[need]
			}
			if ok {
				ids = append(ids, r["id"].(string))
			}
		}
		sort.Strings(ids)
		return strings.Join(ids, " ")
	}
	for _, c := range []struct {
		on   []string
		want string
	}{
		{nil, "plain six undated"},
		{[]string{"subagents"}, "plain six sub undated"},
		{[]string{"analyzers"}, "analyzer hook plain six undated"},
		{[]string{"search"}, "empty plain six undated"},
		{[]string{"automated"}, "auto autoAnalyzer autoEmpty plain six undated"},
		{[]string{"subagents", "automated", "analyzers", "search"}, "analyzer analyzerEmpty auto autoAnalyzer autoEmpty empty hook plain six sub subEmpty undated"},
	} {
		if got := shown(c.on...); got != c.want {
			t.Errorf("%v: %q, want %q", c.on, got, c.want)
		}
	}
}

// A kind the dashboard hides but no client switch reveals must not reach
// the client: its hidden list would be empty and the app would show it.
func TestHistoryRows_DropsKindsNoRevealCovers(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	empty := history.Session{ID: "empty", TurnCount: 2, StartTime: at, LastActive: at}
	sub := history.Session{ID: "sub", IsSubagent: true, CostUSD: 1, TurnCount: 10, StartTime: at, LastActive: at}
	noSearch := []history.Reveal{{Name: "subagents", Kinds: history.KindSubagent}}
	rows := historyRows([]history.Session{empty, sub}, history.DashboardOpts(), noSearch)
	if got := hiddenByID(rows); len(got) != 1 || !reflect.DeepEqual(got["sub"], []string{"subagents"}) {
		t.Errorf("rows %v, want only sub (empty has no reveal)", got)
	}
}

// Every kind DashboardOpts hides is either revealed by a switch the app
// offers or dropped by the server.
func TestDashboardHistory_EveryHiddenKindRevealedOrDropped(t *testing.T) {
	var covered history.Kind
	for _, r := range history.DashboardReveals {
		covered |= r.Kinds
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	perKind := map[history.Kind]history.Session{
		history.KindSubagent:  {ID: "sub", IsSubagent: true, CostUSD: 1, TurnCount: 10, StartTime: at, LastActive: at},
		history.KindAutomated: {ID: "auto", Automated: true, CostUSD: 1, TurnCount: 10, StartTime: at, LastActive: at},
		history.KindAnalyzer:  {ID: "an", Title: "session analyzer", CostUSD: 1, TurnCount: 10, StartTime: at, LastActive: at},
		history.KindNearEmpty: {ID: "empty", TurnCount: 2, StartTime: at, LastActive: at},
		history.KindUndated:   {ID: "undated", CostUSD: 1, TurnCount: 10},
	}
	hide := history.DashboardOpts().Hide
	for k, s := range perKind {
		if history.Kinds(s) != k {
			t.Fatalf("fixture %s has kinds %b, want %b", s.ID, history.Kinds(s), k)
		}
		rows := hiddenByID(dashboardHistory([]history.Session{s}))
		h, listed := rows[s.ID]
		switch {
		case hide&k == 0: // not hidden by the dashboard: listed, nothing needed
			if !listed || len(h) != 0 {
				t.Errorf("%s: %v listed=%v, want shown", s.ID, h, listed)
			}
		case covered&k != 0:
			if !listed || len(h) == 0 {
				t.Errorf("%s: %v listed=%v, want listed behind a reveal", s.ID, h, listed)
			}
		default:
			if listed {
				t.Errorf("%s: listed with %v, want dropped (no reveal covers it)", s.ID, h)
			}
		}
	}
}
