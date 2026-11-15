package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/provider"
)

const proxyAgentLabel = "com.aimux.mcp-proxy"

// proxyInstaller builds the installer for the owner's real setup under home.
// The launchd agent's Binary is left empty: install sets it, so a dry run
// or uninstall never needs a stable binary.
func proxyInstaller(home string, port int, out io.Writer) mcpproxy.Installer {
	aimuxDir := filepath.Join(home, ".aimux")
	claudeConfig := filepath.Join(home, ".claude.json")
	servers := filepath.Join(aimuxDir, "mcp-servers.json")
	catalog := filepath.Join(aimuxDir, "mcp-catalog.json")
	return mcpproxy.Installer{
		ClaudeConfig: claudeConfig,
		ManifestPath: filepath.Join(aimuxDir, "mcp-proxy-install.json"),
		BackupDir:    filepath.Join(aimuxDir, "backups"),
		ServersPath:  servers,
		CatalogPath:  catalog,
		Settings:     filepath.Join(home, ".claude", "settings.json"),
		Port:         port,
		Version:      rootCmd.Version,
		Agent: mcpproxy.Agent{
			Label:     proxyAgentLabel,
			PlistPath: filepath.Join(home, "Library", "LaunchAgents", proxyAgentLabel+".plist"),
			LogDir:    filepath.Join(aimuxDir, "logs", "mcp-proxy"),
			Args:      []string{"mcp", "proxy", "serve", "--port", fmt.Sprint(port)},
			Run:       runLaunchctl,
			UID:       os.Getuid(),
			// launchd starts the daemon without the shell's environment;
			// npx, uv and friends are found through this PATH. Never more:
			// the plist is world-readable.
			Env: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home},
		},
		Status: func() (mcpproxy.StatusReport, error) { return getProxyStatus(fmt.Sprintf("127.0.0.1:%d", port)) },
		Probe: func(servers []string) (mcpproxy.StatusReport, error) {
			return probeProxy(fmt.Sprintf("127.0.0.1:%d", port), servers)
		},
		Reload: func() (mcpproxy.ReloadResult, error) { return mcpproxy.ReloadDaemon(port) },
		Refresh: func(catalogPath string, names []string) map[string]error {
			return proxyRefreshFor(claudeConfig, servers, catalogPath, names, nil)
		},
		Sessions: func() ([]string, error) {
			return openSessionIDs(provider.LiveOf(provider.NewRegistry(home, config.Config{}).Local(), 0))
		},
		Out: out,
	}
}

// errInstallInterrupted: Ctrl-C or SIGTERM arrived while install listed
// tools; the servers it had started are stopped.
var errInstallInterrupted = errors.New("interrupted while listing tools; the servers started to list them were stopped")

func runLaunchctl(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput() // #nosec G204 -- name is always launchctl, args built by mcpproxy.Agent
}

// proxyRefreshFor lists the tools of names and saves them in the catalog at
// catalogPath (a temporary copy for a dry run), returning the failures by
// server. It reads the servers file and never writes it. While it lists,
// SIGINT and SIGTERM stop the servers it started (they run in sessions of
// their own, so the terminal's interrupt does not reach them); it returns
// once they are closed, with interrupted (when not nil) set and nothing
// saved.
func proxyRefreshFor(claudeConfig, serversFile, catalogPath string, names []string, interrupted *atomic.Bool) map[string]error {
	fail := func(err error) map[string]error {
		errs := map[string]error{}
		for _, n := range names {
			errs[n] = err
		}
		return errs
	}
	cfgs, resolved, err := proxyBackends(claudeConfig, serversFile, nil, false)
	if err != nil {
		return fail(err)
	}
	cat, err := mcpproxy.LoadCatalog(catalogPath)
	if err != nil {
		return fail(err)
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sigCtx, 3*time.Minute)
	defer cancel()
	errs := mcpproxy.RefreshCatalog(ctx, cat, mcpproxy.StdioStarter(cfgs, resolved...), names)
	if sigCtx.Err() != nil {
		if interrupted != nil {
			interrupted.Store(true)
		}
		return fail(errInstallInterrupted)
	}
	if err := cat.Save(catalogPath); err != nil {
		return fail(err)
	}
	return errs
}

// getProxyStatus reads the daemon's /status at addr; a refused connection
// is a report with Running false, not an error.
func getProxyStatus(addr string) (mcpproxy.StatusReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/status", nil)
	if err != nil {
		return mcpproxy.StatusReport{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return mcpproxy.StatusReport{}, nil
	}
	if err != nil {
		return mcpproxy.StatusReport{}, fmt.Errorf("ask the MCP proxy on %s for its status: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return mcpproxy.StatusReport{}, fmt.Errorf("MCP proxy on %s answered %s", addr, resp.Status)
	}
	var rep mcpproxy.StatusReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rep); err != nil {
		return mcpproxy.StatusReport{}, fmt.Errorf("read the MCP proxy's status from %s: %w", addr, err)
	}
	return rep, nil
}

// proxyProbeTimeout is how long the daemon may take to start and list the
// servers install moves.
const proxyProbeTimeout = 2 * time.Minute

// probeProxy asks the daemon at addr to start the named servers and list
// their tools (POST /status/probe).
func probeProxy(addr string, servers []string) (mcpproxy.StatusReport, error) {
	q := url.Values{"timeout": {proxyProbeTimeout.String()}, "server": servers}
	ctx, cancel := context.WithTimeout(context.Background(), proxyProbeTimeout+10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/status/probe?"+q.Encode(), nil)
	if err != nil {
		return mcpproxy.StatusReport{}, err
	}
	req.Header.Set(mcpproxy.ProbeHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mcpproxy.StatusReport{}, fmt.Errorf("probe the MCP proxy on %s: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return mcpproxy.StatusReport{}, fmt.Errorf("MCP proxy on %s answered %s to a probe", addr, resp.Status)
	}
	var rep mcpproxy.StatusReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rep); err != nil {
		return mcpproxy.StatusReport{}, fmt.Errorf("read the MCP proxy's probe answer from %s: %w", addr, err)
	}
	return rep, nil
}

// openSessionIDs is the sorted ids of the sessions open in a terminal,
// from every harness that records them.
func openSessionIDs(lives []harness.LiveSessions) ([]string, error) {
	seen := map[string]bool{}
	for _, l := range lives {
		hs, err := l.Holders(harness.List)
		if err != nil {
			return nil, err
		}
		for _, h := range hs {
			if h.SessionID != "" {
				seen[h.SessionID] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// printInstallSection prints whether the proxy is installed and in place.
// printInstallSection prints the install section and returns the state it
// printed, so the table can mark removed servers.
func printInstallSection(in mcpproxy.Installer, out io.Writer, color bool) mcpproxy.InstallState {
	st, err := in.InstallStatus()
	if err != nil {
		_, _ = fmt.Fprintf(out, "Install: unknown (%v)\n", err)
		return mcpproxy.InstallState{}
	}
	printInstallState(out, st, in.ClaudeConfig, in.UpgradeText(st.DaemonVersion), color)
	return st
}

func newMCPProxyInstallCmd() *cobra.Command {
	var (
		dryRun       bool
		skip, unskip []string
		port         int
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Move Claude Code's stdio MCP servers behind the proxy (edits mcpServers in ~/.claude.json)",
		Long: "Moves every stdio server in ~/.claude.json that is not behind the proxy yet (except --skip ones). It lists " +
			"each server's tools, saves its definition for the daemon, starts the proxy under launchd (or has the running " +
			"proxy reload in place) and probes the servers, backs up ~/.claude.json to ~/.aimux/backups, then replaces each " +
			"server's entry with an HTTP entry of the same name pointing at its own proxy endpoint, in one atomic edit. " +
			"Tool names stay the same, so permission rules keep working unchanged. In ~/.claude/settings.json it sets " +
			"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT and adds two hooks: one moves a server you add in a Claude Code session behind the proxy, " +
			"one names servers still direct when a session starts (uninstall removes both). " +
			"Run it again to move servers added later, and to take off the proxy any moved server you removed from ~/.claude.json (claude mcp remove); servers kept direct with --skip stay direct until --unskip. Moved servers are configured with `aimux mcp proxy edit`, not in " +
			"~/.claude.json. What changed is recorded in ~/.aimux/mcp-proxy-install.json; `uninstall` undoes it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			in := proxyInstaller(home, port, cmd.OutOrStdout())
			in.Unskip = unskip
			var interrupted atomic.Bool
			in.Refresh = func(catalogPath string, names []string) map[string]error {
				return proxyRefreshFor(in.ClaudeConfig, in.ServersPath, catalogPath, names, &interrupted)
			}
			if dryRun {
				p, err := in.DryRun(skip)
				if interrupted.Load() {
					return errInstallInterrupted
				}
				if err != nil {
					return err
				}
				in.PrintPlan(p)
				return nil
			}
			if in.Agent.Binary, err = stableBinary("aimux mcp proxy install"); err != nil {
				return err
			}
			in.HookBinary = in.Agent.Binary
			err = in.Install(skip)
			if interrupted.Load() {
				return fmt.Errorf("%w: %v", errInstallInterrupted, err)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would change (names only) and exit; servers missing from the catalog are started once to list their tools into a temporary catalog")
	cmd.Flags().StringSliceVar(&skip, "skip", nil, "Stdio servers to leave as direct entries (recorded: later runs keep them direct)")
	cmd.Flags().StringSliceVar(&unskip, "unskip", nil, "Servers an earlier --skip kept direct, to move this time")
	cmd.Flags().IntVar(&port, "port", 7381, "Port the proxy listens on at 127.0.0.1")
	return cmd
}

func newMCPProxyUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Give Claude Code its direct stdio MCP servers back and stop the proxy's launchd agent",
		Long: "Undoes what `install` recorded, in one edit of mcpServers: each moved entry that is still the proxy entry " +
			"install wrote gets its original bytes back; one you changed or removed since is kept and reported. Then it " +
			"removes the launchd agent. Without a record it leaves the Claude config alone and only removes a leftover launchd agent.",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return proxyInstaller(home, 7381, cmd.OutOrStdout()).Uninstall()
		},
	}
}

func newMCPProxyRestartCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the MCP proxy daemon, e.g. after upgrading aimux",
		Long: "Restarts the proxy's daemon in place (launchctl kickstart -k; the launchd job stays loaded and its plist " +
			"is not re-read) and waits until the new daemon answers, on the port the proxy was installed on unless " +
			"--port is given. Use it after upgrading aimux (status says when a restart is pending). Open Claude Code " +
			"sessions keep working: each server starts again on its next call.",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			port = restartPort(home, port, cmd.Flags().Changed("port"))
			return runProxyRestart(proxyInstaller(home, port, cmd.OutOrStdout()), cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&port, "port", 7381, "Port the proxy listens on at 127.0.0.1")
	return cmd
}

// restartPort is the port to wait on after a restart: the one the proxy
// was installed on (its manifest), unless --port was given.
func restartPort(home string, flagPort int, explicit bool) int {
	if explicit {
		return flagPort
	}
	if m, err := mcpproxy.LoadManifest(filepath.Join(home, ".aimux", "mcp-proxy-install.json")); err == nil && m != nil && m.Port > 0 {
		return m.Port
	}
	return flagPort
}

// runProxyRestart restarts the daemon and says which version now runs.
func runProxyRestart(in mcpproxy.Installer, out io.Writer) error {
	rep, err := in.Restart()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Restarted the MCP proxy: pid %d, aimux %s. Open sessions keep working; each server starts again on its next call.\n", rep.PID, rep.Version)
	if in.Version != "" && rep.Version != "" && rep.Version != in.Version {
		_, _ = fmt.Fprintf(out, "Note: the daemon runs aimux %s but this binary is %s: the launchd agent starts another aimux binary; run `aimux mcp proxy install` from this one to switch.\n", rep.Version, in.Version)
	}
	return nil
}
