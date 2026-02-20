package jump

import (
	"fmt"
	"os"
	"os/exec"
)

// TmuxAttach attaches to a tmux session, taking over the current terminal.
func TmuxAttach(sessionName string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// TmuxHasSession checks if a tmux session exists.
func TmuxHasSession(sessionName string) bool {
	err := exec.Command("tmux", "has-session", "-t", sessionName).Run()
	return err == nil
}

// TmuxSendKeys sends keystrokes to a tmux session.
func TmuxSendKeys(sessionName, keys string) error {
	return exec.Command("tmux", "send-keys", "-t", sessionName, keys, "Enter").Run()
}

// IsTmuxAvailable checks if tmux is installed.
func IsTmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// IsInsideTmux returns true if we're running inside a tmux session.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// SuspendAndAttach returns an exec.Cmd that attaches to (or switches to)
// a tmux session. Suitable for use with tea.ExecProcess.
func SuspendAndAttach(sessionName string) *exec.Cmd {
	if IsInsideTmux() {
		return exec.Command("tmux", "switch-client", "-t", sessionName)
	}
	return exec.Command("tmux", "attach-session", "-t", sessionName)
}

// FormatJumpCommand returns a human-readable description of the jump command.
func FormatJumpCommand(sessionName string) string {
	if IsInsideTmux() {
		return fmt.Sprintf("tmux switch-client -t %s", sessionName)
	}
	return fmt.Sprintf("tmux attach-session -t %s", sessionName)
}
