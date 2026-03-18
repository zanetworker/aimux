package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogWritesToFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "debug.log")

	// Manually open the file (bypassing Init's hardcoded path)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}

	mu.Lock()
	file = f
	mu.Unlock()

	Log("test message %s %d", "hello", 42)

	mu.Lock()
	file.Close()
	file = nil
	mu.Unlock()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "test message hello 42") {
		t.Errorf("log content = %q, want it to contain 'test message hello 42'", content)
	}
}

func TestLogNoopWithoutInit(t *testing.T) {
	// Ensure file is nil
	mu.Lock()
	old := file
	file = nil
	mu.Unlock()

	// Should not panic
	Log("this should be a no-op")

	mu.Lock()
	file = old
	mu.Unlock()
}

func TestCloseIdempotent(t *testing.T) {
	// Should not panic when called without Init
	Close()
	Close()
}
