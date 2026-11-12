package toolgateway

import (
	"reflect"
	"regexp"
	"testing"
)

func TestTokens_IssueLookupRevoke(t *testing.T) {
	var tk Tokens
	allow := map[string][]string{"jira": {"get_issue_details"}, "wiki": {"wiki_search"}}
	tok, err := tk.Issue("sess-1", allow)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
		t.Fatalf("token %q is not 32 random bytes in hex", tok)
	}
	sess, got, ok := tk.Lookup(tok)
	if !ok || sess != "sess-1" || !reflect.DeepEqual(got, allow) {
		t.Fatalf("Lookup = %q %v %v", sess, got, ok)
	}
	// The caller's map and the looked-up map are copies: editing one must not
	// widen the token.
	allow["slack"] = []string{"search_messages"}
	got["jira"][0] = "delete_issue"
	_, again, _ := tk.Lookup(tok)
	if _, widened := again["slack"]; widened || again["jira"][0] != "get_issue_details" {
		t.Fatalf("token rules changed behind its back: %v", again)
	}
	other, err := tk.Issue("sess-2", nil)
	if err != nil || other == tok {
		t.Fatalf("second token %q err %v: tokens must be unique", other, err)
	}
	if _, _, ok := tk.Lookup(""); ok {
		t.Fatal("empty token must never match")
	}
	if _, _, ok := tk.Lookup(tok[:63]); ok {
		t.Fatal("token prefix must not match")
	}
}

func TestTokens_RevokedTokenRefused(t *testing.T) {
	var tk Tokens
	tok, _ := tk.Issue("sess-1", map[string][]string{"jira": {"a"}})
	keep, _ := tk.Issue("sess-2", map[string][]string{"jira": {"a"}})
	tk.Revoke("sess-1")
	if _, _, ok := tk.Lookup(tok); ok {
		t.Fatal("revoked token still accepted")
	}
	if _, _, ok := tk.Lookup(keep); !ok {
		t.Fatal("revoking one session removed another's token")
	}
	tk.Revoke("never-issued") // no panic
	// Issuing again for a session replaces its token.
	first, _ := tk.Issue("sess-2", nil)
	if _, _, ok := tk.Lookup(keep); ok || first == keep {
		t.Fatal("re-issue must retire the session's old token")
	}
	if _, _, ok := tk.Lookup(first); !ok {
		t.Fatal("re-issued token not accepted")
	}
}

func TestTokens_IssueRejectsEmptySession(t *testing.T) {
	var tk Tokens
	if _, err := tk.Issue("", nil); err == nil {
		t.Fatal("a token needs a session")
	}
}
