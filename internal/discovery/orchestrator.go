package discovery

import (
	"fmt"
	"os/exec"
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

// SnapshotProvider is an optional interface for providers that can accept
// a shared process snapshot. This avoids each provider calling `ps aux`
// and `tmux list-sessions` independently (~200ms each).
type SnapshotProvider interface {
	DiscoverWithSnapshot(snap *Snapshot) ([]agent.Agent, error)
}

// Snapshot holds shared data collected once and reused by all providers.
type Snapshot struct {
	PsOutput     string         // raw `ps aux` output
	TmuxSessions []TmuxSession  // parsed tmux sessions
}

// TakeSnapshot captures a process snapshot (ps aux + tmux) once.
func TakeSnapshot() *Snapshot {
	snap := &Snapshot{}
	if out, err := exec.Command("ps", "aux").Output(); err == nil {
		snap.PsOutput = string(out)
	}
	snap.TmuxSessions = ListTmuxSessions()
	return snap
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
// A single process snapshot is taken first and shared with providers
// that implement SnapshotProvider, avoiding redundant ps/tmux calls.
func (o *Orchestrator) Discover() ([]agent.Agent, error) {
	snap := TakeSnapshot()

	type result struct {
		agents []agent.Agent
	}

	ch := make(chan result, len(o.providers))
	for _, p := range o.providers {
		go func(prov AgentProvider) {
			var agents []agent.Agent
			var err error
			if sp, ok := prov.(SnapshotProvider); ok {
				agents, err = sp.DiscoverWithSnapshot(snap)
			} else {
				agents, err = prov.Discover()
			}
			if err == nil {
				ch <- result{agents: agents}
			} else {
				ch <- result{}
			}
		}(p)
	}

	var all []agent.Agent
	for range len(o.providers) {
		r := <-ch
		all = append(all, r.agents...)
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
