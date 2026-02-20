package jump

import (
	"fmt"
	"os/exec"
	"strings"
)

// ResumeInPane opens a Claude session in a tmux split pane below claudetopus.
// Claudetopus stays visible in the top pane. When the user exits Claude,
// the bottom pane closes automatically and claudetopus goes back to full screen.
//
// If not inside tmux, falls back to a simple exec (takes over terminal).
func ResumeInPane(sessionID, workingDir string) error {
	if !IsInsideTmux() {
		// Not in tmux — fall back to direct exec (will take over terminal)
		return resumeDirect(sessionID, workingDir)
	}

	// Build the claude command to run in the split pane
	claudeCmd := buildClaudeCommand(sessionID, workingDir)
	if claudeCmd == "" {
		return fmt.Errorf("no session ID or working directory available")
	}

	// Create a tmux split pane running the claude command
	// -v = vertical split (new pane below)
	// -l 70% = new pane takes 70% of height (Claude gets more space)
	// -P = print pane info
	// -d = don't switch focus yet
	args := []string{
		"split-window",
		"-v",
		"-l", "70%",
		"-c", workingDir,
		claudeCmd,
	}

	cmd := exec.Command("tmux", args...)
	return cmd.Run()
}

// buildClaudeCommand builds the shell command string to resume a Claude session.
func buildClaudeCommand(sessionID, workingDir string) string {
	claudeBin := findClaudeBinary()

	var parts []string
	if workingDir != "" {
		parts = append(parts, fmt.Sprintf("cd %q &&", workingDir))
	}

	if sessionID != "" {
		parts = append(parts, claudeBin, "--resume", sessionID)
	} else if workingDir != "" {
		parts = append(parts, claudeBin, "--continue")
	} else {
		return ""
	}

	return strings.Join(parts, " ")
}

// resumeDirect runs claude directly (takes over the terminal).
// Used as fallback when not inside tmux.
func resumeDirect(sessionID, workingDir string) error {
	claudeBin := findClaudeBinary()

	var cmd *exec.Cmd
	if sessionID != "" {
		cmd = exec.Command(claudeBin, "--resume", sessionID)
	} else if workingDir != "" {
		cmd = exec.Command(claudeBin, "--continue")
	} else {
		return fmt.Errorf("no session ID or working directory available")
	}

	if workingDir != "" {
		cmd.Dir = workingDir
	}

	return cmd.Run()
}

// findClaudeBinary returns the path to the claude binary.
func findClaudeBinary() string {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "claude"
	}
	return path
}
