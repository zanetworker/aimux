package config

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// AgentConfig defines a named agent configuration loaded from agents.yaml.
type AgentConfig struct {
	Name      string   `yaml:"name"`
	Runtime   string   `yaml:"runtime"`
	Inference string   `yaml:"inference,omitempty"`
	Model     string   `yaml:"model,omitempty"`
	Prompt    string   `yaml:"prompt,omitempty"`
	MCP       []string `yaml:"mcp,omitempty"`
	Skills    []string `yaml:"skills,omitempty"`
	Policy    string   `yaml:"policy,omitempty"`
}

// LoadAgents reads agent configs from a YAML file.
// Returns nil, nil if the file does not exist.
func LoadAgents(path string) ([]AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents config: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var agents []AgentConfig
	if err := yaml.Unmarshal(data, &agents); err != nil {
		return nil, fmt.Errorf("parse agents config %s: %w", path, err)
	}
	return agents, nil
}

// LoadAgentsMerged loads agents from global and project-local files,
// merging them with project-local configs overriding global ones by name.
func LoadAgentsMerged(globalPath, projectPath string) []AgentConfig {
	global, _ := LoadAgents(globalPath)
	project, _ := LoadAgents(projectPath)

	byName := make(map[string]AgentConfig, len(global)+len(project))
	for _, a := range global {
		byName[a.Name] = a
	}
	for _, a := range project {
		byName[a.Name] = a
	}

	result := make([]AgentConfig, 0, len(byName))
	for _, a := range byName {
		result = append(result, a)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// AgentConfigNames returns sorted names from a list of agent configs.
func AgentConfigNames(agents []AgentConfig) []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	sort.Strings(names)
	return names
}
