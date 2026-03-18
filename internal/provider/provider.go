package provider

import (
	"os/exec"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/task"
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
	// to the process tree. For infra providers (e.g., Kubernetes), this
	// deletes the pod or scales down the deployment.
	Kill(a agent.Agent) error
}

// Messenger is an optional interface for providers that support sending
// messages to a specific agent (e.g. writing to a Redis inbox stream).
// Check with: if m, ok := p.(provider.Messenger); ok { m.SendMessage(...) }
type Messenger interface {
	SendMessage(agentID, text string) error
}

// TaskLister is an optional interface for providers that support task management.
// The TUI checks for this via type assertion:
//
//	if tl, ok := p.(provider.TaskLister); ok { tasks, _ := tl.ListTasks() }
type TaskLister interface {
	ListTasks() ([]task.Task, error)
	GetTaskResult(taskID string) (string, error)
}

// Spawner is an optional interface for providers that can spawn agents remotely.
// The TUI checks for this via type assertion:
//
//	if sp, ok := p.(provider.Spawner); ok { sp.SpawnRemote(...) }
type Spawner interface {
	SpawnRemote(provider, role string, count int) error
	ScaleDown(provider, role string) error
}

// InfraProvider is an optional interface for providers that manage remote
// agent infrastructure (Kubernetes, EC2, SSH hosts, etc.). The TUI stores
// one of these for on-demand operations (spawn sessions, health checks,
// status display). Each backend implements its own discovery, spawning, and
// health check logic behind this interface.
//
//	if rp, ok := p.(provider.InfraProvider); ok { rp.Status() }
type InfraProvider interface {
	Provider
	TaskLister
	Spawner

	// Status returns a human-readable connection status for display.
	Status() string

	// CheckHealth validates connectivity to backing infrastructure.
	// The returned HealthStatus uses generic fields that map to any
	// backend's coordination + compute layers.
	CheckHealth() HealthStatus

	// SpawnSession creates a new interactive session instance and waits
	// for it to become ready. Returns the instance name and namespace
	// (or region, availability zone, etc. depending on the backend).
	SpawnSession(providerName string) (instanceName, namespace string, err error)

	// ScaleDownOne removes one instance of the named workload.
	ScaleDownOne(providerName, role string) error
}

// HealthStatus represents the readiness of a infra provider's infrastructure.
// The two-layer model (coordination + compute) maps to any backend:
//
//	K8s:  CoordOK=Redis, ComputeOK=cluster API
//	EC2:  CoordOK=SQS/DynamoDB, ComputeOK=EC2 API
//	SSH:  CoordOK=control host, ComputeOK=target host reachable
type HealthStatus struct {
	Configured  bool     // true if the backend is configured
	CoordOK     bool     // coordination layer healthy (Redis, SQS, etc.)
	CoordErr    string   // coordination error message
	ComputeOK   bool     // compute layer healthy (K8s API, EC2 API, etc.)
	ComputeErr  string   // compute error message
	Workloads   []string // discovered workload names (deployments, instances, etc.)
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
