package profilerun

import (
	"context"
	"strings"
	"testing"
	"time"
)

// With OriginOnly the first prompt carries scheme://host[:port] only: a
// private page's path, query and fragment (ids, a mail's thread) stay out of
// the sandbox. Without it the full URL goes, as before.
func TestSandboxPrompt_OriginOnly(t *testing.T) {
	const full = "https://mail.google.com:8443/mail/u/0/?q=x#inbox/FMfcgzA"
	p := sandboxPrompt(&AskRequest{URL: full, OriginOnly: true}, "q")
	if !strings.Contains(p, "URL: https://mail.google.com:8443\n") {
		t.Fatalf("origin-only prompt lacks the origin line: %q", p)
	}
	for _, s := range []string{"/mail/u/0", "FMfcgzA", "q=x"} {
		if strings.Contains(p, s) {
			t.Fatalf("origin-only prompt holds %q: %q", s, p)
		}
	}
	if p := sandboxPrompt(&AskRequest{URL: full}, "q"); !strings.Contains(p, "URL: "+full+"\n") {
		t.Fatalf("full prompt lost the URL: %q", p)
	}
	if p := sandboxPrompt(&AskRequest{URL: "file:///Users/me/offer.pdf", OriginOnly: true}, "q"); strings.Contains(p, "offer") {
		t.Fatalf("origin-only file URL kept its path: %q", p)
	}
	if p := sandboxPrompt(&AskRequest{URL: "%%bad", OriginOnly: true}, "q"); strings.Contains(p, "bad") {
		t.Fatalf("an unparsable origin-only URL went into the prompt: %q", p)
	}
}

// New and Adopt carry Meta.OriginOnly to the first prompt.
func TestSandboxEngine_OriginOnlyReachesFirstPrompt(t *testing.T) {
	const full = "https://docs.google.com/document/d/SECRET/edit"
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly", URL: full, OriginOnly: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	page := ""
	e.Adopt(adoptedID, Meta{Profile: "work-readonly", URL: full, OriginOnly: true}, &page)
	if _, _, err := sendCollect(t, e, adoptedID, "q"); err != nil {
		t.Fatal(err)
	}
	for i, x := range b.execs {
		if strings.Contains(x.prompt, "SECRET") || !strings.Contains(x.prompt, "URL: https://docs.google.com\n") {
			t.Fatalf("exec %d prompt %q, want the origin only", i, x.prompt)
		}
	}
}

// FindPage keys on OriginOnly next to WithPage: a session whose first prompt
// held the full URL is not continued by an origin-only question, and back.
func TestStore_FindPageKeysOnOriginOnly(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	_ = s.Put("full", Meta{Profile: "research", Client: "wingman", URL: "https://a", Created: at})
	_ = s.Put("origin", Meta{Profile: "research", Client: "wingman", URL: "https://a", Created: at.Add(-time.Hour), OriginOnly: true})
	if id, ok := s.FindPage("wingman", "", "https://a", "research", false, false); !ok || id != "full" {
		t.Fatalf("full = %q %v, want full", id, ok)
	}
	if id, ok := s.FindPage("wingman", "", "https://a", "research", false, true); !ok || id != "origin" {
		t.Fatalf("origin-only = %q %v, want origin", id, ok)
	}
}
