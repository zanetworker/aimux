package history

import (
	"reflect"
	"testing"
	"time"
)

func visibleFixture() map[string]Session {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := func(cost float64, turns int) Session {
		return Session{CostUSD: cost, TurnCount: turns, StartTime: at, LastActive: at}
	}
	m := map[string]Session{}
	m["plain"] = s(1, 10)
	m["empty"] = s(0, 5) // boundary: 5 turns at no cost is near-empty
	m["six"] = s(0, 6)   // 6 turns is not
	sub := s(1, 10)
	sub.IsSubagent = true
	m["sub"] = sub
	auto := s(1, 10)
	auto.Automated = true
	m["auto"] = auto
	autoEmpty := s(0, 2)
	autoEmpty.Automated = true
	m["autoEmpty"] = autoEmpty
	hook := s(1, 10)
	hook.FirstPrompt = "YOU ARE A helper"
	m["hook"] = hook
	analyzer := s(1, 10)
	analyzer.Title = "please act as a session analyzer"
	m["analyzer"] = analyzer
	m["undated"] = Session{CostUSD: 1, TurnCount: 10}
	half := Session{CostUSD: 1, TurnCount: 10, LastActive: at}
	m["halfDated"] = half
	return m
}

func TestKinds(t *testing.T) {
	f := visibleFixture()
	cases := map[string]Kind{
		"plain":     0,
		"empty":     KindNearEmpty,
		"six":       0,
		"sub":       KindSubagent,
		"auto":      KindAutomated,
		"autoEmpty": KindAutomated | KindNearEmpty,
		"hook":      KindAnalyzer, // a hook prompt is an analyzer session
		"analyzer":  KindAnalyzer,
		"undated":   KindUndated,
		"halfDated": 0, // undated means neither time is known
	}
	for id, want := range cases {
		if got := Kinds(f[id]); got != want {
			t.Errorf("%s: Kinds = %b, want %b", id, got, want)
		}
	}
	both := Session{FirstPrompt: "YOU ARE A SESSION ANALYZER", CostUSD: 1, TurnCount: 9, LastActive: time.Now()}
	if got := Kinds(both); got != KindAnalyzer {
		t.Errorf("hook and analyzer text: %b, want one analyzer kind", got)
	}
	if Kinds(Session{Title: "Analyze the session transcript", CostUSD: 1, TurnCount: 9, LastActive: time.Now()}) != KindAnalyzer {
		t.Error("a hook prefix in the title is an analyzer")
	}
}

func TestVisible_HideAndReveal(t *testing.T) {
	f := visibleFixture()
	o := VisibleOpts{Hide: KindSubagent | KindNearEmpty}
	if !Visible(f["plain"], o) || Visible(f["sub"], o) || Visible(f["empty"], o) {
		t.Error("hidden kinds hide, others show")
	}
	if !Visible(f["auto"], o) {
		t.Error("a kind the list does not hide shows")
	}
	o.Reveal = KindSubagent
	if !Visible(f["sub"], o) || Visible(f["empty"], o) {
		t.Error("revealing one kind shows only that kind")
	}
}

func TestVisible_AutomatedOverridesNoise(t *testing.T) {
	f := visibleFixture()
	o := VisibleOpts{Hide: KindAutomated | KindNearEmpty, Reveal: KindAutomated}
	if Visible(f["autoEmpty"], o) {
		t.Error("without the option, noise hides a revealed automated session")
	}
	o.AutomatedOverridesNoise = true
	if !Visible(f["autoEmpty"], o) {
		t.Error("with the option, a revealed automated session shows whole")
	}
	if Visible(f["empty"], o) {
		t.Error("the option does not lift noise from interactive sessions")
	}
	o.Reveal = 0
	if Visible(f["autoEmpty"], o) {
		t.Error("the option needs automated revealed")
	}
}

func TestHiddenUntil(t *testing.T) {
	f := visibleFixture()
	o := DashboardOpts()
	reveals := DashboardReveals
	cases := map[string][]string{
		"plain":     {},
		"empty":     {"search"},
		"sub":       {"subagents"},
		"analyzer":  {"analyzers"},
		"hook":      {"analyzers"}, // hook prompts are analyzers on every surface
		"auto":      {"automated"},
		"autoEmpty": {"automated"}, // Automated shows automated sessions whole, like TUI H
		"undated":   {},
	}
	for id, want := range cases {
		if got := HiddenUntil(f[id], o, reveals); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
	subEmpty := f["empty"]
	subEmpty.IsSubagent = true
	if got := HiddenUntil(subEmpty, o, reveals); !reflect.DeepEqual(got, []string{"subagents", "search"}) {
		t.Errorf("two reasons need both reveals: %v", got)
	}
	autoAnalyzer := f["auto"]
	autoAnalyzer.FirstPrompt = "YOU ARE A SESSION ANALYZER"
	if got := HiddenUntil(autoAnalyzer, o, reveals); !reflect.DeepEqual(got, []string{"automated"}) {
		t.Errorf("revealing automated shows it whole: %v", got)
	}
	autoSub := f["auto"]
	autoSub.IsSubagent = true
	if got := HiddenUntil(autoSub, o, reveals); !reflect.DeepEqual(got, []string{"subagents", "automated"}) {
		t.Errorf("the override lifts only noise: %v", got)
	}
}

func TestSurfaceOpts(t *testing.T) {
	f := visibleFixture()
	shown := func(o VisibleOpts) map[string]bool {
		m := map[string]bool{}
		for id, s := range f {
			if Visible(s, o) {
				m[id] = true
			}
		}
		return m
	}
	set := func(ids ...string) map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	all := set("plain", "empty", "six", "sub", "auto", "autoEmpty", "hook", "analyzer", "undated", "halfDated")
	cases := []struct {
		name string
		o    VisibleOpts
		want map[string]bool
	}{
		{"tui default", TUIOpts(false, false), set("plain", "six", "halfDated")},
		{"tui H", TUIOpts(true, false), set("plain", "six", "halfDated", "sub", "auto", "autoEmpty")},
		{"tui search", TUIOpts(false, true), all},
		{"dashboard", DashboardOpts(), set("plain", "six", "undated", "halfDated")},
		{"remote", RemoteOpts(false, false), set("plain", "empty", "six", "hook", "analyzer", "undated", "halfDated")},
		{"remote automated", RemoteOpts(true, false), set("plain", "empty", "six", "hook", "analyzer", "undated", "halfDated", "auto", "autoEmpty")},
		{"cli", CLIOpts(false), set("plain", "six", "hook", "analyzer", "halfDated")},
		{"cli automated", CLIOpts(true), set("plain", "six", "hook", "analyzer", "halfDated", "auto")},
		{"remote created", RemoteOpts(false, true), set("plain", "empty", "six", "hook", "analyzer", "undated", "halfDated", "auto", "autoEmpty")},
		{"mcp", MCPOpts(false), set("plain", "empty", "six", "undated", "halfDated")},
		{"mcp automated", MCPOpts(true), set("plain", "empty", "six", "hook", "analyzer", "undated", "halfDated", "auto", "autoEmpty")},
	}
	for _, c := range cases {
		if got := shown(c.o); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v\nwant %v", c.name, got, c.want)
		}
	}
}
