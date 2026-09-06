package environment

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/trace"
)

// OpenShellConfig holds configuration for an OpenShell environment.
type OpenShellConfig struct {
	Gateway  string
	Insecure bool
	Image    string
}

// OpenShellEnvironment discovers and manages agents running in OpenShell sandboxes.
type OpenShellEnvironment struct {
	name string
	cfg  OpenShellConfig
}

// Compile-time interface check.
var _ Environment = (*OpenShellEnvironment)(nil)

// NewOpenShellEnvironment creates an OpenShellEnvironment with the given config.
func NewOpenShellEnvironment(name string, cfg OpenShellConfig) *OpenShellEnvironment {
	if name == "" {
		name = "openshell"
	}
	return &OpenShellEnvironment{name: name, cfg: cfg}
}

func (e *OpenShellEnvironment) Name() string { return e.name }
func (e *OpenShellEnvironment) Type() string { return "openshell" }

// Discover returns agents for running OpenShell sandboxes. Uses a 3-second
// timeout so a stuck gateway never blocks the discovery tick.
func (e *OpenShellEnvironment) Discover() ([]agent.Agent, error) {
	if _, err := exec.LookPath("openshell"); err != nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "list") // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	return parseSandboxAgents(string(out)), nil
}

// CreateSandbox creates a new OpenShell sandbox.
func (e *OpenShellEnvironment) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	args := []string{"sandbox", "create"}
	if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	} else if e.cfg.Image != "" {
		args = append(args, "--image", e.cfg.Image)
	}
	cmd := exec.CommandContext(ctx, "openshell", args...) // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("openshell sandbox create: %w", err)
	}
	name := strings.TrimSpace(string(out))
	return name, nil
}

// DeleteSandbox deletes an OpenShell sandbox by name.
func (e *OpenShellEnvironment) DeleteSandbox(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "delete", name) // #nosec G204
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("openshell sandbox delete %s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

// ListSandboxes lists all OpenShell sandboxes and returns their status.
func (e *OpenShellEnvironment) ListSandboxes(ctx context.Context) ([]SandboxStatus, error) {
	if _, err := exec.LookPath("openshell"); err != nil {
		return nil, nil
	}

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "list") // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	agents := parseSandboxAgents(string(out))
	statuses := make([]SandboxStatus, len(agents))
	for i, a := range agents {
		statuses[i] = SandboxStatus{
			Name:   a.SandboxName,
			Status: a.Status.String(),
			Idle:   a.Status == agent.StatusIdle,
		}
	}
	return statuses, nil
}

// Kill terminates an agent by deleting its sandbox.
func (e *OpenShellEnvironment) Kill(a agent.Agent) error {
	if a.SandboxName == "" {
		return fmt.Errorf("cannot kill agent %q: no sandbox name", a.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return e.DeleteSandbox(ctx, a.SandboxName)
}


// FetchSessionReplies reads the Claude Code session JSONL from inside a remote
// sandbox and returns assistant reply text keyed by promptId.
func (e *OpenShellEnvironment) FetchSessionReplies(sandboxName, sessionID string) map[string]string {
	if sandboxName == "" || sessionID == "" {
		return nil
	}

	path := fmt.Sprintf("/sandbox/.claude/projects/-sandbox/%s.jsonl", sessionID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "openshell", "sandbox", "exec",
		"--name", sandboxName, "--", "cat", path).Output() // #nosec G204
	if err != nil || len(out) == 0 {
		return nil
	}

	return otel.ParseSessionReplies(out)
}

// FetchSessionTurns reads the session JSONL from inside a sandbox and builds
// trace.Turn entries directly from it, without needing OTEL data.
func (e *OpenShellEnvironment) FetchSessionTurns(sandboxName, sessionID string) []trace.Turn {
	if sandboxName == "" || sessionID == "" {
		return nil
	}

	path := fmt.Sprintf("/sandbox/.claude/projects/-sandbox/%s.jsonl", sessionID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "openshell", "sandbox", "exec",
		"--name", sandboxName, "--", "cat", path).Output() // #nosec G204
	if err != nil || len(out) == 0 {
		return nil
	}

	return otel.ParseSessionTurns(out)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// parseSandboxAgents parses the tabular output of `openshell sandbox list`
// into agent entries. Each sandbox is tagged with ProviderName "claude"
// since OpenShell sandboxes currently run Claude Code.
func parseSandboxAgents(output string) []agent.Agent {
	output = stripAnsi(output)
	var agents []agent.Agent

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "No sandbox") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name := fields[0]
		phase := fields[len(fields)-1]

		var status agent.Status
		var lastAction string
		switch phase {
		case "Ready":
			status = agent.StatusActive
		case "Error":
			status = agent.StatusError
		case "Deleting", "Terminating":
			status = agent.StatusError
			lastAction = "Deleting"
		default:
			status = agent.StatusIdle
		}

		agents = append(agents, agent.Agent{
			Name:         name,
			ProviderName: "claude",
			WorkingDir:   "/sandbox",
			Status:       status,
			LastAction:   lastAction,
			Location:     "remote",
			SandboxName:  name,
			StartTime:    time.Now(),
			LastActivity: time.Now(),
			GroupCount:   1,
		})
	}

	return agents
}
