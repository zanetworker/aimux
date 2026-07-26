package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/debuglog"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

// Options configures the compose Engine adapter.
type Options struct {
	Binary   string // openshell binary path (default: "openshell")
	Gateway  string // gateway endpoint
	Insecure bool   // skip TLS verification
	Image    string // default sandbox image
	Executor pkgcompose.Executor // optional: inject for testing
}

// Engine wraps agent-compose's Engine with aimux-specific defaults.
type Engine struct {
	inner    *pkgcompose.Engine
	cfg      *pkgcompose.Config
	image    string
	executor pkgcompose.Executor
}

// New creates an Engine from aimux config.
func New(opts Options) (*Engine, error) {
	binary := opts.Binary
	if binary == "" {
		binary = "openshell"
	}

	cfg := pkgcompose.DefaultConfig()

	var composeOpts []pkgcompose.Option
	composeOpts = append(composeOpts, pkgcompose.WithConfig(cfg))

	var executor pkgcompose.Executor
	if opts.Executor != nil {
		executor = opts.Executor
		composeOpts = append(composeOpts, pkgcompose.WithExecutor(executor))
	} else {
		executor = pkgcompose.NewCLIExecutor(binary, os.Stdin, os.Stdout, os.Stderr)
		composeOpts = append(composeOpts, pkgcompose.WithExecutor(executor))
	}

	inner := pkgcompose.New(composeOpts...)

	return &Engine{
		inner:    inner,
		cfg:      cfg,
		image:    opts.Image,
		executor: executor,
	}, nil
}

// Inner returns the underlying agent-compose Engine for direct access.
func (e *Engine) Inner() *pkgcompose.Engine {
	return e.inner
}

// LaunchOpts configures a sandbox launch.
type LaunchOpts struct {
	Name         string
	Image        string
	Provider     string
	OTELEndpoint string
}

// LaunchResult carries information about a successfully launched sandbox session.
type LaunchResult struct {
	TmuxSession   string
	SandboxName   string
	OTELSessionID string
}

// LaunchInSandbox creates an OpenShell sandbox, wraps "openshell sandbox
// connect" in a tmux session, and starts the agent inside it. The tmux
// session is what the TUI mirrors for the live terminal view.
func (e *Engine) LaunchInSandbox(provider, dir string, opts LaunchOpts) (*LaunchResult, error) {
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	_ = dir // dir is used for workspace context in future extensions
	image := opts.Image
	if image == "" {
		image = e.image
	}
	if image == "" {
		image = "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
	}

	otelSessionID := fmt.Sprintf("aimux-remote-%s-%d", provider, time.Now().UnixNano())
	env := otelSandboxEnv(opts.OTELEndpoint, otelSessionID)
	if env == nil {
		env = make(map[string]string)
	}
	// TODO(azaalouk): revert to "https://inference.local" once NVIDIA/OpenShell#2444 merges.
	// Workaround: host-side proxy strips context_management before forwarding to Vertex.
	// Start the proxy with: node /tmp/strip-proxy.js
	env["ANTHROPIC_BASE_URL"] = "http://host.openshell.internal:8081"
	env["CLAUDE_MODEL"] = "claude-sonnet-4-6"
	env["CLAUDE_CODE_MODEL"] = "claude-sonnet-4-6"
	env["ANTHROPIC_API_KEY"] = "placeholder"

	sandboxName := fmt.Sprintf("ax-%s-%d", provider[:min(len(provider), 5)], time.Now().Unix())

	debuglog.Log("compose: launching sandbox %s for %s (image=%s, otel=%s)",
		sandboxName, provider, image, otelSessionID)

	// Step 1: Create sandbox via openshell CLI.
	createArgs := []string{"sandbox", "create", "--name", sandboxName, "--from", image, "--no-tty", "--auto-providers", "--provider", "vertex"}
	for k, v := range env {
		createArgs = append(createArgs, "--env", k+"="+v)
	}
	createArgs = append(createArgs, "--", "true")

	cmd := exec.Command("openshell", createArgs...) // #nosec G204
	if out, err := cmd.CombinedOutput(); err != nil {
		debuglog.Log("compose: sandbox create FAILED: %v: %s", err, strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("compose: sandbox create: %s: %w", strings.TrimSpace(string(out)), err)
	}
	debuglog.Log("compose: sandbox %s created", sandboxName)

	// Step 2: Inject OTEL env vars into .bashrc so sandbox connect shells get them.
	// --env only applies to the container's initial process, not to shells
	// spawned by "sandbox connect".
	for k, v := range env {
		exportCmd := fmt.Sprintf("echo 'export %s=%s' >> ~/.bashrc", k, v)
		_ = exec.Command("openshell", "sandbox", "exec", "--name", sandboxName, // #nosec G204
			"--", "bash", "-c", exportCmd).Run()
	}
	debuglog.Log("compose: OTEL env vars written to .bashrc for %s", sandboxName)

	// Step 3: Update egress policy BEFORE agent starts.
	// Claude Code fires API calls immediately on startup;
	// the policy must be loaded before then.
	var endpoints []string
	if opts.OTELEndpoint != "" {
		if hp := otelHostPort(opts.OTELEndpoint); hp != "" {
			endpoints = append(endpoints, hp)
		}
	}
	// TODO(azaalouk): remove host.openshell.internal:8081 once NVIDIA/OpenShell#2444 merges.
	endpoints = append(endpoints, "host.openshell.internal:8081")

	for _, ep := range endpoints {
		if err := allowOTELEndpoint(sandboxName, ep); err != nil {
			debuglog.Log("compose: policy failed for %s on %s: %v", sandboxName, ep, err)
		}
	}

	// Step 4: Create tmux session wrapping "openshell sandbox connect"
	sessionName := SandboxSessionName(provider, sandboxName)
	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil { // #nosec G204
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run() // #nosec G204
	}

	connectArgs := []string{"new-session", "-d", "-s", sessionName, "--",
		"openshell", "sandbox", "connect", sandboxName}
	if err := exec.Command("tmux", connectArgs...).Run(); err != nil { // #nosec G204
		debuglog.Log("compose: tmux session FAILED for %s: %v", sandboxName, err)
		_ = exec.Command("openshell", "sandbox", "delete", sandboxName).Run() // #nosec G204
		return nil, fmt.Errorf("compose: tmux session: %w", err)
	}
	debuglog.Log("compose: tmux session %s created", sessionName)

	// Step 3: Wait for shell, then start the agent
	agentCmd := agentBinary(provider)
	time.Sleep(3 * time.Second)
	_ = exec.Command("tmux", "send-keys", "-t", sessionName, agentCmd, "Enter").Run() // #nosec G204
	debuglog.Log("compose: sent %q to %s", agentCmd, sessionName)

	return &LaunchResult{
		TmuxSession:   sessionName,
		SandboxName:   sandboxName,
		OTELSessionID: otelSessionID,
	}, nil
}

// KillSandbox stops a sandbox by name.
func (e *Engine) KillSandbox(ctx context.Context, name string) error {
	return e.inner.Stop(ctx, name)
}

// SandboxSessionName returns the tmux session name for a remote sandbox.
func SandboxSessionName(provider, sandboxName string) string {
	return fmt.Sprintf("aimux-remote-%s-%s", provider, sandboxName)
}

func agentBinary(provider string) string {
	switch provider {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini"
	default:
		return provider
	}
}

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

// allowOTELEndpoint updates the sandbox egress policy to permit OTEL
// traffic from the agent process (node) to the host collector.
func allowOTELEndpoint(sandboxName, hostPort string) error {
	args := []string{"policy", "update", sandboxName,
		"--add-endpoint", hostPort + ":read-write:rest:enforce",
		"--binary", "/usr/bin/node",
		"--binary", "/usr/local/bin/node",
		"--binary", "/usr/local/bin/claude",
		"--add-allow", hostPort + ":POST:/**",
		"--add-allow", hostPort + ":GET:/**",
		"--wait", "--timeout", "15",
	}
	cmd := exec.Command("openshell", args...) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	debuglog.Log("compose: policy updated for %s on %s", sandboxName, hostPort)
	return nil
}

// otelHostPort converts an OTEL endpoint to the host:port the sandbox uses.
// The sandbox always reaches the host via host.openshell.internal.
func otelHostPort(endpoint string) string {
	_, hostPort, _ := strings.Cut(endpoint, "://")
	if hostPort == "" {
		return ""
	}
	port := "4318"
	if _, p, ok := strings.Cut(hostPort, ":"); ok && p != "" {
		port = p
	}
	return "host.openshell.internal:" + port
}

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
	logsEndpoint := endpoint + "/v1/logs"
	if sessionID != "" {
		logsEndpoint += "?aimux_session=" + sessionID
	}

	env := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":     "1",
		"OTEL_EXPORTER_OTLP_PROTOCOL":      "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT":      endpoint,
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": logsEndpoint,
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL": "http/protobuf",
		"OTEL_METRICS_EXPORTER":            "otlp",
		"OTEL_LOGS_EXPORTER":               "otlp",
		"OTEL_LOG_USER_PROMPTS":            "1",
		"OTEL_LOG_TOOL_DETAILS":            "1",
		"OTEL_LOGS_EXPORT_INTERVAL":        "2000",
	}
	if sessionID != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = "aimux.session_id=" + sessionID
		env["OTEL_EXPORTER_OTLP_HEADERS"] = "X-Aimux-Session-Id=" + sessionID
		env["OTEL_EXPORTER_OTLP_LOGS_HEADERS"] = "X-Aimux-Session-Id=" + sessionID
	}
	return env
}
