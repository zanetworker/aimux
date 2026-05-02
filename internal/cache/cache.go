package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

// entry is the serialized representation of an agent.Agent for caching.
type entry struct {
	PID          int       `json:"pid"`
	Name         string    `json:"name"`
	ProviderName string    `json:"provider"`
	WorkingDir   string    `json:"cwd"`
	Model        string    `json:"model"`
	Status       string    `json:"status"`
	EstCostUSD   float64   `json:"cost"`
	GitBranch    string    `json:"branch,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
}

// toEntry converts an agent.Agent to a cache entry, setting LastSeen to time.Now().
func toEntry(a agent.Agent) entry {
	return entry{
		PID:          a.PID,
		Name:         a.Name,
		ProviderName: a.ProviderName,
		WorkingDir:   a.WorkingDir,
		Model:        a.Model,
		Status:       a.Status.String(),
		EstCostUSD:   a.EstCostUSD,
		GitBranch:    a.GitBranch,
		LastSeen:     time.Now(),
	}
}

// toAgent converts a cache entry back to an agent.Agent.
// Maps LastSeen to LastActivity field.
func toAgent(e entry) agent.Agent {
	// Parse the status string back to agent.Status enum
	status := parseStatus(e.Status)

	return agent.Agent{
		PID:          e.PID,
		Name:         e.Name,
		ProviderName: e.ProviderName,
		WorkingDir:   e.WorkingDir,
		Model:        e.Model,
		Status:       status,
		EstCostUSD:   e.EstCostUSD,
		GitBranch:    e.GitBranch,
		LastActivity: e.LastSeen,
	}
}

// parseStatus converts a status string back to agent.Status enum.
// Defaults to StatusIdle (not StatusUnknown) because cached agents
// are last-known state and "idle" is the safest assumption.
func parseStatus(s string) agent.Status {
	switch s {
	case "Active":
		return agent.StatusActive
	case "Idle":
		return agent.StatusIdle
	case "Waiting":
		return agent.StatusWaitingPermission
	case "Error":
		return agent.StatusError
	default:
		return agent.StatusIdle
	}
}

// DefaultPath returns the default cache file path: ~/.aimux/cache/last-seen.json
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".aimux", "cache", "last-seen.json")
	}
	return filepath.Join(home, ".aimux", "cache", "last-seen.json")
}

// Save marshals the agents as indented JSON and writes to path.
// Creates parent directories with MkdirAll if they don't exist.
func Save(path string, agents []agent.Agent) error {
	// Convert agents to cache entries
	entries := make([]entry, len(agents))
	for i, a := range agents {
		entries[i] = toEntry(a)
	}

	// Marshal to indented JSON
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	// Create parent directories
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Write to file
	return os.WriteFile(path, data, 0644)
}

// Load reads and unmarshals agents from the cache file.
// Returns an empty slice (not error) on missing file or corrupt JSON.
func Load(path string) ([]agent.Agent, error) {
	// Read the file
	data, err := os.ReadFile(path)
	if err != nil {
		// Return empty slice for missing file
		if os.IsNotExist(err) {
			return []agent.Agent{}, nil
		}
		return []agent.Agent{}, nil
	}

	// Unmarshal entries
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		// Return empty slice for corrupt JSON
		return []agent.Agent{}, nil
	}

	// Convert entries to agents
	agents := make([]agent.Agent, len(entries))
	for i, e := range entries {
		agents[i] = toAgent(e)
	}

	return agents, nil
}
