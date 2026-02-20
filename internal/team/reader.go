package team

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Member represents a single agent in a team.
type Member struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	AgentType string `json:"agentType"`
	Model     string `json:"model"`
}

// TeamConfig represents a Claude Code team configuration.
type TeamConfig struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []Member `json:"members"`
}

// ReadTeamConfig reads and parses a team config from the given file path.
func ReadTeamConfig(path string) (TeamConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TeamConfig{}, fmt.Errorf("reading team config %s: %w", path, err)
	}

	var cfg TeamConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return TeamConfig{}, fmt.Errorf("parsing team config %s: %w", path, err)
	}
	return cfg, nil
}

// ListTeams reads all team configs from subdirectories of teamsDir.
// Each subdirectory is expected to contain a config.json file.
func ListTeams(teamsDir string) ([]TeamConfig, error) {
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return nil, fmt.Errorf("listing teams directory %s: %w", teamsDir, err)
	}

	var teams []TeamConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cfgPath := filepath.Join(teamsDir, entry.Name(), "config.json")
		cfg, err := ReadTeamConfig(cfgPath)
		if err != nil {
			// Skip directories without a valid config.
			continue
		}
		teams = append(teams, cfg)
	}
	return teams, nil
}

// ListTeamsDefault reads all team configs from ~/.claude/teams/.
func ListTeamsDefault() ([]TeamConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}
	return ListTeams(filepath.Join(home, ".claude", "teams"))
}
