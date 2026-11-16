package profile

import (
	"reflect"
	"strings"
	"testing"
)

func TestToolGrant_AccessorsSorted(t *testing.T) {
	g := NewToolGrant(map[string][]string{"wiki": {"search", "read_page"}, "jira": {"search"}})
	if got := g.Servers(); !reflect.DeepEqual(got, []string{"jira", "wiki"}) {
		t.Errorf("Servers = %v", got)
	}
	if got := g.Tools("wiki"); !reflect.DeepEqual(got, []string{"read_page", "search"}) {
		t.Errorf("Tools(wiki) = %v", got)
	}
	if got := g.Tools("slack"); got != nil {
		t.Errorf("Tools(unknown) = %v, want nil", got)
	}
	if g.Empty() {
		t.Error("Empty = true for a grant with servers")
	}
	if !strings.HasPrefix(g.Digest(), "sha256:") || len(g.Digest()) != len("sha256:")+64 {
		t.Errorf("Digest = %q", g.Digest())
	}
}

func TestToolGrant_ZeroAndNilAreEmpty(t *testing.T) {
	var zero ToolGrant
	for _, g := range []ToolGrant{zero, NewToolGrant(nil), NewToolGrant(map[string][]string{})} {
		if !g.Empty() || len(g.Servers()) != 0 || len(g.Scope()) != 0 {
			t.Errorf("grant %+v is not empty", g)
		}
		if g.Digest() != zero.Digest() {
			t.Errorf("empty grants digest differently: %q vs %q", g.Digest(), zero.Digest())
		}
	}
}

// The digest depends on the granted set only, not on input order.
func TestToolGrant_DigestIsCanonical(t *testing.T) {
	a := NewToolGrant(map[string][]string{"wiki": {"b", "a"}, "jira": {"x"}})
	b := NewToolGrant(map[string][]string{"jira": {"x"}, "wiki": {"a", "b"}})
	if a.Digest() != b.Digest() {
		t.Errorf("same grant, different digests: %q %q", a.Digest(), b.Digest())
	}
	c := NewToolGrant(map[string][]string{"jira": {"x"}, "wiki": {"a"}})
	d := NewToolGrant(map[string][]string{"jira": {"x", "wiki"}, "a": nil})
	if a.Digest() == c.Digest() || c.Digest() == d.Digest() {
		t.Error("different grants share a digest")
	}
}

// Nothing a caller holds, the map it built the grant from or anything an
// accessor returned, can change the grant or what it renders later.
func TestToolGrant_Immutable(t *testing.T) {
	in := map[string][]string{"wiki": {"read_page", "search"}, "jira": {"search"}}
	g := NewToolGrant(in)
	digest, scope := g.Digest(), g.Scope()

	in["slack"] = []string{"history"}
	in["wiki"][0] = "delete_page"
	s := g.Servers()
	s[0] = "slack"
	tools := g.Tools("wiki")
	tools[0] = "delete_page"
	sc := g.Scope()
	sc["slack"] = []string{"history"}
	sc["jira"][0] = "create_issue"
	delete(sc, "wiki")

	if g.Digest() != digest {
		t.Errorf("digest moved: %q -> %q", digest, g.Digest())
	}
	if !reflect.DeepEqual(g.Scope(), scope) {
		t.Errorf("grant changed behind its back: %v, want %v", g.Scope(), scope)
	}
	if !reflect.DeepEqual(g.Servers(), []string{"jira", "wiki"}) || !reflect.DeepEqual(g.Tools("wiki"), []string{"read_page", "search"}) {
		t.Errorf("accessors changed: %v %v", g.Servers(), g.Tools("wiki"))
	}
}
