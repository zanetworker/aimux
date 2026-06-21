package spawn

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/runtime"
)

// SandboxOpts configures an OpenShell sandbox launch.
type SandboxOpts struct {
	Name         string // sandbox name (empty = auto-generated)
	Image        string // sandbox image (empty = gateway default)
	Provider     string // openshell provider name for credential injection (e.g., "claude")
	Binary       string // openshell binary (empty = "openshell")
	OTELEndpoint string // host OTEL collector endpoint (rewritten for host.openshell.internal)
}

// LaunchInSandbox creates an OpenShell sandbox, injects OTEL env vars
// into the sandbox's .bashrc, connects via tmux, and starts the agent.
//
// Flow:
//  1. openshell sandbox create --provider <name>
//  2. openshell sandbox exec -- append OTEL exports to ~/.bashrc
//  3. tmux new-session "openshell sandbox connect <name>"
//  4. tmux send-keys "claude" (agent starts with .bashrc sourced)
func LaunchInSandbox(providerName, dir string, opts SandboxOpts) (*LaunchResult, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("spawn: tmux not found in PATH: %w", err)
	}

	binary := opts.Binary
	if binary == "" {
		binary = "openshell"
	}
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("spawn: %s not found in PATH", binary)
	}

	r := runtime.NewOpenShellRuntime(opts.Name, binary)

	osProvider := opts.Provider
	if osProvider == "" {
		osProvider = openshellProviderName(providerName)
	}

	// Generate a session ID for OTEL trace matching before creating the
	// sandbox, so it's injected via --env at creation time.
	otelSessionID := fmt.Sprintf("aimux-remote-%s-%d", providerName, time.Now().UnixNano())
	env := otelSandboxEnv(opts.OTELEndpoint, otelSessionID)

	debuglog.Log("spawn: creating OpenShell sandbox for %s (image=%s, provider=%s, otel_session=%s)",
		providerName, opts.Image, osProvider, otelSessionID)
	if err := r.CreateWithProvider(runtime.CreateOpts{Image: opts.Image, Env: env}, osProvider); err != nil {
		return nil, fmt.Errorf("spawn: sandbox create: %w", err)
	}
	sandboxName := r.Name()
	debuglog.Log("spawn: sandbox %s ready", sandboxName)

	// Update sandbox policy to allow OTEL traffic to the host collector.
	if opts.OTELEndpoint != "" && env != nil {
		endpoint := env["OTEL_EXPORTER_OTLP_ENDPOINT"]
		if endpoint != "" {
			_, hostPort, _ := strings.Cut(endpoint, "://")
			go func() {
				if err := allowOTELEndpoint(binary, sandboxName, hostPort); err != nil {
					debuglog.Log("spawn: OTEL policy update failed: %v", err)
				}
			}()
		}
	}

	sessionName := SandboxSessionName(providerName, sandboxName)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil { // #nosec G204
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run() // #nosec G204
	}

	connectParts := r.ConnectCommand()
	tmuxArgs := append([]string{"new-session", "-d", "-s", sessionName, "-c", dir, "--"}, connectParts...)

	tmuxCmd := exec.Command("tmux", tmuxArgs...) // #nosec G204
	if err := tmuxCmd.Run(); err != nil {
		_ = r.Delete()
		return nil, fmt.Errorf("spawn: failed to create tmux session for sandbox %q: %w", sandboxName, err)
	}

	// Wait for the connect shell to be ready, then start the agent
	// ONLY if the provider didn't already auto-start it.
	agentCmd := agentCommand(providerName)
	if agentCmd != "" {
		time.Sleep(3 * time.Second)

		if !agentAlreadyRunning(sessionName) {
			_ = exec.Command("tmux", "send-keys", "-t", sessionName, agentCmd, "Enter").Run() // #nosec G204
			debuglog.Log("spawn: sent %q to session %s", agentCmd, sessionName)
		} else {
			debuglog.Log("spawn: agent already running in %s, skipping send-keys", sessionName)
		}
	}

	return &LaunchResult{
		TmuxSession:   sessionName,
		SandboxName:   sandboxName,
		OTELSessionID: otelSessionID,
	}, nil
}

// allowOTELEndpoint updates the sandbox policy to permit traffic to the
// OTEL collector on the host. Without this, the egress proxy returns 403.
func allowOTELEndpoint(binary, sandboxName, hostPort string) error {
	args := []string{"policy", "update", sandboxName,
		"--add-endpoint", hostPort + ":read-write:rest:enforce",
		"--binary", "/usr/bin/node",
		"--binary", "/usr/local/bin/node",
		"--add-allow", hostPort + ":POST:/**",
		"--wait", "--timeout", "15",
	}
	cmd := exec.Command(binary, args...) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	debuglog.Log("spawn: OTEL policy updated for sandbox %s", sandboxName)
	return nil
}

// otelSandboxEnv returns env vars for OTEL trace forwarding via
// host.openshell.internal. The sessionID is injected as an OTEL resource
// attribute so aimux can match spans to the right session.
func otelSandboxEnv(hostEndpoint, sessionID string) map[string]string {
	if hostEndpoint == "" {
		return nil
	}

	_, hostPort, _ := strings.Cut(hostEndpoint, "://")
	port := "4318"
	if _, p, ok := strings.Cut(hostPort, ":"); ok && p != "" {
		port = p
	}

	endpoint := fmt.Sprintf("http://host.openshell.internal:%s", port)
	env := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":      "1",
		"OTEL_EXPORTER_OTLP_PROTOCOL":      "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT":      endpoint,
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": endpoint + "/v1/logs",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL": "http/protobuf",
		"OTEL_METRICS_EXPORTER":            "otlp",
		"OTEL_LOGS_EXPORTER":               "otlp",
		"OTEL_LOG_USER_PROMPTS":            "1",
		"OTEL_LOG_TOOL_DETAILS":            "1",
		"OTEL_LOGS_EXPORT_INTERVAL":        "2000",
	}
	if sessionID != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = "aimux.session_id=" + sessionID
	}
	return env
}

// agentAlreadyRunning checks the tmux pane content to detect whether
// the provider auto-started the agent. If the pane contains Claude's
// UI markers (welcome message, prompt indicator), the agent is running
// and we should NOT send "claude" via send-keys.
func agentAlreadyRunning(sessionName string) bool {
	out, err := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p").Output() // #nosec G204
	if err != nil {
		return false
	}
	pane := strings.ToLower(string(out))
	// Claude Code indicators: welcome message, prompt, or the status bar
	for _, marker := range []string{"what would you like", "ready when you are", "for shortcuts", "claude code v", "help you today"} {
		if strings.Contains(pane, marker) {
			return true
		}
	}
	return false
}

// agentCommand returns the CLI command that starts the coding agent.
func agentCommand(provider string) string {
	switch provider {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini"
	default:
		return ""
	}
}

// openshellProviderName maps aimux provider names to OpenShell provider names.
func openshellProviderName(provider string) string {
	switch provider {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	default:
		return ""
	}
}

// SandboxSessionName returns the tmux session name for a remote sandbox.
func SandboxSessionName(provider, sandboxName string) string {
	return fmt.Sprintf("aimux-remote-%s-%s", provider, sandboxName)
}
