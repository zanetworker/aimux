// Package runtime defines the interface for agent execution environments.
// Runtimes manage the lifecycle of the process or container that an agent
// runs inside. The package is UI-agnostic and MUST NOT import bubbletea,
// lipgloss, or anything from the tui/ package.
package runtime

import "fmt"

// State represents the lifecycle state of a runtime environment.
type State int

const (
	StateStopped  State = iota // not running
	StateCreating              // being provisioned
	StateRunning               // active and healthy
	StateError                 // failed or unhealthy
)

// String returns a human-readable label for the state.
func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateCreating:
		return "creating"
	case StateRunning:
		return "running"
	case StateError:
		return "error"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// RuntimeStatus holds the current state plus an optional human-readable
// message (e.g. an error string when State == StateError).
type RuntimeStatus struct {
	State   State
	Message string
}

// Resources specifies compute limits for a runtime environment.
type Resources struct {
	CPULimit    string // e.g. "2" or "500m"
	MemoryLimit string // e.g. "4Gi" or "512Mi"
}

// CreateOpts carries the parameters needed to create a new runtime
// environment. Not all fields are relevant for every runtime type.
type CreateOpts struct {
	Name      string
	Image     string            // container image (container/openshell only)
	WorkDir   string            // working directory to mount/use
	Env       map[string]string // environment variables
	Resources Resources
	Sandbox   SandboxConfig // sandbox policy (openshell only)
}

// Runtime is the interface that all execution environments implement.
// Local processes, containers, and sandboxed runtimes all satisfy this
// contract so the rest of aimux can treat them uniformly.
type Runtime interface {
	// Type returns a short identifier: "local", "container", "openshell".
	Type() string

	// Name returns the instance name of this runtime.
	Name() string

	// Create provisions the runtime environment (pull image, create
	// container, etc.). A no-op for local runtimes.
	Create(opts CreateOpts) error

	// Start begins execution inside the runtime. A no-op for local
	// runtimes (the agent process IS the runtime).
	Start() error

	// Stop halts the runtime without destroying it.
	Stop() error

	// Delete removes the runtime and cleans up resources.
	Delete() error

	// Status returns the current lifecycle state.
	Status() RuntimeStatus

	// ExecPrefix returns the command prefix needed to execute a command
	// inside this runtime. For local runtimes this is nil (no prefix).
	// For containers it might be ["podman", "exec", "-it", "name"].
	ExecPrefix() []string

	// Attach connects the caller's terminal to the runtime. For local
	// runtimes this is a no-op. For containers it may exec into the
	// container interactively.
	Attach() error
}
