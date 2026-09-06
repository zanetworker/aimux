package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/session"
)

func tempSQLiteStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteStore_CreateAndGet(t *testing.T) {
	store := tempSQLiteStore(t)
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

func TestSQLiteStore_GetNotFound(t *testing.T) {
	store := tempSQLiteStore(t)
	_, err := store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestSQLiteStore_List(t *testing.T) {
	store := tempSQLiteStore(t)

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

func TestSQLiteStore_ListEmpty(t *testing.T) {
	store := tempSQLiteStore(t)
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List len = %d, want 0", len(list))
	}
}

func TestSQLiteStore_Update(t *testing.T) {
	store := tempSQLiteStore(t)
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

func TestSQLiteStore_UpdateNotFound(t *testing.T) {
	store := tempSQLiteStore(t)
	s := testSession("nope", "claude", "local", "/tmp")
	if err := store.Update(s); err == nil {
		t.Fatal("expected error updating nonexistent session")
	}
}

func TestSQLiteStore_Delete(t *testing.T) {
	store := tempSQLiteStore(t)
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

func TestSQLiteStore_DeleteNotFound(t *testing.T) {
	store := tempSQLiteStore(t)
	if err := store.Delete("nope"); err == nil {
		t.Fatal("expected error deleting nonexistent session")
	}
}

func TestSQLiteStore_DuplicateCreate(t *testing.T) {
	store := tempSQLiteStore(t)
	s := testSession("dup", "claude", "local", "/tmp")
	if err := store.Create(s); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(s); err == nil {
		t.Fatal("expected error on duplicate create")
	}
}

func TestSQLiteStore_ConcurrentAccess(t *testing.T) {
	store := tempSQLiteStore(t)
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

func TestSQLiteStore_AddEventAndList(t *testing.T) {
	store := tempSQLiteStore(t)
	if err := store.Create(testSession("s1", "claude", "local", "/tmp")); err != nil {
		t.Fatal(err)
	}

	evt := &session.SessionEvent{
		SessionID: "s1",
		Timestamp: time.Now(),
		Type:      "started",
		Data:      `{"pid": 1234}`,
	}
	if err := store.AddEvent("s1", evt); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}

	evt2 := &session.SessionEvent{
		SessionID: "s1",
		Timestamp: time.Now(),
		Type:      "tool_call",
		Data:      `{"tool": "bash"}`,
	}
	if err := store.AddEvent("s1", evt2); err != nil {
		t.Fatalf("AddEvent second: %v", err)
	}

	events, err := store.ListEvents("s1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListEvents len = %d, want 2", len(events))
	}
	if events[0].Type != "started" {
		t.Errorf("events[0].Type = %q, want started", events[0].Type)
	}
	if events[1].Type != "tool_call" {
		t.Errorf("events[1].Type = %q, want tool_call", events[1].Type)
	}
	if events[0].ID == 0 {
		t.Error("events[0].ID should be set by AUTOINCREMENT")
	}
}

func TestSQLiteStore_ListEventsEmpty(t *testing.T) {
	store := tempSQLiteStore(t)
	if err := store.Create(testSession("s1", "claude", "local", "/tmp")); err != nil {
		t.Fatal(err)
	}

	events, err := store.ListEvents("s1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("ListEvents len = %d, want 0", len(events))
	}
}

func TestSQLiteStore_AddEventNoSession(t *testing.T) {
	store := tempSQLiteStore(t)
	evt := &session.SessionEvent{
		SessionID: "nonexistent",
		Timestamp: time.Now(),
		Type:      "started",
	}
	if err := store.AddEvent("nonexistent", evt); err == nil {
		t.Fatal("expected error adding event for nonexistent session")
	}
}

func TestSQLiteStore_NewFields(t *testing.T) {
	store := tempSQLiteStore(t)
	s := testSession("s1", "claude", "local", "/tmp")
	s.Harness = "sdk"
	s.Error = "something broke"
	s.Metadata = `{"key": "value"}`

	if err := store.Create(s); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Harness != "sdk" {
		t.Errorf("Harness = %q, want sdk", got.Harness)
	}
	if got.Error != "something broke" {
		t.Errorf("Error = %q, want 'something broke'", got.Error)
	}
	if got.Metadata != `{"key": "value"}` {
		t.Errorf("Metadata = %q, want {\"key\": \"value\"}", got.Metadata)
	}
}

func TestSQLiteStore_DeleteCascadesEvents(t *testing.T) {
	store := tempSQLiteStore(t)
	if err := store.Create(testSession("s1", "claude", "local", "/tmp")); err != nil {
		t.Fatal(err)
	}

	evt1 := &session.SessionEvent{
		SessionID: "s1",
		Timestamp: time.Now(),
		Type:      "started",
	}
	evt2 := &session.SessionEvent{
		SessionID: "s1",
		Timestamp: time.Now(),
		Type:      "tool_call",
		Data:      `{"tool": "bash"}`,
	}
	if err := store.AddEvent("s1", evt1); err != nil {
		t.Fatal(err)
	}
	if err := store.AddEvent("s1", evt2); err != nil {
		t.Fatal(err)
	}

	// Delete session should cascade to events
	if err := store.Delete("s1"); err != nil {
		t.Fatalf("Delete session with events: %v", err)
	}

	// Session should be gone
	_, err := store.Get("s1")
	if err == nil {
		t.Fatal("expected not-found after delete")
	}

	// Events should also be gone
	events, err := store.ListEvents("s1")
	if err != nil {
		t.Fatalf("ListEvents after cascade delete: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after cascade delete, got %d", len(events))
	}
}

func TestSQLiteStore_CompileTimeCheck(t *testing.T) {
	var _ session.Store = (*session.SQLiteStore)(nil)
}

func TestMigrateFromJSON(t *testing.T) {
	dir := t.TempDir()

	// Write a JSON sessions file
	s1 := testSession("m1", "claude", "local", "/tmp/a")
	s2 := testSession("m2", "codex", "sandbox", "/tmp/b")
	data := map[string]*session.Session{
		"m1": s1,
		"m2": s2,
	}
	raw, _ := json.Marshal(data)
	jsonPath := filepath.Join(dir, "sessions.json")
	os.WriteFile(jsonPath, raw, 0o600)

	// Create SQLite store
	dbPath := filepath.Join(dir, "test.db")
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	// Migrate
	if err := session.MigrateFromJSON(jsonPath, store); err != nil {
		t.Fatalf("MigrateFromJSON: %v", err)
	}

	// Verify sessions were imported
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
}

func TestMigrateFromJSON_NoFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Should not error when JSON file doesn't exist
	if err := session.MigrateFromJSON(filepath.Join(dir, "nope.json"), store); err != nil {
		t.Fatalf("MigrateFromJSON with no file: %v", err)
	}
}

func TestMigrateFromJSON_SkipsIfDBNotEmpty(t *testing.T) {
	dir := t.TempDir()

	// Write JSON
	data := map[string]*session.Session{
		"m1": testSession("m1", "claude", "local", "/tmp"),
	}
	raw, _ := json.Marshal(data)
	jsonPath := filepath.Join(dir, "sessions.json")
	os.WriteFile(jsonPath, raw, 0o600)

	// Create store and add a session directly
	dbPath := filepath.Join(dir, "test.db")
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	existing := testSession("existing", "gemini", "local", "/tmp")
	store.Create(existing)

	// Migrate should skip
	if err := session.MigrateFromJSON(jsonPath, store); err != nil {
		t.Fatalf("MigrateFromJSON: %v", err)
	}

	list, _ := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session (no migration), got %d", len(list))
	}
	if list[0].ID != "existing" {
		t.Errorf("expected existing session, got %q", list[0].ID)
	}
}
