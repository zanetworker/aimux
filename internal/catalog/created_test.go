package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreatedStore_PersistsPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-created.json")
	s := &CreatedStore{Path: path}
	if err := s.Add("new-1", "/w/app", "start the cache work"); err != nil {
		t.Fatal(err)
	}
	again := &CreatedStore{Path: path} // as after a restart
	c, ok := again.Get("new-1")
	if !ok || c.Cwd != "/w/app" || c.Prompt != "start the cache work" {
		t.Fatalf("after reload: %+v %v", c, ok)
	}
	if _, ok := again.Get("other"); ok {
		t.Fatal("unknown id reported as created")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file mode %v (%v), want 0600: prompts are private", fi.Mode().Perm(), err)
	}
}

func TestCreatedStore_MissingFileIsEmpty(t *testing.T) {
	s := &CreatedStore{Path: filepath.Join(t.TempDir(), "none.json")}
	if _, ok := s.Get("x"); ok {
		t.Fatal("empty store has an entry")
	}
}

func TestCreatedStore_NullFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &CreatedStore{Path: path}
	if err := s.Add("x", "/w", "p"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("x"); !ok {
		t.Fatal("entry missing after Add")
	}
}

// Remote serve adds sessions through its own store while a long-lived reader
// (aimux mcp serve) keeps another one on the same file: the reader sees the
// additions, and a file that disappears empties it.
func TestCreatedStore_ReaderSeesAnotherProcessesAdditions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "created.json")
	reader := &CreatedStore{Path: path}
	if n := len(reader.All()); n != 0 {
		t.Fatalf("empty store listed %d", n)
	}
	writer := &CreatedStore{Path: path}
	if err := writer.Add("s1", "/w", "first"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reader.Get("s1"); !ok {
		t.Fatal("reader missed s1 added by another store")
	}
	if err := writer.Add("s2", "/w", "second"); err != nil {
		t.Fatal(err)
	}
	if n := len(reader.All()); n != 2 {
		t.Fatalf("reader lists %d, want 2", n)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if n := len(reader.All()); n != 0 {
		t.Fatalf("reader lists %d after the file was removed, want 0", n)
	}
}
