package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

const proxyName = "mux" // what Claude Code registers the proxy as; tools appear as mcp__mux__<server>__<tool>

type proxyFlags struct {
	claudeConfig string
	servers      string
	catalog      string
	exclude      []string
	perSession   []string
	port         int
	idle         time.Duration
}

func (f *proxyFlags) bind(cmd *cobra.Command, serve, withPort bool) {
	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&f.claudeConfig, "claude-config", filepath.Join(home, ".claude.json"), "Claude Code config to read stdio MCP servers from")
	cmd.Flags().StringVar(&f.servers, "servers", filepath.Join(home, ".aimux", "mcp-servers.json"), "The proxy's saved backend definitions (written by refresh)")
	cmd.Flags().StringVar(&f.catalog, "catalog", filepath.Join(home, ".aimux", "mcp-catalog.json"), "Tool catalog file")
	cmd.Flags().StringSliceVar(&f.exclude, "exclude", nil, "MCP servers to leave out of the proxy")
	if withPort {
		cmd.Flags().IntVar(&f.port, "port", 7381, "Port on 127.0.0.1")
	}
	if serve {
		cmd.Flags().StringSliceVar(&f.perSession, "per-session", []string{"playwright", "chrome-devtools", "linkedin"},
			"Servers that get one process per client session instead of a shared one")
		cmd.Flags().DurationVar(&f.idle, "idle", 10*time.Minute, "Stop a backend after it has been unused this long")
	}
}

func newMCPProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Serve your stdio MCP servers lazily behind one shared endpoint",
		Long: "One MCP endpoint for all stdio MCP servers in ~/.claude.json. Tools are listed from a cached " +
			"catalog, each server starts on its first tool call, is shared by every session, and stops when idle. " +
			"Run `refresh` once, then `serve`, then add the entry printed by `config` to your Claude Code config.",
	}
	cmd.AddCommand(newMCPProxyRefreshCmd(), newMCPProxyServeCmd(), newMCPProxyConfigCmd())
	return cmd
}

func newMCPProxyRefreshCmd() *cobra.Command {
	var f proxyFlags
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Start each stdio MCP server once and cache its tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgs, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, true)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			return proxyRefresh(ctx, sortedKeys(cfgs), mcpproxy.StdioStarter(cfgs), f.catalog, cmd.OutOrStdout())
		},
	}
	f.bind(cmd, false, false)
	return cmd
}

// proxyRefresh refreshes the catalog for servers and reports each outcome.
func proxyRefresh(ctx context.Context, servers []string, start mcpproxy.Starter, catalogPath string, out io.Writer) error {
	if len(servers) == 0 {
		return errors.New("no stdio MCP servers to proxy in the Claude config")
	}
	cat, err := mcpproxy.LoadCatalog(catalogPath)
	if err != nil {
		return err
	}
	errs := mcpproxy.RefreshCatalog(ctx, cat, start, servers)
	if err := cat.Save(catalogPath); err != nil {
		return err
	}
	for _, s := range servers {
		if e := errs[s]; e != nil {
			_, _ = fmt.Fprintf(out, "  %-28s failed: %v\n", s, e)
			continue
		}
		_, _ = fmt.Fprintf(out, "  %-28s %d tools\n", s, len(cat.Servers[s].Tools))
	}
	_, _ = fmt.Fprintf(out, "Catalog: %s (%d of %d servers refreshed)\n", catalogPath, len(servers)-len(errs), len(servers))
	if len(errs) == len(servers) {
		return errors.New("no server could be refreshed; see the failures above")
	}
	return nil
}

// proxyCatalogProblem explains why serve cannot run with this catalog, or
// returns "" when it has tools for configured servers.
func proxyCatalogProblem(catalogPath string, servers int) string {
	if servers > 0 {
		return ""
	}
	if _, err := os.Stat(catalogPath); err != nil {
		return fmt.Sprintf("no catalog at %s; run `aimux mcp proxy refresh` first", catalogPath)
	}
	return fmt.Sprintf("catalog %s has no tools for the configured servers; check the failures `aimux mcp proxy refresh` reports", catalogPath)
}

func newMCPProxyServeCmd() *cobra.Command {
	var f proxyFlags
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the shared MCP proxy on 127.0.0.1",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgs, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, false)
			if err != nil {
				return err
			}
			cat, err := mcpproxy.LoadCatalog(f.catalog)
			if err != nil {
				return err
			}
			// Serve only configured servers, but keep the full catalog for the
			// updater, which saves it: an --exclude for this run must not drop
			// a server's tools from the file.
			configured := func(s string) bool { _, ok := cfgs[s]; return ok }
			served := cat.Only(configured)
			if msg := proxyCatalogProblem(f.catalog, len(served.Servers)); msg != "" {
				return errors.New(msg)
			}
			perSession := map[string]bool{}
			for _, s := range f.perSession {
				perSession[s] = true
			}
			updater := mcpproxy.NewCatalogUpdater(cat, f.catalog)
			pool := mcpproxy.NewPool(mcpproxy.StdioStarter(cfgs), mcpproxy.PoolOptions{
				PerSession: perSession,
				// Keep the catalog current: re-list tools whenever a backend starts.
				OnStart: func(server string, b mcpproxy.Backend) {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := updater.Refresh(ctx, server, b); err != nil {
							_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err)
						}
					}()
				},
			})
			srv, skipped := mcpproxy.NewServer(updater.Snapshot().Only(configured), pool, proxyName)
			for _, s := range skipped {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s\n", s)
			}
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.port))
			if err != nil {
				return fmt.Errorf("listen on 127.0.0.1:%d: %w (choose another port with --port)", f.port, err)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			tools := 0
			for _, e := range served.Servers {
				tools += len(e.Tools)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP proxy: %d tools from %d servers at http://%s/mcp (idle stop after %v)\n",
				tools-len(skipped), len(served.Servers), ln.Addr(), f.idle)
			return mcpproxy.Serve(ctx, ln, srv, pool, mcpproxy.DaemonOptions{IdleTimeout: f.idle})
		},
	}
	f.bind(cmd, true, true)
	return cmd
}

func newMCPProxyConfigCmd() *cobra.Command {
	var f proxyFlags
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the Claude Code entry for the proxy (does not edit any file)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgs, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, false)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), proxyClaudeConfig(sortedKeys(cfgs), f.port, f.servers))
			return nil
		},
	}
	f.bind(cmd, false, true)
	return cmd
}

// proxyBackends returns the stdio servers the proxy runs: those saved by an
// earlier refresh, overlaid with the ones still in the Claude config, minus
// excluded names. With save, that set is written back, so removing the stdio
// entries from the Claude config afterwards loses nothing.
func proxyBackends(claudeConfig, serversFile string, exclude []string, save bool) (map[string]mcpproxy.ServerConfig, error) {
	saved, err := mcpproxy.LoadServers(serversFile)
	if err != nil {
		return nil, err
	}
	fromClaude, err := mcpproxy.LoadClaudeServers(claudeConfig, nil)
	if err != nil && len(saved) == 0 {
		return nil, err // with nothing saved, the Claude config is required
	}
	merged := mcpproxy.MergeServers(saved, fromClaude)
	for _, name := range exclude {
		delete(merged, name)
	}
	if save {
		if err := mcpproxy.SaveServers(serversFile, merged); err != nil {
			return nil, err
		}
	}
	return merged, nil
}

// proxyClaudeConfig explains how to switch Claude Code to the proxy.
func proxyClaudeConfig(replaced []string, port int, serversFile string) string {
	entry := map[string]any{"mcpServers": map[string]any{
		proxyName: map[string]string{"type": "http", "url": fmt.Sprintf("http://127.0.0.1:%d/mcp", port)},
	}}
	data, _ := json.MarshalIndent(entry, "", "  ")
	sorted := append([]string(nil), replaced...)
	sort.Strings(sorted)
	return fmt.Sprintf("Add this entry to mcpServers in your Claude Code config:\n\n%s\n\n"+
		"It replaces these stdio entries: %s\n\n"+
		"Their definitions are saved in %s by `aimux mcp proxy refresh`; once refresh has run,\n"+
		"you can remove them from the Claude config and the proxy keeps serving them.\n", data, strings.Join(sorted, ", "), serversFile)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
