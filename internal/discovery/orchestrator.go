package discovery

import (
	"github.com/zanetworker/aimux/internal/agent"
)

// AgentProvider is the minimal interface that the orchestrator requires from
// each provider. It is intentionally kept small to avoid a circular dependency
// between the discovery and provider packages. Any type implementing
// provider.Provider automatically satisfies this interface.
type AgentProvider interface {
	Name() string
	Discover() ([]agent.Agent, error)
}

// Orchestrator coordinates multiple providers to produce a unified list of agents.
type Orchestrator struct {
	providers []AgentProvider
}

// NewOrchestrator creates an orchestrator that iterates the given providers.
func NewOrchestrator(providers ...AgentProvider) *Orchestrator {
	return &Orchestrator{providers: providers}
}

// Discover queries every registered provider and merges the results.
// Individual provider errors are silently skipped so that one failing
// provider does not prevent the others from returning agents.
func (o *Orchestrator) Discover() ([]agent.Agent, error) {
	var all []agent.Agent
	for _, p := range o.providers {
		agents, err := p.Discover()
		if err != nil {
			continue
		}
		all = append(all, agents...)
	}
	return all, nil
}

// ProviderFor returns the first provider whose Name() matches the given name,
// or nil if no provider matches.
func (o *Orchestrator) ProviderFor(name string) AgentProvider {
	for _, p := range o.providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}
