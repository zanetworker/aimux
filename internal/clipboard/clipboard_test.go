package clipboard

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestResumeCommand(t *testing.T) {
	tests := []struct {
		sessionID string
		want      string
	}{
		{"abc-123", "claude --resume abc-123"},
		{"", "claude --resume "},
		{"session-with-dashes", "claude --resume session-with-dashes"},
	}
	for _, tt := range tests {
		got := ResumeCommand(tt.sessionID)
		if got != tt.want {
			t.Errorf("ResumeCommand(%q) = %q, want %q", tt.sessionID, got, tt.want)
		}
	}
}

func TestCopy_Integration(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("clipboard integration test only runs on macOS")
	}
	if _, err := exec.LookPath("pbcopy"); err != nil {
		t.Skip("pbcopy not available")
	}

	err := Copy("aimux-clipboard-test")
	if err != nil {
		t.Fatalf("Copy() returned error: %v", err)
	}

	// Verify with pbpaste
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		t.Fatalf("pbpaste failed: %v", err)
	}
	if string(out) != "aimux-clipboard-test" {
		t.Errorf("clipboard content = %q, want %q", string(out), "aimux-clipboard-test")
	}
}
