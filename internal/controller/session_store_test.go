package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionStore_PutGet(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(dir)
	s.Put("ax-cl-1234", "uuid-aaaa-bbbb")
	if got := s.Get("ax-cl-1234"); got != "uuid-aaaa-bbbb" {
		t.Errorf("Get = %q, want %q", got, "uuid-aaaa-bbbb")
	}
}

func TestSessionStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	s1 := NewSessionStore(dir)
	s1.Put("ax-cl-5678", "uuid-cccc-dddd")

	s2 := NewSessionStore(dir)
	if got := s2.Get("ax-cl-5678"); got != "uuid-cccc-dddd" {
		t.Errorf("reloaded Get = %q, want %q", got, "uuid-cccc-dddd")
	}
}

func TestSessionStore_MissingKey(t *testing.T) {
	s := NewSessionStore(t.TempDir())
	if got := s.Get("nonexistent"); got != "" {
		t.Errorf("missing key = %q, want empty", got)
	}
}

func TestSessionStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(dir)
	s.Put("test", "val")
	info, err := os.Stat(filepath.Join(dir, "remote-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
}
