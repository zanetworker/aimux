package mcpproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// SaveServers writes the backend definitions the proxy runs, so they survive
// removing the stdio entries from the Claude config. The file is owner-only:
// env values often hold tokens.
func SaveServers(path string, servers map[string]ServerConfig) error {
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MCP servers: %w", err)
	}
	if err := writePrivateFile(path, data); err != nil {
		return fmt.Errorf("write MCP servers %s: %w", path, err)
	}
	return nil
}

// LoadServers reads a file written by SaveServers. A missing file is empty.
func LoadServers(path string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- aimux's own state file
	if errors.Is(err, os.ErrNotExist) {
		return map[string]ServerConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read MCP servers %s: %w", path, err)
	}
	var f struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse MCP servers %s: %w", path, err)
	}
	if f.MCPServers == nil {
		f.MCPServers = map[string]ServerConfig{}
	}
	return f.MCPServers, nil
}

// MergeServers returns saved overlaid with fromClaude: an entry still in the
// Claude config wins, and one already moved out of it is kept.
func MergeServers(saved, fromClaude map[string]ServerConfig) map[string]ServerConfig {
	out := make(map[string]ServerConfig, len(saved)+len(fromClaude))
	for k, v := range saved {
		out[k] = v
	}
	for k, v := range fromClaude {
		out[k] = v
	}
	return out
}

// claudeVar matches the variable forms Claude Code expands in MCP server
// definitions: ${VAR} and ${VAR:-default}.
var claudeVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func expandValue(s string) string {
	return claudeVar.ReplaceAllStringFunc(s, func(m string) string {
		parts := claudeVar.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(parts[1]); ok && v != "" {
			return v
		}
		return parts[3] // the default, or "" for an unset variable without one
	})
}

// expandConfig resolves ${VAR} and ${VAR:-default} in a server's command,
// arguments and env, as Claude Code does, without changing cfg. The saved
// definitions keep the variables, so a changed value applies on next start.
func expandConfig(cfg ServerConfig) ServerConfig {
	out := ServerConfig{Command: expandValue(cfg.Command)}
	for _, a := range cfg.Args {
		out.Args = append(out.Args, expandValue(a))
	}
	if cfg.Env != nil {
		out.Env = make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			out.Env[k] = expandValue(v)
		}
	}
	return out
}
