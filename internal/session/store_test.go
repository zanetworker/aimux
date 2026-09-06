package session_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/session"
)

func tempStore(t *testing.T) *session.FileStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	return session.NewFileStore(path)
}

func testSession(id, provider, env, dir string) *session.Session {
	return &session.Session{
		ID:           id,
		Provider:     provider,
		Environment:  env,
		WorkingDir:   dir,
		Status:       session.StatusCreated,
		StartTime:    time.Now(),
		LastActivity: time.Now(),
	}
}

func TestFileStore_CreateAndGet(t *testing.T) {
	store := tempStore(t)
	s := testSession("s1", "claude", "local", "/tmp/project")

	if err := store.Create(s); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "s1" {
		t.Errorf("ID = %q, want s1", got.ID)
	}
	if got.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", got.Provider)
	}
	if got.Environment != "local" {
		t.Errorf("Environment = %q, want local", got.Environment)
	}
}

func TestFileStore_GetNotFound(t *testing.T) {
	store := tempStore(t)
	_, err := store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestFileStore_List(t *testing.T) {
	store := tempStore(t)

	if err := store.Create(testSession("s1", "claude", "local", "/a")); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(testSession("s2", "codex", "sandbox", "/b")); err != nil {
		t.Fatal(err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
}

func TestFileStore_ListEmpty(t *testing.T) {
	store := tempStore(t)
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List len = %d, want 0", len(list))
	}
}

func TestFileStore_Update(t *testing.T) {
	store := tempStore(t)
	s := testSession("s1", "claude", "local", "/tmp")
	if err := store.Create(s); err != nil {
		t.Fatal(err)
	}

	s.Status = session.StatusRunning
	s.TokensIn = 100
	if err := store.Update(s); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.Get("s1")
	if got.Status != session.StatusRunning {
		t.Errorf("Status = %q, want running", got.Status)
	}
	if got.TokensIn != 100 {
		t.Errorf("TokensIn = %d, want 100", got.TokensIn)
	}
}

func TestFileStore_UpdateNotFound(t *testing.T) {
	store := tempStore(t)
	s := testSession("nope", "claude", "local", "/tmp")
	if err := store.Update(s); err == nil {
		t.Fatal("expected error updating nonexistent session")
	}
}

func TestFileStore_Delete(t *testing.T) {
	store := tempStore(t)
	if err := store.Create(testSession("s1", "claude", "local", "/tmp")); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get("s1")
	if err == nil {
		t.Fatal("expected not-found after delete")
	}

	list, _ := store.List()
	if len(list) != 0 {
		t.Errorf("List after delete: len = %d, want 0", len(list))
	}
}

func TestFileStore_DeleteNotFound(t *testing.T) {
	store := tempStore(t)
	if err := store.Delete("nope"); err == nil {
		t.Fatal("expected error deleting nonexistent session")
	}
}

func TestFileStore_DuplicateCreate(t *testing.T) {
	store := tempStore(t)
	s := testSession("dup", "claude", "local", "/tmp")
	if err := store.Create(s); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(s); err == nil {
		t.Fatal("expected error on duplicate create")
	}
}

func TestFileStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	store1 := session.NewFileStore(path)
	if err := store1.Create(testSession("persist", "claude", "local", "/tmp")); err != nil {
		t.Fatal(err)
	}

	// New store instance reads from the same file
	store2 := session.NewFileStore(path)
	got, err := store2.Get("persist")
	if err != nil {
		t.Fatalf("Get from new store: %v", err)
	}
	if got.ID != "persist" {
		t.Errorf("ID = %q, want persist", got.ID)
	}
}

func TestFileStore_ConcurrentAccess(t *testing.T) {
	store := tempStore(t)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "concurrent-" + string(rune('a'+n))
			s := testSession(id, "claude", "local", "/tmp")
			_ = store.Create(s)
		}(i)
	}
	wg.Wait()

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 20 {
		t.Errorf("concurrent creates: got %d, want 20", len(list))
	}
}

func TestFileStore_NonexistentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subdir", "deep", "sessions.json")
	store := session.NewFileStore(path)
	list, err := store.List()
	if err != nil {
		t.Fatalf("List on nonexistent path: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}

	// Create should auto-create the directory
	if err := store.Create(testSession("auto", "claude", "local", "/tmp")); err != nil {
		t.Fatalf("Create with auto-dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist after create: %v", err)
	}
}
