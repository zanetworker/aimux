package history

import (
	"os"
	"path/filepath"
	"testing"
)

// Reread is one session's record as Discover would give it now: a /rename
// appended to the transcript and a .meta.json title change both show.
func TestReread_PicksUpRenameAndMeta(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-w")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-01-01T10:00:00Z","cwd":"/w","message":{"role":"user","content":"hello there"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := Discover(DiscoverOpts{}, root)
	if err != nil || len(all) != 1 {
		t.Fatalf("Discover: %v %d", err, len(all))
	}
	old := all[0]

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- the test's temp file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"custom-title","customTitle":"renamed"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	s, err := Reread(old)
	if err != nil || s.CustomTitle != "renamed" || s.ID != old.ID || s.Project != old.Project || s.Cwd != "/w" {
		t.Fatalf("after /rename: %+v %v", s, err)
	}
	if err := UpdateMeta(path, func(m *Meta) { m.Title, m.TitleBy = "Mine", "owner" }); err != nil {
		t.Fatal(err)
	}
	if s, err = Reread(old); err != nil || ShownTitle(s) != "Mine" || s.TitleBy != "owner" {
		t.Fatalf("after meta: %q %v", ShownTitle(s), err)
	}
}

func TestReread_MissingFile(t *testing.T) {
	if _, err := Reread(Session{ID: "x", FilePath: filepath.Join(t.TempDir(), "gone.jsonl")}); err == nil {
		t.Fatal("want an error for a missing file")
	}
	if _, err := Reread(Session{ID: "x"}); err == nil {
		t.Fatal("want an error for a record without a file")
	}
}
