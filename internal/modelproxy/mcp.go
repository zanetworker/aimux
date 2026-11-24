package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// List from an empty directory, as app-server runs: a project config in the caller's directory would add
	// servers app-server does not define, and switching those off makes Codex reject the whole config.
	path, err := exec.LookPath(command)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		return nil, fmt.Errorf("find Codex command: %w", err)
	}
	dir, err := os.MkdirTemp("", "aimux-codex-mcp-")
	if err != nil {
		return nil, fmt.Errorf("create directory to list Codex MCP servers: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cmd := exec.CommandContext(ctx, path, "mcp", "list", "--disable", "plugins", "--json") // #nosec G204 -- the owner-configured Codex command, fixed args
	cmd.Dir = dir
	out, err := cmd.Output()
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
