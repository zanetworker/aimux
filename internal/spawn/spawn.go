package spawn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zanetworker/agentmux/internal/jump"
)

// Launch runs a pre-built exec.Cmd in the specified runtime environment
// (tmux session or iTerm2 split pane). The provider name and directory
// are used to derive the tmux session name.
func Launch(cmd *exec.Cmd, providerName, dir, runtime string) error {
	if cmd == nil {
		return fmt.Errorf("spawn: nil command")
	}

	if runtime == "" {
		runtime = "tmux"
	}

	switch runtime {
	case "tmux":
		return launchTmux(cmd, providerName, dir)
	case "iterm":
		return launchITerm(cmd, dir)
	default:
		return fmt.Errorf("spawn: unsupported runtime %q (want \"tmux\" or \"iterm\")", runtime)
	}
}

// launchTmux creates a new tmux session running the command.
func launchTmux(cmd *exec.Cmd, providerName, dir string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("spawn: tmux not found in PATH: %w", err)
	}

	sessionName := TmuxSessionName(providerName, dir)

	// Build: tmux new-session -d -s <name> -c <dir> -- <binary> <args...>
	args := []string{"new-session", "-d", "-s", sessionName, "-c", dir, "--"}
	args = append(args, cmd.Path)
	args = append(args, cmd.Args[1:]...)

	tmuxCmd := exec.Command("tmux", args...)
	if err := tmuxCmd.Run(); err != nil {
		return fmt.Errorf("spawn: failed to create tmux session %q: %w", sessionName, err)
	}
	return nil
}

// launchITerm opens an iTerm2 split pane running the command.
func launchITerm(cmd *exec.Cmd, dir string) error {
	if !jump.IsITerm2() {
		return fmt.Errorf("spawn: iTerm2 runtime requested but terminal is not iTerm2")
	}

	parts := []string{cmd.Path}
	parts = append(parts, cmd.Args[1:]...)

	cmdStr := fmt.Sprintf("cd %s && %s", shellQuote(dir), strings.Join(parts, " "))
	if err := jump.ITerm2SplitPane(cmdStr); err != nil {
		return fmt.Errorf("spawn: failed to create iTerm2 split pane: %w", err)
	}
	return nil
}

// shellQuote wraps a string in single quotes for shell safety.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// TmuxSessionName returns the tmux session name for a given provider and directory.
// Format: agentmux-<provider>-<basename(dir)> with spaces replaced by hyphens.
func TmuxSessionName(provider, dir string) string {
	base := filepath.Base(dir)
	base = strings.ReplaceAll(base, " ", "-")
	return fmt.Sprintf("agentmux-%s-%s", provider, base)
}
