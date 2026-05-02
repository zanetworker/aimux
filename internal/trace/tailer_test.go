package trace

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTailerDetectsNewLines(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.jsonl")

	// Create file with initial content
	if err := os.WriteFile(path, []byte("initial line\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Start tailer (should skip initial content)
	var mu sync.Mutex
	lines := []string{}
	onLine := func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}

	tailer, err := NewTailer(path, onLine)
	if err != nil {
		t.Fatalf("NewTailer failed: %v", err)
	}
	defer tailer.Stop()

	// Append new content
	time.Sleep(100 * time.Millisecond) // Let tailer initialize
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	if _, err := f.WriteString("new line\n"); err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}
	f.Close()

	// Wait for tailer to detect (max 3 seconds)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := len(lines) > 0 && lines[0] == "new line"
		mu.Unlock()
		if found {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("Expected 'new line', got %v", lines)
}

func TestTailerSkipsExistingContent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.jsonl")

	// Create file with existing content
	if err := os.WriteFile(path, []byte("existing line 1\nexisting line 2\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Start tailer
	var mu sync.Mutex
	lines := []string{}
	onLine := func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}

	tailer, err := NewTailer(path, onLine)
	if err != nil {
		t.Fatalf("NewTailer failed: %v", err)
	}
	defer tailer.Stop()

	// Wait 500ms to ensure no lines arrive
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 0 {
		t.Fatalf("Expected no lines, got %v", lines)
	}
}

func TestTailerStopClean(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.jsonl")

	// Create empty file
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Start tailer
	tailer, err := NewTailer(path, func(string) {})
	if err != nil {
		t.Fatalf("NewTailer failed: %v", err)
	}

	// Stop should not panic or hang
	done := make(chan struct{})
	go func() {
		tailer.Stop()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() hung")
	}
}
