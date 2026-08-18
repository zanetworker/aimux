package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_SaveAndGet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	s := NewStore(path)

	p := Profile{Name: "work", Provider: "claude", Dir: "/tmp/proj", Model: "opus"}
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok := s.Get("work")
	if !ok {
		t.Fatal("Get: profile not found")
	}
	if got.Provider != "claude" {
		t.Errorf("Provider=%q, want %q", got.Provider, "claude")
	}
	if got.Model != "opus" {
		t.Errorf("Model=%q, want %q", got.Model, "opus")
	}
}

func TestStore_List(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "profiles.json"))
	_ = s.Save(Profile{Name: "b"})
	_ = s.Save(Profile{Name: "a"})

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List len=%d, want 2", len(list))
	}
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Errorf("List not sorted: %v", list)
	}
}

func TestStore_Delete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "profiles.json"))
	_ = s.Save(Profile{Name: "x"})

	if err := s.Delete("x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get("x"); ok {
		t.Error("profile should be deleted")
	}
}

func TestStore_DeleteNotFound(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "profiles.json"))
	if err := s.Delete("nope"); err == nil {
		t.Error("expected error for missing profile")
	}
}

func TestStore_LoadPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")

	s1 := NewStore(path)
	_ = s1.Save(Profile{Name: "test", Provider: "codex"})

	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := s2.Get("test")
	if !ok {
		t.Fatal("profile not found after reload")
	}
	if got.Provider != "codex" {
		t.Errorf("Provider=%q, want %q", got.Provider, "codex")
	}
}

func TestStore_LoadMissing(t *testing.T) {
	s := NewStore("/nonexistent/profiles.json")
	if err := s.Load(); err != nil {
		t.Errorf("Load should not error on missing file: %v", err)
	}
}

func TestStore_Names(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "profiles.json"))
	_ = s.Save(Profile{Name: "z"})
	_ = s.Save(Profile{Name: "a"})

	names := s.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "z" {
		t.Errorf("Names=%v, want [a z]", names)
	}
}

func TestStore_SaveCreatesDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub", "profiles.json")
	s := NewStore(nested)
	if err := s.Save(Profile{Name: "x"}); err != nil {
		t.Fatalf("Save should create parent dirs: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("file should exist: %v", err)
	}
}
