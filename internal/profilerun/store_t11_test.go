package profilerun

import (
	"os"
	"testing"
	"time"
)

// All returns every client's sessions; an unreadable file is an error, not
// an empty map (a restart must not forget sessions silently).
func TestStore_All(t *testing.T) {
	s := newStore(t)
	if all, err := s.All(); err != nil || len(all) != 0 {
		t.Fatalf("empty store: %v %v", all, err)
	}
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_ = s.Put("a", Meta{Profile: "page", Client: "wingman", Created: at})
	_ = s.Put("b", Meta{Profile: "work-readonly", Client: "device", Created: at})
	all, err := s.All()
	if err != nil || len(all) != 2 || all["a"].Client != "wingman" || all["b"].Profile != "work-readonly" {
		t.Fatalf("All = %v %v", all, err)
	}
	if err := os.WriteFile(s.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.All(); err == nil {
		t.Fatal("a corrupt store read as fine")
	}
}

// MarkStarted records that a session's first turn started, keeping the rest
// of its Meta; an unknown session is an error.
func TestStore_MarkStarted(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_ = s.Put("a", Meta{Profile: "work-readonly", Client: "wingman", URL: "https://a", Created: at})
	if m, _ := s.Get("a"); m.Started {
		t.Fatal("started before MarkStarted")
	}
	if err := s.MarkStarted("a"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Get("a"); !m.Started || m.URL != "https://a" || m.Client != "wingman" || !m.Created.Equal(at) {
		t.Fatalf("after MarkStarted: %+v", m)
	}
	if err := s.MarkStarted("nope"); err == nil {
		t.Fatal("an unknown session was marked")
	}
}
