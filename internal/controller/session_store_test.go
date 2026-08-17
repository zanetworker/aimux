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

func TestSessionStore_PutMetaGetMeta(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(dir)
	meta := LaunchMeta{SessionID: "uuid-1234", Provider: "claude", Dir: "/home/user/project"}
	s.PutMeta("ax-cl-abcd", meta)

	got := s.GetMeta("ax-cl-abcd")
	if got.SessionID != meta.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, meta.SessionID)
	}
	if got.Provider != meta.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, meta.Provider)
	}
	if got.Dir != meta.Dir {
		t.Errorf("Dir = %q, want %q", got.Dir, meta.Dir)
	}
	// Get() backward-compat accessor must also return the session ID.
	if got := s.Get("ax-cl-abcd"); got != meta.SessionID {
		t.Errorf("Get = %q, want %q", got, meta.SessionID)
	}
}

func TestSessionStore_MetaPersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := NewSessionStore(dir)
	s1.PutMeta("ax-cl-efgh", LaunchMeta{SessionID: "uuid-5678", Provider: "codex", Dir: "/tmp/work"})

	s2 := NewSessionStore(dir)
	got := s2.GetMeta("ax-cl-efgh")
	if got.SessionID != "uuid-5678" || got.Provider != "codex" || got.Dir != "/tmp/work" {
		t.Errorf("reloaded meta = %+v", got)
	}
}

func TestSessionStore_LegacyMigration(t *testing.T) {
	dir := t.TempDir()
	// Write old-format JSON (map[string]string) directly.
	path := filepath.Join(dir, "remote-sessions.json")
	if err := os.WriteFile(path, []byte(`{"ax-cl-old":"legacy-uuid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(dir)
	if got := s.Get("ax-cl-old"); got != "legacy-uuid" {
		t.Errorf("migrated Get = %q, want %q", got, "legacy-uuid")
	}
}
