package spawn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/jump"
)

// Launch runs a pre-built exec.Cmd in the specified runtime environment
// (tmux session or iTerm2 split pane). The provider name and directory
// are used to derive the tmux session name. The shell parameter specifies
// the login shell to use (e.g., "/bin/zsh"); use config.ResolveShell().
// The envPrefix is prepended to the command (e.g., OTEL env vars).
func Launch(cmd *exec.Cmd, providerName, dir, runtime, shell, envPrefix string) error {
	if cmd == nil {
		return fmt.Errorf("spawn: nil command")
	}

	if runtime == "" {
		runtime = "tmux"
	}

	switch runtime {
	case "tmux":
		return launchTmux(cmd, providerName, dir, shell, envPrefix)
	case "iterm":
		return launchITerm(cmd, dir)
	default:
		return fmt.Errorf("spawn: unsupported runtime %q (want \"tmux\" or \"iterm\")", runtime)
	}
}

// launchTmux creates a new tmux session running the command.
func launchTmux(cmd *exec.Cmd, providerName, dir, shell, envPrefix string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("spawn: tmux not found in PATH: %w", err)
	}

	sessionName := TmuxSessionName(providerName, dir)

	// If session already exists, kill it first (user is re-launching)
	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	}

	// Run through a login shell with RC file sourced so shell functions
	// and env vars are available (e.g., gemini() wrapper with Vertex AI config).
	// Use Args[0] (the command name) instead of Path (absolute binary path)
	// so shell functions take precedence over the raw binary.
	var cmdParts []string
	cmdParts = append(cmdParts, filepath.Base(cmd.Args[0]))
	cmdParts = append(cmdParts, cmd.Args[1:]...)
	innerCmd := strings.Join(cmdParts, " ")
	shellCmd := config.ShellRCPrefix(shell) + envPrefix + innerCmd

	args := []string{"new-session", "-d", "-s", sessionName, "-c", dir,
		"--", shell, "-lc", shellCmd}

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
// Format: aimux-<provider>-<basename(dir)> with spaces replaced by hyphens.
func TmuxSessionName(provider, dir string) string {
	base := filepath.Base(dir)
	base = strings.ReplaceAll(base, " ", "-")
	return fmt.Sprintf("aimux-%s-%s", provider, base)
}
