package tui

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// NewApp runs before the first frame. It used to ping the configured Redis
// (3 s timeout) for a coordinator the TUI never used.
func TestNewApp_DoesNotWaitForRedis(t *testing.T) {
	// A Redis that accepts connections and never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
		}
	}()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("coordination:\n  redis_url: redis://%s\notel:\n  enabled: false\n", ln.Addr())
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)

	start := time.Now()
	_ = NewApp()
	if d := time.Since(start); d > time.Second {
		t.Errorf("NewApp took %v with an unresponsive Redis configured; it must not wait for it", d)
	}
}
