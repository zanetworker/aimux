package search

import (
	"strings"
	"testing"
)

func TestMakeSnippet_HighlightsPrefixMatches(t *testing.T) {
	got := makeSnippet("the token exchange fails behind keycloak today", []string{"token", "exch"}, 14)
	if !strings.Contains(got, "[token] [exchange]") {
		t.Errorf("snippet %q should mark both terms", got)
	}
	if strings.Contains(got, "[the]") || strings.Contains(got, "[keycloak]") {
		t.Errorf("snippet %q marks words that do not match", got)
	}
}

func TestMakeSnippet_WindowsAroundFirstMatchInLongText(t *testing.T) {
	text := strings.Repeat("filler ", 200) + "the registrar reaches keycloak " + strings.Repeat("tail ", 200)
	got := makeSnippet(text, []string{"registrar"}, 14)
	if !strings.Contains(got, "[registrar]") {
		t.Fatalf("match missing from %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("a window inside long text should be marked with ellipses: %q", got)
	}
	if n := len(strings.Fields(strings.Trim(got, "…"))); n > 14 {
		t.Errorf("window has %d words, want at most 14", n)
	}
}

func TestMakeSnippet_NoMatchAndEmpty(t *testing.T) {
	if got := makeSnippet("nothing relevant here", []string{"zzz"}, 14); strings.Contains(got, "[") || got == "" {
		t.Errorf("no match: %q, want the opening words without marks", got)
	}
	if got := makeSnippet("", []string{"x"}, 14); got != "" {
		t.Errorf("empty text: %q", got)
	}
	// punctuation stays attached; case-insensitive match
	if got := makeSnippet("Use OpenShell, then retry.", []string{"openshell"}, 14); !strings.Contains(got, "[OpenShell],") {
		t.Errorf("punctuation/case: %q", got)
	}
}
