package jump

import (
	"fmt"
	"os/exec"
	"strings"
)

// ResumeResult describes what happened when we tried to open a session.
type ResumeResult struct {
	Method string // "tmux-pane", "iterm-pane", "exec"
	Hint   string // status bar hint for the user
}

// ResumeInPane opens a Claude session alongside aimux.
// Tries, in order:
//  1. tmux split pane (if inside tmux)
//  2. iTerm2 split pane (if inside iTerm2)
//  3. Returns nil — caller should use tea.ExecProcess as fallback
//
// Returns a ResumeResult describing what happened, or an error.
// If neither tmux nor iTerm2 is available, returns (nil, nil) to signal
// the caller should fall back to tea.ExecProcess.
func ResumeInPane(sessionID, workingDir string) (*ResumeResult, error) {
	claudeCmd := buildClaudeCommand(sessionID, workingDir)
	if claudeCmd == "" {
		return nil, fmt.Errorf("no session ID or working directory")
	}

	// 1. tmux split pane
	if IsInsideTmux() {
		args := []string{"split-window", "-v", "-l", "70%"}
		if workingDir != "" {
			args = append(args, "-c", workingDir)
		}
		args = append(args, claudeCmd)

		cmd := exec.Command("tmux", args...)
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("tmux split: %w", err)
		}
		return &ResumeResult{
			Method: "tmux-pane",
			Hint:   "Pane opened below. Ctrl+b \u2193 to focus, Ctrl+b \u2191 to return.",
		}, nil
	}

	// 2. iTerm2 split pane
	if IsITerm2() {
		if err := ITerm2SplitPane(claudeCmd); err != nil {
			return nil, fmt.Errorf("iTerm2 split: %w", err)
		}
		return &ResumeResult{
			Method: "iterm-pane",
			Hint:   "Pane opened below. Cmd+] to switch panes, Cmd+[ to return.",
		}, nil
	}

	// 3. No split pane support — signal caller to use tea.ExecProcess
	return nil, nil
}

// ResumeCmd returns an exec.Cmd for use with tea.ExecProcess.
// This suspends the TUI and runs Claude directly. When the user
// exits Claude (/exit or Ctrl+C), the TUI resumes.
func ResumeCmd(sessionID, workingDir string) *exec.Cmd {
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

// buildClaudeCommand builds a shell command string for tmux/iTerm2 panes.
func buildClaudeCommand(sessionID, workingDir string) string {
	claudeBin := findClaudeBinary()

	var parts []string
	if sessionID != "" {
		parts = append(parts, claudeBin, "--resume", sessionID)
	} else if workingDir != "" {
		parts = append(parts, claudeBin, "--continue")
	} else {
		return ""
	}

	return strings.Join(parts, " ")
}

// findClaudeBinary returns the path to the claude binary.
func findClaudeBinary() string {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "claude"
	}
	return path
}
