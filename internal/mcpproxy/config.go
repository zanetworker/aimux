package mcpproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type claudeServerEntry struct {
	Type string `json:"type"`
	ServerConfig
}

// LoadClaudeServers returns the stdio MCP servers in a Claude Code config
// (~/.claude.json), minus excluded names and any entry that runs this proxy.
// Remote (http, sse) servers are left out: they cost no local processes, so
// clients keep connecting to them directly.
func LoadClaudeServers(path string, exclude []string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- user's own Claude config
	if err != nil {
		return nil, fmt.Errorf("read Claude config %s: %w", path, err)
	}
	var cfg struct {
		MCPServers map[string]claudeServerEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse Claude config %s: %w", path, err)
	}
	out := map[string]ServerConfig{}
	for name, e := range cfg.MCPServers {
		if (e.Type != "" && e.Type != "stdio") || e.Command == "" || slices.Contains(exclude, name) || isProxyCommand(e.ServerConfig) {
			continue
		}
		out[name] = e.ServerConfig
	}
	return out, nil
}

func isProxyCommand(c ServerConfig) bool {
	return filepath.Base(c.Command) == "aimux" && len(c.Args) >= 2 && c.Args[0] == "mcp" && c.Args[1] == "proxy"
}
