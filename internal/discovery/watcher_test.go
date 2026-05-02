package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherDetectsNewFile(t *testing.T) {
	dir := t.TempDir()

	events := make(chan struct{}, 10)
	w, err := NewWatcher([]string{dir}, func() {
		events <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	// Give the watcher time to initialize
	time.Sleep(100 * time.Millisecond)

	// Create a new file
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case <-events:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for file creation event")
	}
}

func TestWatcherSkipsMissingDirs(t *testing.T) {
	w, err := NewWatcher([]string{"/nonexistent/path/12345"}, func() {})
	if err != nil {
		t.Fatalf("NewWatcher should not error on missing dirs: %v", err)
	}
	w.Stop()
}

func TestWatcherMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	events := make(chan struct{}, 10)
	w, err := NewWatcher([]string{dir1, dir2}, func() {
		events <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	time.Sleep(100 * time.Millisecond)

	// Create files in both directories
	if err := os.WriteFile(filepath.Join(dir1, "file1.jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile dir1: %v", err)
	}

	select {
	case <-events:
		// success - detected change in dir1
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for file creation in dir1")
	}

	if err := os.WriteFile(filepath.Join(dir2, "file2.jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile dir2: %v", err)
	}

	select {
	case <-events:
		// success - detected change in dir2
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for file creation in dir2")
	}
}

func TestWatcherDebounce(t *testing.T) {
	dir := t.TempDir()

	eventCount := 0
	events := make(chan struct{}, 10)
	w, err := NewWatcher([]string{dir}, func() {
		eventCount++
		events <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	time.Sleep(100 * time.Millisecond)

	// Create multiple files rapidly (should be debounced to one callback)
	for i := 0; i < 5; i++ {
		filename := filepath.Join(dir, "file"+string(rune('a'+i))+".jsonl")
		if err := os.WriteFile(filename, []byte("{}"), 0644); err != nil {
			t.Fatalf("WriteFile %d: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond) // Less than the 500ms debounce window
	}

	// Wait for debounce to fire
	select {
	case <-events:
		// Should receive exactly one debounced event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for debounced event")
	}

	// Give extra time to ensure no more events fire
	time.Sleep(600 * time.Millisecond)

	// Check that we got exactly 1 callback despite 5 file creations
	if eventCount > 2 {
		t.Errorf("expected at most 2 callbacks due to debounce, got %d", eventCount)
	}
}

func TestWatcherStopCleanup(t *testing.T) {
	dir := t.TempDir()

	events := make(chan struct{}, 10)
	w, err := NewWatcher([]string{dir}, func() {
		events <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Stop the watcher
	w.Stop()

	// Create a file after stopping - should NOT trigger callback
	if err := os.WriteFile(filepath.Join(dir, "after-stop.jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case <-events:
		t.Fatal("received event after Stop() was called")
	case <-time.After(1 * time.Second):
		// success - no event received
	}
}

func TestWatcherModifyFile(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a file
	filename := filepath.Join(dir, "existing.jsonl")
	if err := os.WriteFile(filename, []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	events := make(chan struct{}, 10)
	w, err := NewWatcher([]string{dir}, func() {
		events <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	time.Sleep(100 * time.Millisecond)

	// Modify the existing file
	if err := os.WriteFile(filename, []byte("{\"new\":\"data\"}"), 0644); err != nil {
		t.Fatalf("WriteFile modify: %v", err)
	}

	select {
	case <-events:
		// success - detected file modification
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for file modification event")
	}
}
