package controller

import (
	"fmt"

	"github.com/zanetworker/aimux/internal/config"
)

// CreateAgent validates and saves a new agent config to the given directory.
func CreateAgent(dir string, cfg config.AgentConfig) error {
	if err := config.ValidateAgentName(cfg.Name); err != nil {
		return fmt.Errorf("create agent %q: %w", cfg.Name, err)
	}
	return config.SaveAgent(dir, cfg)
}

// UpdateAgent loads an existing agent, merges non-zero fields from updates, and saves.
func UpdateAgent(dir string, name string, updates config.AgentConfig) error {
	if err := config.ValidateAgentName(name); err != nil {
		return fmt.Errorf("update agent %q: %w", name, err)
	}
	agents, err := config.LoadAgentDir(dir)
	if err != nil {
		return fmt.Errorf("update agent %q: %w", name, err)
	}

	var found *config.AgentConfig
	for i := range agents {
		if agents[i].Name == name {
			found = &agents[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("update agent %q: not found", name)
	}

	if updates.Harness != "" {
		found.Harness = updates.Harness
	}
	if updates.Inference != "" {
		found.Inference = updates.Inference
	}
	if updates.Model != "" {
		found.Model = updates.Model
	}
	if updates.Prompt != "" {
		found.Prompt = updates.Prompt
	}
	if len(updates.MCP) > 0 {
		found.MCP = updates.MCP
	}
	if len(updates.Skills) > 0 {
		found.Skills = updates.Skills
	}
	if updates.Policy != "" {
		found.Policy = updates.Policy
	}

	return config.SaveAgent(dir, *found)
}

// RemoveAgent deletes an agent config file from the given directory.
func RemoveAgent(dir string, name string) error {
	if err := config.ValidateAgentName(name); err != nil {
		return fmt.Errorf("remove agent %q: %w", name, err)
	}
	return config.DeleteAgent(dir, name)
}

// ListAgents loads agent configs from global and project directories,
// merging them with project-local configs overriding global ones by name.
func ListAgents(globalDir, projectDir string) ([]config.AgentConfig, error) {
	global, err := config.LoadAgentDir(globalDir)
	if err != nil {
		return nil, fmt.Errorf("list agents (global): %w", err)
	}
	project, err := config.LoadAgentDir(projectDir)
	if err != nil {
		return nil, fmt.Errorf("list agents (project): %w", err)
	}

	byName := make(map[string]config.AgentConfig, len(global)+len(project))
	for _, a := range global {
		byName[a.Name] = a
	}
	for _, a := range project {
		byName[a.Name] = a
	}

	result := make([]config.AgentConfig, 0, len(byName))
	for _, a := range byName {
		result = append(result, a)
	}
	// Sort by name for stable output
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Name < result[i].Name {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result, nil
}
