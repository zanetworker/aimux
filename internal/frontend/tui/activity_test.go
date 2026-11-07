package tui

import (
	"path/filepath"
	"testing"
)

// The TUI checks the hooks in the user's Claude Code settings; with no home
// it checks nothing (no hint).
func TestNewLocalActivity_HooksSettings(t *testing.T) {
	if got := newLocalActivity(nil, "/h").HooksSettings; got != filepath.Join("/h", ".claude", "settings.json") {
		t.Fatalf("HooksSettings = %q", got)
	}
	if l := newLocalActivity(nil, ""); l.HooksSettings != "" || !l.HooksInstalled() {
		t.Fatalf("no home: HooksSettings %q, installed %v", l.HooksSettings, l.HooksInstalled())
	}
}
