package discovery

import (
	"fmt"
	"path/filepath"
	"strings"

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
// Agents sharing the same project name get short unique suffixes to
// disambiguate them in the UI (e.g. "myproject #1", "myproject #2").
func (o *Orchestrator) Discover() ([]agent.Agent, error) {
	var all []agent.Agent
	for _, p := range o.providers {
		agents, err := p.Discover()
		if err != nil {
			continue
		}
		all = append(all, agents...)
	}
	assignUniqueSuffixes(all)
	return all, nil
}

// assignUniqueSuffixes adds "#N" suffixes to agent names when multiple agents
// share the same ShortProject name. This disambiguates multiple sessions in
// the same directory across any provider.
func assignUniqueSuffixes(agents []agent.Agent) {
	// Count how many agents share each project name.
	counts := make(map[string]int)
	for _, a := range agents {
		name := strings.ToLower(a.ShortProject())
		counts[name]++
	}

	// Assign suffixes to duplicates.
	seen := make(map[string]int) // name -> next suffix number
	for i := range agents {
		name := strings.ToLower(agents[i].ShortProject())
		if counts[name] <= 1 {
			continue
		}
		seen[name]++
		baseName := filepath.Base(agents[i].WorkingDir)
		agents[i].Name = fmt.Sprintf("%s #%d", baseName, seen[name])
	}
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
