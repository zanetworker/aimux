package spawn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/jump"
	"github.com/zanetworker/aimux/internal/runtime"
)

// Launch runs a pre-built exec.Cmd in the specified session manager
// (tmux or iTerm2). The provider name and directory are used to derive
// the tmux session name. The shell parameter specifies the login shell
// to use (e.g., "/bin/zsh"); use config.ResolveShell().
// The envPrefix is prepended to the command (e.g., OTEL env vars).
func Launch(cmd *exec.Cmd, providerName, dir, sessionMgr, shell, envPrefix string) error {
	if cmd == nil {
		return fmt.Errorf("spawn: nil command")
	}

	if sessionMgr == "" {
		sessionMgr = "tmux"
	}

	switch sessionMgr {
	case "tmux":
		return launchTmux(cmd, providerName, dir, shell, envPrefix)
	case "iterm":
		return launchITerm(cmd, dir)
	default:
		return fmt.Errorf("spawn: unsupported session manager %q (want \"tmux\" or \"iterm\")", sessionMgr)
	}
}

// LaunchInContainer creates a container, then launches the agent inside it
// via tmux. The container runs `sleep infinity` and the agent command is
// executed inside it via `podman exec`. The tmux session wraps the podman
// exec so the user can attach/detach normally.
//
// Flow: podman create → podman start → tmux new-session "podman exec ... agent"
func LaunchInContainer(cmd *exec.Cmd, providerName, dir, shell, envPrefix string, opts ContainerOpts) error {
	if cmd == nil {
		return fmt.Errorf("spawn: nil command")
	}

	engine := opts.Engine
	if engine == "" {
		engine = "podman"
	}
	if _, err := exec.LookPath(engine); err != nil {
		return fmt.Errorf("spawn: %s not found in PATH (required for container runtime)", engine)
	}

	name := ContainerName(providerName, dir)
	backend := runtime.NewPodmanBackend(opts.Engine)
	c := runtime.NewContainer(name, backend)

	env := parseEnvPrefix(envPrefix)

	image := opts.Image
	if image == "" {
		image = "fedora:41"
	}
	if err := c.Create(runtime.CreateOpts{
		WorkDir: dir,
		Image:   image,
		Env:     env,
	}); err != nil {
		return fmt.Errorf("spawn: container create: %w", err)
	}

	// Build the agent command to run inside the container
	var cmdParts []string
	cmdParts = append(cmdParts, filepath.Base(cmd.Args[0]))
	for _, arg := range cmd.Args[1:] {
		cmdParts = append(cmdParts, shellQuote(arg))
	}
	innerCmd := strings.Join(cmdParts, " ")

	// Wrap in tmux: the tmux session runs "podman exec -it <name> <shell> -lc <agent>"
	execCmd := fmt.Sprintf("%s exec -it %s %s -lc %s",
		engine, shellQuote(name), shell, shellQuote(innerCmd))

	sessionName := TmuxSessionName(providerName, dir)
	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil { // #nosec G204
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run() // #nosec G204
	}

	tmuxArgs := []string{"new-session", "-d", "-s", sessionName, "-c", dir,
		"--", shell, "-lc", execCmd}
	tmuxCmd := exec.Command("tmux", tmuxArgs...) // #nosec G204
	if err := tmuxCmd.Run(); err != nil {
		return fmt.Errorf("spawn: failed to create tmux session for container %q: %w", name, err)
	}
	return nil
}

// ContainerOpts configures container-based agent launch.
type ContainerOpts struct {
	Engine string // "podman" or "docker", defaults to "podman"
	Image  string // container image, defaults to "fedora:41"
}

// ContainerName returns the container name for a given provider and directory.
func ContainerName(provider, dir string) string {
	base := filepath.Base(dir)
	base = strings.ReplaceAll(base, " ", "-")
	return fmt.Sprintf("aimux-%s-%s", provider, base)
}

func parseEnvPrefix(envPrefix string) map[string]string {
	env := make(map[string]string)
	for _, part := range strings.Fields(envPrefix) {
		if k, v, ok := strings.Cut(part, "="); ok {
			env[k] = v
		}
	}
	return env
}

// launchTmux creates a new tmux session running the command.
func launchTmux(cmd *exec.Cmd, providerName, dir, shell, envPrefix string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("spawn: tmux not found in PATH: %w", err)
	}

	sessionName := TmuxSessionName(providerName, dir)

	// If session already exists, kill it first (user is re-launching)
	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil { // #nosec G204
		if err := exec.Command("tmux", "kill-session", "-t", sessionName).Run(); err != nil { // #nosec G204
			debuglog.Log("failed to kill existing tmux session %q: %v", sessionName, err)
		}
	}

	// Run through a login shell with RC file sourced so shell functions
	// and env vars are available (e.g., gemini() wrapper with Vertex AI config).
	// Use Args[0] (the command name) instead of Path (absolute binary path)
	// so shell functions take precedence over the raw binary.
	var cmdParts []string
	cmdParts = append(cmdParts, filepath.Base(cmd.Args[0]))
	for _, arg := range cmd.Args[1:] {
		cmdParts = append(cmdParts, shellQuote(arg))
	}
	innerCmd := strings.Join(cmdParts, " ")
	shellCmd := config.ShellRCPrefix(shell) + envPrefix + innerCmd

	args := []string{"new-session", "-d", "-s", sessionName, "-c", dir,
		"--", shell, "-lc", shellCmd}

	tmuxCmd := exec.Command("tmux", args...) // #nosec G204
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
