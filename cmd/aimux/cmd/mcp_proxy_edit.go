package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

func newMCPProxyEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <server>",
		Short: "Edit a moved MCP server's definition",
		Long: `Edit a moved MCP server's definition in $EDITOR.

After a server is moved behind the proxy with 'aimux mcp proxy install',
its command, arguments, and environment variables live in ~/.aimux/mcp-servers.json.
Editing ~/.claude.json no longer changes the server.

This command opens the server's definition in $EDITOR (falling back to vi;
$EDITOR may carry arguments such as "code --wait", split as a shell would,
with the file passed last),
validates the changes (only command, args and env are allowed), probes the
server with the new definition in the environment launchd gives the proxy
(PATH and HOME from the installed plist plus the definition's env, nothing
else from your shell; a bare command is resolved on that PATH),
saves the definition and the tools it listed, and reloads that one server in
place. Any failure before the save leaves the old definition unchanged.

For a server not behind the proxy, run 'aimux mcp proxy install' first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return runEdit(home, args[0], cmd.OutOrStdout(), mcpproxy.DefaultEditor())
		},
	}
}

// runEdit edits server's definition under home: the port comes from the
// install manifest, and the probe runs the server with the environment the
// installed launchd agent gives it (its plist's PATH and HOME, plus the
// definition's env), resolving the command on that PATH.
func runEdit(home, server string, out io.Writer, editor func(string) error) error {
	aimuxDir := filepath.Join(home, ".aimux")
	manifestPath := filepath.Join(aimuxDir, "mcp-proxy-install.json")
	m, err := mcpproxy.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("MCP proxy is not installed; run `aimux mcp proxy install`")
	}
	base := editProbeEnv(home, m.Port, out)
	return mcpproxy.EditServer(context.Background(), mcpproxy.EditOptions{
		Server:       server,
		ServersPath:  filepath.Join(aimuxDir, "mcp-servers.json"),
		ManifestPath: manifestPath,
		CatalogPath:  filepath.Join(aimuxDir, "mcp-catalog.json"),
		BaseEnv:      base,
		Out:          out,
		Port:         m.Port,
		Editor:       editor,
		Probe: func(ctx context.Context, cfg mcpproxy.ServerConfig) ([]json.RawMessage, error) {
			return mcpproxy.ProbeServerDirect(ctx, cfg, 2*time.Minute, base)
		},
		Reload:  func() (mcpproxy.ReloadResult, error) { return mcpproxy.ReloadDaemon(m.Port) },
		Timeout: 2 * time.Minute,
	})
}

// editProbeEnv is the environment edit probes with: the EnvironmentVariables
// of the installed launchd plist (PATH and HOME as the daemon has them).
// Without a readable plist it falls back to the values install would write
// from this shell, and says so.
func editProbeEnv(home string, port int, out io.Writer) map[string]string {
	agent := proxyInstaller(home, port, out).Agent
	env, found, err := mcpproxy.ReadPlistEnv(agent.PlistPath)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(out, "Note: could not read %s (%v); probing with this shell's PATH and HOME.\n", agent.PlistPath, err)
	case !found:
		_, _ = fmt.Fprintf(out, "Note: no installed launchd plist at %s; probing with this shell's PATH and HOME.\n", agent.PlistPath)
	default:
		return env
	}
	return agent.Env
}
