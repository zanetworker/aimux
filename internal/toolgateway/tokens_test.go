package toolgateway

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/zanetworker/aimux/internal/toolreg"
)

func TestTokens_IssueLookupRevoke(t *testing.T) {
	var tk Tokens
	allow := map[string][]string{"jira": {"get_issue_details"}, "wiki": {"wiki_search"}}
	tok, err := tk.Issue("sess-1", allow, toolreg.Snapshot{})
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
	other, err := tk.Issue("sess-2", nil, toolreg.Snapshot{})
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
	tok, _ := tk.Issue("sess-1", map[string][]string{"jira": {"a"}}, toolreg.Snapshot{})
	keep, _ := tk.Issue("sess-2", map[string][]string{"jira": {"a"}}, toolreg.Snapshot{})
	tk.Revoke("sess-1")
	if _, _, ok := tk.Lookup(tok); ok {
		t.Fatal("revoked token still accepted")
	}
	if _, _, ok := tk.Lookup(keep); !ok {
		t.Fatal("revoking one session removed another's token")
	}
	tk.Revoke("never-issued") // no panic
	// Issuing again for a session replaces its token.
	first, _ := tk.Issue("sess-2", nil, toolreg.Snapshot{})
	if _, _, ok := tk.Lookup(keep); ok || first == keep {
		t.Fatal("re-issue must retire the session's old token")
	}
	if _, _, ok := tk.Lookup(first); !ok {
		t.Fatal("re-issued token not accepted")
	}
}

func TestTokens_IssueRejectsEmptySession(t *testing.T) {
	var tk Tokens
	if _, err := tk.Issue("", nil, toolreg.Snapshot{}); err == nil {
		t.Fatal("a token needs a session")
	}
}

// memStore is a toolreg.Store over in-memory inputs; edit then reload.
type memStore struct {
	*toolreg.Store
	reg  toolreg.Registry
	pins toolreg.PinTable
}

func newMemStore(reg toolreg.Registry, pins toolreg.PinTable) *memStore {
	m := &memStore{reg: reg, pins: pins}
	m.Store = &toolreg.Store{LoadRegistry: func() (toolreg.Registry, error) { return m.reg, nil },
		LoadPins: func() (toolreg.PinTable, error) { return m.pins, nil }}
	return m
}

func (m *memStore) reload(t *testing.T) toolreg.Snapshot {
	t.Helper()
	s, changed, err := m.Reload()
	if err != nil || !changed {
		t.Fatalf("reload: changed=%v err=%v", changed, err)
	}
	return s
}

func staleRegistry() toolreg.Registry {
	return toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"jira": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"}, "slack": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:7381/mcp/slack"}},
		Sets: map[string]toolreg.Set{"jira:readonly": {Source: "jira", Tools: []string{"keep", "edit", "drop"}},
			"slack:readonly": {Source: "slack", Tools: []string{"search"}}}}
}

func stalePins() toolreg.PinTable {
	return toolreg.PinTable{"jira": {"keep": {Hash: "k"}, "edit": {Hash: "e1"}, "drop": {Hash: "d"}}, "slack": {"search": {Hash: "s"}}}
}

// A token issued under an older snapshot loses only the tools whose pin
// changed or went since; an unrelated edit leaves it whole, and so does a
// token issued under the current snapshot. A newer token against an older
// gateway loses the changed tools as well.
func TestTokens_StaleSnapshotDropsOnlyChangedTools(t *testing.T) {
	m := newMemStore(staleRegistry(), stalePins())
	old := m.Current()
	var tk Tokens
	var current toolreg.Snapshot
	tk.Current = func() toolreg.Snapshot { return current }
	allow := map[string][]string{"jira": {"drop", "edit", "keep"}, "slack": {"search"}}
	tok, err := tk.Issue("s", allow, old)
	if err != nil {
		t.Fatal(err)
	}

	// Unrelated edit: a new source and set nobody's token names.
	m.reg = staleRegistry()
	m.reg.Sources["google"] = toolreg.Source{Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8000/mcp"}
	m.reg.Sets["google:readonly"] = toolreg.Set{Source: "google", Tools: []string{"x"}}
	m.pins["google"] = map[string]toolreg.Pin{"x": {Hash: "x"}}
	current = m.reload(t)
	if _, got, _ := tk.Lookup(tok); !reflect.DeepEqual(got, allow) {
		t.Fatalf("unrelated edit narrowed the token: %v", got)
	}

	// edit re-pinned, drop un-pinned: those two go, the rest stay.
	m.pins = stalePins()
	m.pins["jira"]["edit"] = toolreg.Pin{Hash: "e2"}
	delete(m.pins["jira"], "drop")
	current = m.reload(t)
	want := map[string][]string{"jira": {"keep"}, "slack": {"search"}}
	if _, got, _ := tk.Lookup(tok); !reflect.DeepEqual(got, want) {
		t.Fatalf("after re-pin: %v, want %v", got, want)
	}

	// A token issued under the current snapshot keeps everything it names.
	fresh, _ := tk.Issue("s2", map[string][]string{"jira": {"edit", "keep"}}, current)
	if _, got, _ := tk.Lookup(fresh); !reflect.DeepEqual(got, map[string][]string{"jira": {"edit", "keep"}}) {
		t.Fatalf("current token narrowed: %v", got)
	}
	// Gateway still on the old snapshot, token newer: the old servers serve
	// edit under its old pin, so the newer token loses it too (fail closed).
	current = old
	if _, got, _ := tk.Lookup(fresh); !reflect.DeepEqual(got, map[string][]string{"jira": {"keep"}}) {
		t.Fatalf("newer token against the older gateway: %v, want only keep", got)
	}
}

// A re-stamped source is a changed pin for a token issued before it.
func TestTokens_StaleSnapshotSourceRestampIsAChange(t *testing.T) {
	m := newMemStore(staleRegistry(), stalePins())
	old := m.Current()
	var tk Tokens
	var current toolreg.Snapshot
	tk.Current = func() toolreg.Snapshot { return current }
	tok, _ := tk.Issue("s", map[string][]string{"jira": {"keep"}}, old)
	m.pins = stalePins()
	m.pins["jira"]["keep"] = toolreg.Pin{Hash: "k", Source: "http://127.0.0.1:8001/mcp"}
	current = m.reload(t)
	if _, got, _ := tk.Lookup(tok); len(got["jira"]) != 0 {
		t.Fatalf("restamped tool still allowed: %v", got)
	}
}
