package terminal

import (
	"testing"
)

func TestCaptureTmuxPane_NonexistentSession(t *testing.T) {
	got := CaptureTmuxPane("aimux-test-nonexistent-session-xyz", 5)
	if got != "" {
		t.Errorf("CaptureTmuxPane(nonexistent) = %q, want empty string", got)
	}
}

func TestCaptureTmuxPane_ZeroLines(t *testing.T) {
	got := CaptureTmuxPane("any-session", 0)
	if got != "" {
		t.Errorf("CaptureTmuxPane(0 lines) = %q, want empty string", got)
	}
}

func TestCaptureTmuxPane_NegativeLines(t *testing.T) {
	got := CaptureTmuxPane("any-session", -3)
	if got != "" {
		t.Errorf("CaptureTmuxPane(negative lines) = %q, want empty string", got)
	}
}

func TestCaptureTmuxPane_EmptySessionName(t *testing.T) {
	got := CaptureTmuxPane("", 5)
	if got != "" {
		t.Errorf("CaptureTmuxPane(empty) = %q, want empty string", got)
	}
}
