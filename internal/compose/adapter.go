package compose

import (
	"context"
	"fmt"
	"os"
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

// LaunchInSandbox creates an OpenShell sandbox and connects via tmux.
// This replaces the old spawn.LaunchInSandbox function.
func (e *Engine) LaunchInSandbox(provider, dir string, opts LaunchOpts) (*LaunchResult, error) {
	image := opts.Image
	if image == "" {
		image = e.image
	}

	otelSessionID := fmt.Sprintf("aimux-remote-%s-%d", provider, time.Now().UnixNano())
	env := otelSandboxEnv(opts.OTELEndpoint, otelSessionID)
	if env == nil {
		env = make(map[string]string)
	}
	env["ANTHROPIC_BASE_URL"] = "https://inference.local"

	osProvider := opts.Provider
	if osProvider == "" {
		osProvider = openshellProviderName(provider)
	}

	// If a custom image is provided, use it directly (don't use Runtime which would override Image).
	// Otherwise, use the claude-code runtime profile.
	agent := &pkgcompose.Agent{
		Image: image,
		Env:   env,
		Sandbox: pkgcompose.SandboxOpts{
			Scope: "session",
			Mode:  "all",
		},
	}
	if image == "" || image == e.image {
		// Use runtime profile only when no custom image or using default
		agent.Runtime = "claude-code"
		agent.Image = "" // Let runtime resolver pick the image
	}

	debuglog.Log("compose: launching sandbox for %s (image=%s, provider=%s, otel_session=%s)",
		provider, image, osProvider, otelSessionID)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	run, err := e.inner.Start(ctx, "", pkgcompose.RunOpts{
		Agent:       agent,
		Interactive: true,
		Workspace:   dir,
	})
	if err != nil {
		return nil, fmt.Errorf("compose: sandbox launch: %w", err)
	}

	sandboxName := run.Sandbox
	if sandboxName == "" {
		sandboxName = run.Agent
	}

	sessionName := SandboxSessionName(provider, sandboxName)
	debuglog.Log("compose: sandbox %s ready, session=%s", sandboxName, sessionName)

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
