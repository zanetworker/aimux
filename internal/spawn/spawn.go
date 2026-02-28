package spawn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zanetworker/agentmux/internal/jump"
)

// LaunchConfig holds all parameters needed to spawn a new agent.
type LaunchConfig struct {
	Provider string // "claude", "codex", "gemini"
	Dir      string // absolute path to working directory
	Model    string // "" for provider default
	Mode     string // "" for default, "bypass", "plan", "full-auto"
	Runtime  string // "tmux" or "iterm"
}

// Spawn launches a new agent with the given configuration.
// It creates either a tmux session or an iTerm2 split pane depending on
// cfg.Runtime. The spawned agent runs independently and will be picked up
// by discovery on the next tick.
func Spawn(cfg LaunchConfig) error {
	cmd := BuildCommand(cfg)
	if cmd == nil {
		return fmt.Errorf("spawn: unknown provider %q", cfg.Provider)
	}

	runtime := cfg.Runtime
	if runtime == "" {
		runtime = "tmux"
	}

	switch runtime {
	case "tmux":
		return spawnTmux(cfg, cmd)
	case "iterm":
		return spawnITerm(cfg, cmd)
	default:
		return fmt.Errorf("spawn: unsupported runtime %q (want \"tmux\" or \"iterm\")", runtime)
	}
}

// spawnTmux creates a new tmux session running the agent command.
func spawnTmux(cfg LaunchConfig, cmd *exec.Cmd) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("spawn: tmux not found in PATH: %w", err)
	}

	sessionName := TmuxSessionName(cfg.Provider, cfg.Dir)

	// Build: tmux new-session -d -s <name> -c <dir> -- <binary> <args...>
	args := []string{"new-session", "-d", "-s", sessionName, "-c", cfg.Dir, "--"}
	args = append(args, cmd.Path)
	args = append(args, cmd.Args[1:]...) // Args[0] is the binary name, already in Path

	tmuxCmd := exec.Command("tmux", args...)
	if err := tmuxCmd.Run(); err != nil {
		return fmt.Errorf("spawn: failed to create tmux session %q: %w", sessionName, err)
	}
	return nil
}

// spawnITerm opens an iTerm2 split pane running the agent command.
func spawnITerm(cfg LaunchConfig, cmd *exec.Cmd) error {
	if !jump.IsITerm2() {
		return fmt.Errorf("spawn: iTerm2 runtime requested but terminal is not iTerm2 (TERM_PROGRAM=%q)", "")
	}

	// Build the full command string: "cd <dir> && <binary> <args...>"
	parts := []string{cmd.Path}
	parts = append(parts, cmd.Args[1:]...)

	cmdStr := fmt.Sprintf("cd %s && %s", shellQuote(cfg.Dir), strings.Join(parts, " "))
	if err := jump.ITerm2SplitPane(cmdStr); err != nil {
		return fmt.Errorf("spawn: failed to create iTerm2 split pane: %w", err)
	}
	return nil
}

// shellQuote wraps a string in single quotes for shell safety.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// BuildCommand constructs the exec.Cmd for the given config without running it.
// Returns nil if the provider is unknown.
func BuildCommand(cfg LaunchConfig) *exec.Cmd {
	switch cfg.Provider {
	case "claude":
		return buildClaude(cfg)
	case "codex":
		return buildCodex(cfg)
	case "gemini":
		return buildGemini(cfg)
	default:
		return nil
	}
}

// buildClaude constructs the command for the Claude Code CLI.
//
// Flags:
//   - --model <model> if Model is set
//   - --dangerously-skip-permissions if Mode == "bypass"
//   - --permission-mode plan if Mode == "plan"
func buildClaude(cfg LaunchConfig) *exec.Cmd {
	bin := findBinary("claude")
	var args []string

	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}

	switch cfg.Mode {
	case "bypass":
		args = append(args, "--dangerously-skip-permissions")
	case "plan":
		args = append(args, "--permission-mode", "plan")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = cfg.Dir
	return cmd
}

// buildCodex constructs the command for the Codex CLI.
//
// Flags:
//   - --no-alt-screen always
//   - --model <model> if Model is set
//   - --full-auto if Mode == "full-auto"
//   - --sandbox workspace-write if Mode is empty (default)
func buildCodex(cfg LaunchConfig) *exec.Cmd {
	bin := findBinary("codex")
	args := []string{"--no-alt-screen"}

	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}

	switch cfg.Mode {
	case "full-auto":
		args = append(args, "--full-auto")
	case "":
		args = append(args, "--sandbox", "workspace-write")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = cfg.Dir
	return cmd
}

// buildGemini constructs the command for the Gemini CLI.
// No flags are supported yet.
func buildGemini(cfg LaunchConfig) *exec.Cmd {
	bin := findBinary("gemini")
	cmd := exec.Command(bin)
	cmd.Dir = cfg.Dir
	return cmd
}

// TmuxSessionName returns the tmux session name for a given provider and directory.
// Format: agentmux-<provider>-<basename(dir)> with spaces replaced by hyphens.
//
// Examples:
//
//	TmuxSessionName("claude", "/Users/me/projects/blog-concept") -> "agentmux-claude-blog-concept"
//	TmuxSessionName("codex", "/tmp/my project")                  -> "agentmux-codex-my-project"
func TmuxSessionName(provider, dir string) string {
	base := filepath.Base(dir)
	base = strings.ReplaceAll(base, " ", "-")
	return fmt.Sprintf("agentmux-%s-%s", provider, base)
}

// findBinary resolves a binary name using exec.LookPath, falling back to the
// bare name if the binary is not found on PATH.
func findBinary(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}
