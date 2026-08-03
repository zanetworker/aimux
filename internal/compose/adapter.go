package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
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

// LaunchResult carries information about a successfully provisioned sandbox.
// The interactive terminal is established separately by the caller via
// terminal.NewOpenShellExec(SandboxName, ...).
type LaunchResult struct {
	SandboxName   string
	OTELSessionID string
}

// LaunchInSandbox provisions an OpenShell sandbox (create, inject OTEL env,
// set egress policy) and returns its name. It does not open the interactive
// terminal; the caller does that via terminal.NewOpenShellExec(SandboxName),
// which runs "openshell sandbox connect" in a real PTY.
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

	// Use a UUID so it can double as the agent's pinned Claude session id
	// (claude --session-id requires a valid UUID). Pinning the session id
	// keeps Claude's telemetry session.id stable across reconnects, so the
	// trace pane accumulates all turns under one conversation instead of
	// resetting each time the agent restarts.
	otelSessionID := uuid.NewString()
	env := otelSandboxEnv(opts.OTELEndpoint, otelSessionID)
	if env == nil {
		env = make(map[string]string)
	}
	// The agent inside the sandbox routes model traffic through the gateway's
	// in-sandbox inference proxy (inference.local), configured by
	// --auto-providers at create time. Do NOT pass the host's Vertex
	// credentials (CLAUDE_CODE_USE_VERTEX, GOOGLE_APPLICATION_CREDENTIALS,
	// etc.): CLAUDE_CODE_USE_VERTEX makes Claude Code ignore ANTHROPIC_BASE_URL
	// and dial Vertex directly, which then fails because the host's gcloud ADC
	// file does not exist inside the sandbox.
	env["ANTHROPIC_BASE_URL"] = "https://inference.local"
	env["CLAUDE_MODEL"] = "claude-sonnet-4-6"
	env["CLAUDE_CODE_MODEL"] = "claude-sonnet-4-6"
	env["ANTHROPIC_API_KEY"] = "placeholder"

	// OpenShell sandbox names are limited to 19 characters.
	sandboxName := sandboxName(provider, time.Now().Unix())

	debuglog.Log("compose: launching sandbox %s for %s (image=%s, otel=%s)",
		sandboxName, provider, image, otelSessionID)

	// Step 1: Create sandbox via openshell CLI.
	// --provider vertex configures the gateway's in-sandbox inference proxy
	// (inference.local) to route model traffic to Vertex using the GATEWAY's
	// own credentials. This is distinct from the client-side CLAUDE_CODE_USE_VERTEX
	// env (deliberately not set): the agent still dials inference.local, and the
	// gateway does the Vertex auth. Without this flag inference.local has no
	// backend and the agent's requests fail and retry.
	createArgs := []string{"sandbox", "create", "--name", sandboxName, "--from", image, "--no-tty", "--auto-providers", "--provider", "vertex"}
	for k, v := range env {
		createArgs = append(createArgs, "--env", k+"="+v)
	}
	createArgs = append(createArgs, "--", "true")

	cmd := exec.Command("openshell", createArgs...) // #nosec G204
	if out, err := cmd.CombinedOutput(); err != nil {
		raw := strings.TrimSpace(string(out))
		debuglog.Log("compose: sandbox create FAILED: %v: %s", err, raw)
		return nil, fmt.Errorf("sandbox create failed: %s", firstErrorLine(raw))
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

	for _, ep := range endpoints {
		if err := allowOTELEndpoint(sandboxName, ep); err != nil {
			debuglog.Log("compose: policy failed for %s on %s: %v", sandboxName, ep, err)
		}
	}

	// The sandbox is provisioned and ready. The interactive terminal is
	// established by the caller via terminal.NewOpenShellExec, which runs
	// "openshell sandbox connect" in a real PTY. We deliberately do NOT use
	// tmux here: a real PTY gives the connect process a controlling terminal
	// and there is no detached session to die, which is what made the
	// previous tmux-based approach unreliable.
	debuglog.Log("compose: sandbox %s provisioned and ready for connect", sandboxName)

	return &LaunchResult{
		SandboxName:   sandboxName,
		OTELSessionID: otelSessionID,
	}, nil
}

// KillSandbox stops a sandbox by name.
func (e *Engine) KillSandbox(ctx context.Context, name string) error {
	return e.inner.Stop(ctx, name)
}

// sandboxName generates a name that fits within OpenShell's 19-character limit.
func sandboxName(provider string, ts int64) string {
	short := provider
	if len(short) > 2 {
		short = short[:2]
	}
	suffix := fmt.Sprintf("%x", ts%0xFFFF)
	return fmt.Sprintf("ax-%s-%s", short, suffix)
}

// firstErrorLine extracts the first meaningful error line from openshell CLI output.
// openshell prints a tree-structured error (│, ├─▶, ╰─▶) that's hard to read
// in a single-line status hint. This finds the "Error:" line or the first
// non-decorative line containing the actual error message.
func firstErrorLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "│ ")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip ANSI color codes and log-level prefixes
		line = stripAnsi(line)
		if strings.HasPrefix(line, "WARN") || strings.HasPrefix(line, "[20") {
			continue
		}
		// Return the first error-bearing line
		if strings.Contains(line, "Error") || strings.Contains(line, "error") || strings.Contains(line, "failed") {
			return line
		}
	}
	// Fallback: return the last non-empty, non-decorative line
	for i := len(strings.Split(raw, "\n")) - 1; i >= 0; i-- {
		line := strings.TrimSpace(strings.Split(raw, "\n")[i])
		line = stripAnsi(line)
		if line != "" && !strings.HasPrefix(line, "[20") && !strings.HasPrefix(line, "WARN") {
			return line
		}
	}
	return raw
}

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == 0x1b {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
