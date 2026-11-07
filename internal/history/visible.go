package history

import "strings"

// Which history sessions a list shows: one rule for every surface.
//
// Kinds classifies a session by every reason a list might hide it. A list
// says which kinds it hides (VisibleOpts.Hide) and which the viewer has
// revealed (Reveal: a toggle, a flag, a search); a session shows when none
// of its hidden kinds is left unrevealed. The surfaces differ only in their
// options, kept side by side below (TUIOpts, DashboardOpts, RemoteOpts,
// CLIOpts, MCPOpts).
// Live agents (controller.DropEphemeral, DedupBySession) are a different
// rule and are not covered here.

// Kind is a set of reasons a list may hide a session.
type Kind uint8

const (
	KindSubagent  Kind = 1 << iota // a subagent's transcript
	KindAutomated                  // started by a program (IsAutomated)
	KindAnalyzer                   // started by aimux's own analysis or hook prompts
	KindNearEmpty                  // no cost and at most 5 turns
	KindUndated                    // neither a start nor a last-active time
)

// noise are the kinds that only guess a session is uninteresting.
const noise = KindAnalyzer | KindNearEmpty

var hookPrefixes = []string{"YOU ARE A SESSION ANALYZER", "YOU ARE A ", "Analyze the session transcript"}

// Kinds returns every reason a list might hide s.
func Kinds(s Session) Kind {
	var k Kind
	if s.IsSubagent {
		k |= KindSubagent
	}
	if s.Automated {
		k |= KindAutomated
	}
	if isAnalyzer(s) {
		k |= KindAnalyzer
	}
	if s.CostUSD == 0 && s.TurnCount <= 5 {
		k |= KindNearEmpty
	}
	if s.StartTime.IsZero() && s.LastActive.IsZero() {
		k |= KindUndated
	}
	return k
}

// isAnalyzer: a hook or analysis prompt opens the first prompt or title,
// or either mentions SESSION ANALYZER anywhere (any case).
func isAnalyzer(s Session) bool {
	for _, p := range hookPrefixes {
		if strings.HasPrefix(s.FirstPrompt, p) || strings.HasPrefix(s.Title, p) {
			return true
		}
	}
	return strings.Contains(strings.ToUpper(s.FirstPrompt), "SESSION ANALYZER") ||
		strings.Contains(strings.ToUpper(s.Title), "SESSION ANALYZER")
}

// VisibleOpts is one list's use of the rule.
type VisibleOpts struct {
	Hide   Kind // kinds this list hides
	Reveal Kind // kinds the viewer has asked to see
	// AutomatedOverridesNoise: once automated sessions are revealed, the
	// noise kinds (analyzer, near-empty) do not hide them again.
	AutomatedOverridesNoise bool
}

// HiddenBy returns the kinds that hide s in a list with options o; none
// means s is shown.
func HiddenBy(s Session, o VisibleOpts) Kind {
	k := Kinds(s)
	hide := o.Hide &^ o.Reveal
	if o.AutomatedOverridesNoise && k&KindAutomated != 0 && o.Reveal&KindAutomated != 0 {
		hide &^= noise
	}
	return k & hide
}

// Visible reports whether a list with options o shows s.
func Visible(s Session, o VisibleOpts) bool { return HiddenBy(s, o) == 0 }

// Reveal names a switch a client offers and the kinds it reveals.
type Reveal struct {
	Name  string
	Kinds Kind
}

// HiddenUntil names the reveals that must all be on for s to show, for a
// client that applies the rule itself from the server's answer; empty
// means s is shown.
func HiddenUntil(s Session, o VisibleOpts, reveals []Reveal) []string {
	hidden := HiddenBy(s, o)
	// Revealing automated will lift noise too, so only that reveal is needed.
	if o.AutomatedOverridesNoise && hidden&KindAutomated != 0 {
		hidden &^= noise
	}
	out := []string{}
	for _, r := range reveals {
		if hidden&r.Kinds != 0 {
			out = append(out, r.Name)
		}
	}
	return out
}

// TUIOpts: the sessions view. H reveals subagent and automated sessions,
// automated ones whole; a search reveals everything.
func TUIOpts(h, searching bool) VisibleOpts {
	o := VisibleOpts{Hide: KindSubagent | KindAutomated | KindAnalyzer | KindNearEmpty | KindUndated, AutomatedOverridesNoise: true}
	if h {
		o.Reveal |= KindSubagent | KindAutomated
	}
	if searching {
		o.Reveal = o.Hide
	}
	return o
}

// DashboardOpts: /api/history. The React app reveals hidden rows with
// DashboardReveals; Automated shows automated sessions whole, like TUI H.
func DashboardOpts() VisibleOpts {
	return VisibleOpts{Hide: KindSubagent | KindAutomated | KindAnalyzer | KindNearEmpty, AutomatedOverridesNoise: true}
}

// DashboardReveals are the dashboard's switches: the Subagents, Automated
// and Analyzers toggles, and any search (which shows near-empty sessions).
var DashboardReveals = []Reveal{
	{Name: "subagents", Kinds: KindSubagent},
	{Name: "automated", Kinds: KindAutomated},
	{Name: "analyzers", Kinds: KindAnalyzer},
	{Name: "search", Kinds: KindNearEmpty},
}

// RemoteOpts: the remote API's list, projects and search scope. Subagents
// are never listed; automated sessions are when asked for, or when aimux
// created them (a session started from the phone stays listed).
func RemoteOpts(includeAutomated, created bool) VisibleOpts {
	o := VisibleOpts{Hide: KindSubagent | KindAutomated}
	if includeAutomated || created {
		o.Reveal = KindAutomated
	}
	return o
}

// CLIOpts: `aimux sessions` (list, table and fallback picker). Subagents
// and near-empty or undated sessions are never listed; automated ones only
// with --include-automated (exports pass true to stay complete).
func CLIOpts(includeAutomated bool) VisibleOpts {
	o := VisibleOpts{Hide: KindSubagent | KindAutomated | KindNearEmpty | KindUndated}
	if includeAutomated {
		o.Reveal = KindAutomated
	}
	return o
}

// MCPOpts: the MCP server's list_sessions (with a catalog). Subagents are
// never listed; automated sessions and aimux's own analyzer sessions only
// with include_automated. A session aimux created (the catalog's exception)
// reveals automated, and so is not hidden as noise either.
func MCPOpts(includeAutomated bool) VisibleOpts {
	o := VisibleOpts{Hide: KindSubagent | KindAutomated | KindAnalyzer, AutomatedOverridesNoise: true}
	if includeAutomated {
		o.Reveal = KindAutomated | KindAnalyzer
	}
	return o
}
