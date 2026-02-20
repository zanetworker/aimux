package jump

import (
	"os/exec"
)

// ResumeClaudeSession returns an exec.Cmd that resumes a Claude Code session.
// If sessionID is available, uses `claude --resume <id>`.
// If only workingDir is available, uses `claude --continue` in that directory.
// Returns nil if neither is available.
//
// The returned Cmd is meant for use with tea.ExecProcess — it suspends the
// TUI and gives the user an interactive Claude prompt. When the user exits
// Claude (/exit or Ctrl+C), control returns to the TUI.
func ResumeClaudeSession(sessionID, workingDir string) *exec.Cmd {
	claudeBin := findClaudeBinary()

	var cmd *exec.Cmd
	if sessionID != "" {
		cmd = exec.Command(claudeBin, "--resume", sessionID)
	} else if workingDir != "" {
		cmd = exec.Command(claudeBin, "--continue")
	} else {
		return nil
	}

	if workingDir != "" {
		cmd.Dir = workingDir
	}

	return cmd
}

// findClaudeBinary returns the path to the claude binary.
func findClaudeBinary() string {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "claude" // fallback, let exec handle the error
	}
	return path
}
