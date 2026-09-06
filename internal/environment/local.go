package environment

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/zanetworker/aimux/internal/agent"
)

// DiscoverySource abstracts a single source of agent discovery.
// Existing providers (Claude, Codex, Gemini) already satisfy this
// via their Name() and Discover() methods.
type DiscoverySource interface {
	Name() string
	Discover() ([]agent.Agent, error)
}

// LocalEnvironment discovers and manages agents running on the local machine.
// It delegates discovery to one or more DiscoverySource implementations.
type LocalEnvironment struct {
	name    string
	sources []DiscoverySource
}

// NewLocalEnvironment creates a LocalEnvironment that queries the given
// discovery sources during Discover().
func NewLocalEnvironment(sources ...DiscoverySource) *LocalEnvironment {
	return &LocalEnvironment{
		name:    "local",
		sources: sources,
	}
}

func (e *LocalEnvironment) Name() string { return e.name }
func (e *LocalEnvironment) Type() string { return "local" }

func (e *LocalEnvironment) Discover() ([]agent.Agent, error) {
	var all []agent.Agent
	for _, src := range e.sources {
		agents, err := src.Discover()
		if err != nil {
			continue
		}
		all = append(all, agents...)
	}
	return all, nil
}

func (e *LocalEnvironment) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	return "", fmt.Errorf("local environment: CreateSandbox not yet implemented")
}

func (e *LocalEnvironment) DeleteSandbox(ctx context.Context, name string) error {
	return fmt.Errorf("local environment: DeleteSandbox not yet implemented")
}

func (e *LocalEnvironment) ListSandboxes(ctx context.Context) ([]SandboxStatus, error) {
	return nil, nil
}

func (e *LocalEnvironment) Kill(a agent.Agent) error {
	if a.PID <= 0 {
		return fmt.Errorf("cannot kill agent %q: no PID", a.Name)
	}
	proc, err := os.FindProcess(a.PID)
	if err != nil {
		return fmt.Errorf("cannot find process %d: %w", a.PID, err)
	}
	return proc.Signal(syscall.SIGTERM)
}
