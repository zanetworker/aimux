package autoroute

import (
	"context"
	"slices"
	"testing"
)

const bothDescription = "Needs the owner's private sources and the public web together: compare internal plans or notes with public releases, docs or news."

// both is offered only when the client may use research-with-work and
// also both halves it combines, workspace and web.
func TestRoute_BothNeedsWorkspaceAndWebAllowed(t *testing.T) {
	f := Facts{PageReadable: true}
	all := []string{"page", "research", "work-readonly", "research-with-work"}
	if got := allowedFor(f, all); !slices.Contains(got, "both") {
		t.Fatalf("allowed %v, want both with every profile allowed", got)
	}
	for _, scope := range [][]string{
		{"page", "research", "work-readonly"},          // no research-with-work
		{"page", "work-readonly", "research-with-work"}, // no web
		{"page", "research", "research-with-work"},      // no workspace
	} {
		if got := allowedFor(f, scope); slices.Contains(got, "both") {
			t.Errorf("scope %v: allowed %v includes both", scope, got)
		}
	}
	if ProfileOf("both") != "research-with-work" {
		t.Fatalf("ProfileOf(both) = %q", ProfileOf("both"))
	}
	// A decided both pick routes to research-with-work.
	b := &fakeBackend{a: answer("both", 0.8, map[string]float64{"both": 0.8, "web": 0.1, "workspace": 0.1})}
	r := Route(context.Background(), newDecider(b), "how does our egress plan compare with the latest release", f, all)
	if !r.Decided || r.Mode != "both" || r.Profile != "research-with-work" {
		t.Fatalf("Route = %+v", r)
	}
}

// The model is told what both means, and @both is an override like the others.
func TestRoute_BothDescriptionInPrompt(t *testing.T) {
	if ModeQuestion.Options["both"] != bothDescription {
		t.Fatalf("both option = %q", ModeQuestion.Options["both"])
	}
	if m, rest, ok := ParseOverride("@both compare our plan"); !ok || m != "both" || rest != "compare our plan" {
		t.Fatalf("ParseOverride(@both) = %q %q %v", m, rest, ok)
	}
}
