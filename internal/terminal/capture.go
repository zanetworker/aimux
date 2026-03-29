package terminal

import (
	"fmt"
	"os/exec"
	"strings"
)

// CaptureTmuxPane returns the last N lines from a tmux pane's visible output.
// Returns empty string if the session doesn't exist, capture fails, or lines <= 0.
// This is a lightweight read-only operation (~3-5ms) suitable for polling.
func CaptureTmuxPane(sessionName string, lines int) string {
	if lines <= 0 || sessionName == "" {
		return ""
	}
	out, err := exec.Command(
		"tmux", "capture-pane",
		"-t", sessionName,
		"-p",
		"-S", fmt.Sprintf("-%d", lines),
	).Output()
	if err != nil {
		return ""
	}
	result := strings.TrimRight(string(out), "\n ")
	return result
}
