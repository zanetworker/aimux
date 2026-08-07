package controller

import (
	"os/exec"

	"github.com/zanetworker/aimux/internal/config"
)

// ResumeArgs builds the arguments for resuming a Claude session with the
// given permission mode.
func ResumeArgs(sessionID, mode string) []string {
	args := []string{"--resume", sessionID}
	args = append(args, config.ModeFlags(mode)...)
	return args
}

// ResumeCommand builds the full exec.Cmd for resuming a session.
func ResumeCommand(claudeBin, sessionID, workingDir, mode string) *exec.Cmd {
	args := ResumeArgs(sessionID, mode)
	cmd := exec.Command(claudeBin, args...) // #nosec G204
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	return cmd
}

// ToggleBypass flips between "bypass" and "default" modes.
func ToggleBypass(current string) string {
	if current == "bypass" {
		return "default"
	}
	return "bypass"
}

// ResolveMode returns the effective mode: explicit override wins,
// then config default, then "default".
func ResolveMode(explicit, configDefault string) string {
	if explicit != "" {
		return explicit
	}
	if configDefault != "" {
		return configDefault
	}
	return "default"
}

// DefaultSessionDir returns the directory to scope session discovery to.
// Selected agent dir takes priority, then launchDir, then empty (all).
func DefaultSessionDir(selectedAgentDir, launchDir string) string {
	if selectedAgentDir != "" {
		return selectedAgentDir
	}
	return launchDir
}
