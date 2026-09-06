package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var validAgentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// ValidateAgentName checks that a name is non-empty and contains only
// alphanumeric characters and hyphens, starting with an alphanumeric.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name must not be empty")
	}
	if !validAgentName.MatchString(name) {
		return fmt.Errorf("agent name %q must be alphanumeric with hyphens, starting with a letter or digit", name)
	}
	return nil
}

// AgentFileName returns the filename for a named agent config.
func AgentFileName(name string) string {
	return name + ".agent.yaml"
}

// LoadAgentDir reads all *.agent.yaml files from a directory, returning
// configs sorted by name. Returns nil, nil if the directory does not exist.
func LoadAgentDir(dir string) ([]AgentConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent dir %s: %w", dir, err)
	}

	var agents []AgentConfig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".agent.yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read agent file %s: %w", e.Name(), err)
		}
		var ac AgentConfig
		if err := yaml.Unmarshal(data, &ac); err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", e.Name(), err)
		}
		if ac.Name == "" {
			ac.Name = strings.TrimSuffix(e.Name(), ".agent.yaml")
		}
		agents = append(agents, ac)
	}

	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Name < agents[j].Name
	})
	return agents, nil
}

// SaveAgent writes an agent config to <dir>/<name>.agent.yaml.
// It creates the directory if it does not exist.
func SaveAgent(dir string, agent AgentConfig) error {
	if err := ValidateAgentName(agent.Name); err != nil {
		return fmt.Errorf("save agent: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent dir: %w", err)
	}
	data, err := yaml.Marshal(&agent)
	if err != nil {
		return fmt.Errorf("marshal agent %q: %w", agent.Name, err)
	}
	path := filepath.Join(dir, AgentFileName(agent.Name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write agent %q: %w", agent.Name, err)
	}
	return nil
}

// DeleteAgent removes the agent config file <dir>/<name>.agent.yaml.
func DeleteAgent(dir string, name string) error {
	if err := ValidateAgentName(name); err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	path := filepath.Join(dir, AgentFileName(name))
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete agent %q: %w", name, err)
	}
	return nil
}
