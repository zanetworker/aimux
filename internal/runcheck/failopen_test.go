package runcheck

import (
	"strings"
	"testing"
)

// rejectedWith fails unless Check refuses q alone with reason.
func rejectedWith(t *testing.T, q string, p Private, reason string) {
	t.Helper()
	acc, rej := Check([]string{q}, p)
	if len(acc) != 0 || len(rej) != 1 || rej[0].Reason != reason {
		t.Errorf("%q: accepted %v rejected %v, want rejected %s", q, acc, rej, reason)
	}
}

// A query whose percent-decoding fails is refused: a bad escape elsewhere
// in it must not hide an encoded address or key from the decoded check.
func TestCheck_UndecodableQueryRejected(t *testing.T) {
	for _, q := range []string{"jdoe%40redhat%2Ecom %zz", "RHAIENG%2D1234 %zz", "release notes 100%"} {
		rejectedWith(t, q, Private{}, ReasonEncoding)
	}
}

// The overlap check also runs on the decoded form: a private span joined
// with %20 is still a copied span.
func TestCheck_EncodedOverlapRejected(t *testing.T) {
	span := "moves the supervisor into its own pod and fences it"
	rejectedWith(t, strings.ReplaceAll(span, " ", "%20"), Private{ToolOutputs: []string{jiraOutput}}, ReasonOverlap)
}

// Issue keys with any Unicode dash, or a space after an upper-case key;
// in lower case when the number has 4 or more digits or the key is one the
// planner saw. Public identifiers in lower case (gpt-4, utf-8, sha-256)
// still pass.
func TestCheck_IssueKeyVariants(t *testing.T) {
	for _, q := range []string{"rhaieng-1234", "RHAIENG 1234", "RHAIENG\u20101234", "RHAIENG\u20131234 status", "rhaieng\u22121234", "OSD-12"} {
		rejectedWith(t, q, Private{}, ReasonIssueKey)
	}
	rejectedWith(t, "status of osd-12", Private{ToolOutputs: []string{"OSD-12 is blocked"}}, ReasonIssueKey)
	for _, q := range []string{"gpt-4 release notes", "utf-8 decoding", "sha-256 collisions", "llama-3 benchmarks"} {
		if acc, rej := Check([]string{q}, Private{}); len(acc) != 1 {
			t.Errorf("%q: accepted %v rejected %v, want accepted", q, acc, rej)
		}
	}
}

// Dotted names with any last label are hosts, and look-alike dots are
// folded first, so internal names and IPs written with them are refused.
func TestCheck_HostsAndIPsAfterFolding(t *testing.T) {
	for _, q := range []string{"gitlab.cee.nl", "jenkins.prod.redhat outage", "intranet。corp", "intranet｡corp", "intranet﹒corp"} {
		rejectedWith(t, q, Private{}, ReasonHostname)
	}
	for _, q := range []string{"10。0。0。1", "10｡0｡0｡1 latency"} {
		rejectedWith(t, q, Private{}, ReasonIP)
	}
	// Version numbers are not hosts.
	if acc, rej := Check([]string{"openshell v0.15.2 release notes"}, Private{}); len(acc) != 1 {
		t.Errorf("version number: accepted %v rejected %v", acc, rej)
	}
}

// AWS access key ids and "password is X" phrases are credentials.
func TestCheck_MoreCredentialShapes(t *testing.T) {
	rejectedWith(t, "AKIAIOSFODNN7EXAMPLE leak", Private{}, ReasonToken)
	rejectedWith(t, "the password is hunter2", Private{}, ReasonCredential)
	rejectedWith(t, "my token is abc123", Private{}, ReasonCredential)
}

// Earlier questions on the page are private too.
func TestCheck_PriorQuestionsArePrivate(t *testing.T) {
	prior := "how does the customer acme rollout plan for the egress proxy look"
	rejectedWith(t, "customer acme rollout plan for the egress proxy look", Private{Prior: []string{prior}}, ReasonOverlap)
}
