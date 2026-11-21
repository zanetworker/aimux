package runcheck

import (
	"strings"
	"testing"
)

// jiraOutput is what a planner's Jira lookup returned: private text a
// query must never repeat.
const jiraOutput = "The egress plan moves the supervisor into its own pod and fences it with a NetworkPolicy before the November customer rollout."

func TestCheck_Table(t *testing.T) {
	p := Private{ToolOutputs: []string{jiraOutput}}
	cases := []struct {
		query, reason string // reason "" means accepted
	}{
		{"openshell egress proxy release notes november 2026", ""},
		{"RHAISTRAT-2955 status", ReasonIssueKey},
		{"ask jane@redhat.com", ReasonEmail},
		{"api.internal.example.corp outage", ReasonHostname},
		{"sk-abc123xyz", ReasonToken},
		{"moves the supervisor into its own pod and fences it with", ReasonOverlap},
		{"openshell \u202erelease notes", ReasonControlChars},
		{"https://github.com/cli/cli", ReasonURL},
		{"10.0.0.12 latency", ReasonIP},
		{"password=hunter2 login", ReasonCredential},
		{"6f1e2c3d-4b5a-4c6d-8e9f-0a1b2c3d4e5f", ReasonUUID},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefGHIJ", ReasonToken},
		{"Q29weSBvZiBzZWNyZXQ0MmJ5dGVz9xz", ReasonHighEntropy},
		{strings.Repeat("a", 201), ReasonTooLong},
	}
	for _, c := range cases {
		acc, rej := Check([]string{c.query}, p)
		switch {
		case c.reason == "" && (len(acc) != 1 || len(rej) != 0):
			t.Errorf("%q: accepted %v rejected %v, want accepted", c.query, acc, rej)
		case c.reason != "" && (len(acc) != 0 || len(rej) != 1 || rej[0].Reason != c.reason || rej[0].Index != 0):
			t.Errorf("%q: accepted %v rejected %v, want rejected %s", c.query, acc, rej, c.reason)
		}
	}
}

// At most 5 queries: the 6th is rejected, the first 5 pass.
func TestCheck_TooMany(t *testing.T) {
	qs := []string{"one topic", "two topic", "three topic", "four topic", "five topic", "six topic"}
	acc, rej := Check(qs, Private{})
	if len(acc) != 5 || len(rej) != 1 || rej[0].Index != 5 || rej[0].Reason != ReasonTooMany {
		t.Fatalf("accepted %v rejected %v", acc, rej)
	}
}

// The byte budget (600 in total) rejects the query that would cross it.
func TestCheck_Budget(t *testing.T) {
	q := strings.Repeat("word ", 39) + "end" // 198 bytes
	acc, rej := Check([]string{q, q, q, q}, Private{})
	if len(acc) != 3 || len(rej) != 1 || rej[0].Index != 3 || rej[0].Reason != ReasonBudget {
		t.Fatalf("accepted %d rejected %v", len(acc), rej)
	}
}

// A reason never carries the rejected text, so a rejection can be shown
// and logged without leaking what the planner tried to send.
func TestCheck_ReasonsNeverContainText(t *testing.T) {
	key := "RHAISTRAT-2955"
	_, rej := Check([]string{key + " status"}, Private{})
	for _, r := range rej {
		if strings.Contains(r.Reason, "2955") || strings.Contains(r.String(), "2955") {
			t.Fatalf("rejection %+v carries the text", r)
		}
	}
}

// A percent-encoded email is decoded once and still rejected.
func TestCheck_PercentEncodedEmailRejected(t *testing.T) {
	_, rej := Check([]string{"ask jane%40redhat.com today"}, Private{})
	if len(rej) != 1 || rej[0].Reason != ReasonEmail {
		t.Fatalf("rejected %v, want email", rej)
	}
}

// Overlap is checked against every private input, after normalization
// (case, width and spacing do not hide a copied span).
func TestCheck_OverlapAcrossInputs(t *testing.T) {
	notes := "Customer Acme plans to move their agents behind the egress proxy next quarter"
	for _, p := range []Private{{Notes: notes}, {PageText: notes}, {ToolInputs: []string{notes}}, {Question: notes}} {
		_, rej := Check([]string{"CUSTOMER  ACME plans to move their agents behind the egress"}, p)
		if len(rej) != 1 || rej[0].Reason != ReasonOverlap {
			t.Errorf("private %+v: rejected %v, want overlap", p, rej)
		}
	}
	// Seven shared words are not a copied span.
	acc, _ := Check([]string{"plans to move their agents behind the cloud gateway"}, Private{Notes: notes})
	if len(acc) != 1 {
		t.Fatal("a 7-word shared span was rejected")
	}
}

// Empty and whitespace-only queries are dropped, not sent.
func TestCheck_EmptyQueryRejected(t *testing.T) {
	acc, rej := Check([]string{"   "}, Private{})
	if len(acc) != 0 || len(rej) != 1 || rej[0].Reason != ReasonEmpty {
		t.Fatalf("accepted %v rejected %v", acc, rej)
	}
}

// Accepted queries come back normalized (NFKC, single spaces, trimmed).
func TestCheck_AcceptedAreNormalized(t *testing.T) {
	acc, _ := Check([]string{"  ｏｐｅｎｓｈｅｌｌ   release\tnotes "}, Private{})
	if len(acc) != 1 || acc[0] != "openshell release notes" {
		t.Fatalf("accepted %q", acc)
	}
}
