package debuglog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	mu   sync.Mutex
	file *os.File
)

// Init opens the debug log file at ~/.aimux/debug.log.
// Safe to call multiple times; subsequent calls are no-ops.
// If the file cannot be opened, logging silently does nothing.
func Init() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".aimux")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	file = f
}

// Log writes a timestamped message to the debug log.
// No-op if Init was not called or failed.
func Log(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(file, "%s  %s\n", time.Now().Format("15:04:05.000"), msg)
}

// Close closes the log file. Safe to call if Init was not called.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
}
