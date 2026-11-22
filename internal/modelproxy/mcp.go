package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// disabledMCPArgs switches off every MCP server Codex's config defines, as -c flags for app-server. Servers are listed
// with plugins disabled, as app-server runs: a -c entry for a plugin's server makes Codex reject the whole config. Without them a configured server would offer its tools to the model, and Codex would run them
// itself, outside Claude Code's permissions and hooks. When the list cannot be read the caller must not start Codex.
func disabledMCPArgs(ctx context.Context, command string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "mcp", "list", "--disable", "plugins", "--json").Output() // #nosec G204 -- the owner-configured Codex command, fixed args
	if err != nil {
		detail := ""
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			detail = ": " + strings.TrimSpace(string(exit.Stderr))
		}
		return nil, fmt.Errorf("list Codex MCP servers with %q mcp list --disable plugins --json: %w%s", command, err, detail)
	}
	var servers []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &servers); err != nil {
		return nil, fmt.Errorf("decode Codex MCP servers: %w", err)
	}
	args := make([]string, 0, 2*len(servers))
	for _, s := range servers {
		key := s.Name
		if !bareTOMLKey.MatchString(key) {
			key = strconv.Quote(key)
		}
		args = append(args, "-c", "mcp_servers."+key+".enabled=false")
	}
	return args, nil
}
