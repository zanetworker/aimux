package push

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestForApproval_NamesTheSessionAndTool(t *testing.T) {
	m := ForApproval("Fix the cache", "Bash: go test ./...")
	if m.Title != "Approval needed" || m.Body != "Fix the cache: Bash: go test ./..." {
		t.Fatalf("%+v", m)
	}
	if m := ForApproval("", "Edit cache.go"); m.Body != "Edit cache.go" {
		t.Fatalf("no session title: %q", m.Body)
	}
	if m := ForApproval("Fix the cache", ""); m.Body != "Fix the cache: a tool call" {
		t.Fatalf("no tool title: %q", m.Body)
	}
}

func TestForApproval_ShortensLongTitlesOnRuneBoundaries(t *testing.T) {
	m := ForApproval(strings.Repeat("é", 300), strings.Repeat("ü", 500))
	if !utf8.ValidString(m.Body) {
		t.Fatal("cut inside a character")
	}
	if n := utf8.RuneCountInString(m.Body); n > 200 {
		t.Fatalf("body is %d characters; lock screens show about two lines", n)
	}
	if !strings.Contains(m.Body, "…") {
		t.Fatalf("a shortened body must say so: %q", m.Body)
	}
}
