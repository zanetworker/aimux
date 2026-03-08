package provider

import (
	"os/exec"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/trace"
)

// Provider discovers and manages AI CLI agents of a specific type.
// Adding a new provider requires only implementing this interface and
// registering in app.go NewApp() — no other files need changes.
type Provider interface {
	Name() string
	Discover() ([]agent.Agent, error)
	ResumeCommand(a agent.Agent) *exec.Cmd

	// CanEmbed returns true if the agent's TUI can run inside an
	// embedded PTY (split view). False means trace-only view with
	// jump-out for interaction.
	CanEmbed() bool

	// FindSessionFile resolves the session/trace file for an agent.
	// Each provider knows its own storage layout. Returns "" if none found.
	FindSessionFile(a agent.Agent) string

	// RecentDirs returns recently-used project directories from this
	// provider's session history, sorted by most recent first.
	RecentDirs(max int) []RecentDir

	// SpawnCommand builds the exec.Cmd to launch a new agent session
	// in the given directory with the specified model and mode.
	SpawnCommand(dir, model, mode string) *exec.Cmd

	// SpawnArgs returns the available models and modes for the launcher UI.
	SpawnArgs() SpawnArgs

	// ParseTrace reads a session/trace file and parses it into a
	// provider-specific structured trace. Each provider knows its own
	// log format (Claude JSONL, Codex JSONL, Gemini JSON).
	ParseTrace(filePath string) ([]trace.Turn, error)

	// OTELEnv returns the shell env var prefix needed to enable OTEL
	// tracing for this provider, pointing at the given endpoint.
	// Each provider knows its own OTEL activation mechanism.
	OTELEnv(endpoint string) string

	// OTELServiceName returns the service.name value this provider
	// emits in OTEL resource attributes (e.g. "claude-code").
	OTELServiceName() string

	// SubagentAttrKeys returns the OTEL attribute names this provider
	// uses for subagent identity. Return zero AttrKeys if the provider
	// doesn't support subagent tracking.
	SubagentAttrKeys() subagent.AttrKeys

	// Kill stops the agent. For local providers, this sends SIGTERM/SIGKILL
	// to the process tree. For remote providers (e.g., Kubernetes), this
	// deletes the pod or scales down the deployment.
	Kill(a agent.Agent) error
}

// Messenger is an optional interface for providers that support sending
// messages to a specific agent (e.g. writing to a Redis inbox stream).
// Check with: if m, ok := p.(provider.Messenger); ok { m.SendMessage(...) }
type Messenger interface {
	SendMessage(agentID, text string) error
}

// RecentDir is a recently-used project directory from a provider's session history.
type RecentDir struct {
	Path     string
	LastUsed time.Time
}

// SpawnArgs describes the available options for launching a new agent.
type SpawnArgs struct {
	Models []string // e.g., ["default", "opus", "sonnet", "haiku"]
	Modes  []string // e.g., ["default", "bypass", "plan"]
}

// Segment is a single conversation turn, provider-agnostic.
type Segment struct {
	Time    time.Time
	Role    Role
	Content string
	Tool    string // tool name if Role==RoleTool
	Detail  string // e.g., file path, command snippet
}

// Role identifies who produced a conversation segment.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleTool
	RoleSystem
)

func (r Role) String() string {
	switch r {
	case RoleUser:
		return "User"
	case RoleAssistant:
		return "Assistant"
	case RoleTool:
		return "Tool"
	case RoleSystem:
		return "System"
	default:
		return "Unknown"
	}
}
