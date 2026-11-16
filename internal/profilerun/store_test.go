package profilerun

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "sessions", "profiles.json")}
}

func TestStore_PutGetRoundTrip(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	m := Meta{Profile: "page", Digest: "d1", Client: "cli", URL: "https://a", Title: "A", Created: at}
	if err := s.Put("s1", m); err != nil {
		t.Fatal(err)
	}
	// A second Store on the same file sees it: the file is the truth.
	got, ok := (&Store{Path: s.Path}).Get("s1")
	if !ok || got != m {
		t.Fatalf("Get = %+v %v, want %+v", got, ok, m)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("unknown session found")
	}
	if err := s.Put("", m); err == nil {
		t.Fatal("empty session id accepted")
	}
}

func TestStore_File0600(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file left world-readable by something else is replaced at 0600.
	if err := os.WriteFile(s.Path, []byte("{}"), 0o644); err != nil { //nolint:gosec // G306: the test needs a world-readable file to prove it is replaced at 0600
		t.Fatal(err)
	}
	if err := s.Put("s1", Meta{Profile: "page"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	// A corrupt file is an error on Put, never silently overwritten.
	if err := os.WriteFile(s.Path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("s2", Meta{}); err == nil {
		t.Fatal("Put over a corrupt file succeeded")
	}
}

func TestStore_FindPageLatest(t *testing.T) {
	s := newStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	put := func(id string, m Meta) {
		t.Helper()
		if err := s.Put(id, m); err != nil {
			t.Fatal(err)
		}
	}
	put("old", Meta{Profile: "page", Client: "cli", URL: "https://a", Created: t0})
	put("new", Meta{Profile: "page", Client: "cli", URL: "https://a", Created: t0.Add(time.Hour)})
	put("mid", Meta{Profile: "page", Client: "cli", URL: "https://a", Created: t0.Add(time.Minute)})
	put("other-client", Meta{Profile: "page", Client: "web", URL: "https://a", Created: t0.Add(2 * time.Hour)})
	put("other-profile", Meta{Profile: "work-readonly", Client: "cli", URL: "https://a", Created: t0.Add(2 * time.Hour)})
	put("other-url", Meta{Profile: "page", Client: "cli", URL: "https://b", Created: t0.Add(2 * time.Hour)})
	if got, ok := s.FindPage("cli", "", "https://a", "page", false, false); !ok || got != "new" {
		t.Fatalf("FindPage = %q %v, want new", got, ok)
	}
	if got, ok := s.FindPage("cli", "", "https://none", "page", false, false); ok {
		t.Fatalf("FindPage on an unseen url = %q", got)
	}
}

func TestStore_ListByClient(t *testing.T) {
	s := newStore(t)
	for id, c := range map[string]string{"a": "cli", "b": "cli", "c": "web"} {
		if err := s.Put(id, Meta{Profile: "page", Client: c}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.List("cli")
	if len(got) != 2 || got["a"].Client != "cli" || got["b"].Client != "cli" {
		t.Fatalf("List(cli) = %v", got)
	}
	if got := s.List("nobody"); got == nil || len(got) != 0 {
		t.Fatalf("List(nobody) = %#v, want an empty map", got)
	}
}

// A hidden session (Wingman's history delete) is not continued: the next
// question on that page starts a new conversation.
func TestStore_FindPageSkipsHidden(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_ = s.Put("old", Meta{Profile: "page", Client: "wingman", URL: "https://a", Created: at})
	_ = s.Put("new", Meta{Profile: "page", Client: "wingman", URL: "https://a", Created: at.Add(time.Minute), Hidden: true})
	if id, ok := s.FindPage("wingman", "", "https://a", "page", false, false); !ok || id != "old" {
		t.Fatalf("FindPage = %q %v, want the visible old one", id, ok)
	}
	_ = s.Put("old", Meta{Profile: "page", Client: "wingman", URL: "https://a", Created: at, Hidden: true})
	if id, ok := s.FindPage("wingman", "", "https://a", "page", false, false); ok {
		t.Fatalf("FindPage = %q, want none: every session on the page is hidden", id)
	}
}

// Each browser profile (instance) continues only its own sessions: never
// another instance's, and an instance never continues a legacy session
// (one recorded without an instance), which only the legacy key continues.
func TestStore_FindPageKeysOnInstance(t *testing.T) {
	s := newStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_ = s.Put("legacy", Meta{Profile: "page", Client: "wingman", URL: "https://a", Created: t0.Add(3 * time.Hour)})
	_ = s.Put("chrome", Meta{Profile: "page", Client: "wingman", Instance: "i-chrome", URL: "https://a", Created: t0})
	_ = s.Put("beta", Meta{Profile: "page", Client: "wingman", Instance: "i-beta", URL: "https://a", Created: t0.Add(time.Hour)})
	for _, c := range []struct{ instance, want string }{{"i-chrome", "chrome"}, {"i-beta", "beta"}, {"", "legacy"}} {
		if got, ok := s.FindPage("wingman", c.instance, "https://a", "page", false, false); !ok || got != c.want {
			t.Fatalf("FindPage(instance %q) = %q %v, want %s", c.instance, got, ok, c.want)
		}
	}
	if got, ok := s.FindPage("wingman", "i-new", "https://a", "page", false, false); ok {
		t.Fatalf("FindPage(new instance) = %q, want none: it starts its own session", got)
	}
}

// A profiles.json written before instances existed reads as legacy sessions.
func TestStore_OldFileWithoutInstance(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"s1":{"Profile":"page","Digest":"d","Client":"wingman","URL":"https://a","Title":"A","Created":"2026-11-11T09:00:00Z"}}`
	if err := os.WriteFile(s.Path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.FindPage("wingman", "", "https://a", "page", false, false); !ok || got != "s1" {
		t.Fatalf("FindPage(legacy) = %q %v, want s1", got, ok)
	}
	if m, _ := s.Get("s1"); m.Instance != "" {
		t.Fatalf("Instance = %q, want empty", m.Instance)
	}
}

// A session started with page text and one started without are separate
// conversations: FindPage continues only the one whose WithPage matches, so
// text bound on a first turn never reaches a question asked without it.
// A file from before WithPage reads as without.
func TestStore_FindPageKeysOnWithPage(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_ = s.Put("with", Meta{Profile: "research", Client: "wingman", URL: "https://a", Created: at, WithPage: true})
	if id, ok := s.FindPage("wingman", "", "https://a", "research", false, false); ok {
		t.Fatalf("FindPage without page = %q, want none", id)
	}
	if id, ok := s.FindPage("wingman", "", "https://a", "research", true, false); !ok || id != "with" {
		t.Fatalf("FindPage with page = %q %v, want with", id, ok)
	}
	if err := os.WriteFile(s.Path, []byte(`{"legacy":{"Profile":"page","Client":"wingman","URL":"https://b","Created":"2026-11-11T09:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, ok := s.FindPage("wingman", "", "https://b", "page", false, false); !ok || id != "legacy" {
		t.Fatalf("legacy without page = %q %v, want legacy", id, ok)
	}
	if id, ok := s.FindPage("wingman", "", "https://b", "page", true, false); ok {
		t.Fatalf("legacy with page = %q, want none", id)
	}
}
