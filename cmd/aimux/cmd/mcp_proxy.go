package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

type proxyFlags struct {
	claudeConfig string
	servers      string
	catalog      string
	exclude      []string
	perSession   []string
	only         []string
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
		cmd.Flags().StringSliceVar(&f.only, "only", nil, "Serve only these servers (default: the servers install moved, else every configured one)")
	}
}

func newMCPProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Serve your stdio MCP servers lazily from one shared process",
		Long: "One shared process for all stdio MCP servers in ~/.claude.json, each at its own endpoint under its own name. Tools are listed from a cached " +
			"catalog, each server starts on its first tool call, is shared by every session, and stops when idle. " +
			"Run `install` to switch Claude Code to it (and `uninstall` to switch back), or run `refresh` and `serve` " +
			"yourself and add the entries printed by `config`.",
	}
	cmd.AddCommand(newMCPProxyRefreshCmd(), newMCPProxyServeCmd(), newMCPProxyConfigCmd(), newMCPProxyStatusCmd(),
		newMCPProxyInstallCmd(), newMCPProxyUninstallCmd(), newMCPProxyEditCmd())
	return cmd
}

func newMCPProxyRefreshCmd() *cobra.Command {
	var f proxyFlags
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Start each stdio MCP server once and cache its tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgs, resolved, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, true)
			if err != nil {
				return err
			}
			// The servers run in sessions of their own, so Ctrl-C reaches only
			// aimux: catch it (and SIGTERM) and let the refresh close them.
			sigCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(sigCtx, 3*time.Minute)
			defer cancel()
			err = proxyRefresh(ctx, sortedKeys(cfgs), mcpproxy.StdioStarter(cfgs, resolved...), f.catalog, cmd.OutOrStdout())
			if sigCtx.Err() != nil {
				return errors.New("refresh interrupted; the servers it started were stopped")
			}
			return err
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
// returns "" when it has tools for configured servers. An installed daemon
// (installed: the servers file has a moved list) may serve nothing for now.
func proxyCatalogProblem(catalogPath string, servers int, installed bool) string {
	if servers > 0 || installed {
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
			set, missing, err := proxyLoadSet(f)
			if err != nil {
				return err
			}
			for _, s := range missing {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "--only %s: no such server is configured\n", s)
			}
			// Serve only configured servers, but keep the full catalog for the
			// updater, which saves it: an --exclude for this run must not drop
			// a server's tools from the file.
			configured := func(s string) bool { _, ok := set.Configs[s]; return ok }
			served := set.Catalog.Only(configured)
			if msg := proxyCatalogProblem(f.catalog, len(served.Servers), set.Resolved != nil); msg != "" {
				return errors.New(msg)
			}
			perSession := map[string]bool{}
			for _, s := range f.perSession {
				perSession[s] = true
			}
			updater := mcpproxy.NewCatalogUpdater(set.Catalog, f.catalog)
			// A backend's startup refresh is dropped once an edit or reload
			// replaced the definition it was started with.
			updater.Current = proxyCurrentDefinition(f)
			starter := mcpproxy.NewLiveStarter(set.Configs, set.Resolved)
			pool := mcpproxy.NewPool(starter.Start, mcpproxy.PoolOptions{
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
			registered := map[string]string{}
			for name, e := range served.Servers {
				registered[name] = mcpproxy.ToolsFingerprint(e.Tools)
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
			mon := &mcpproxy.Monitor{
				Pool: pool, Servers: sortedKeys(set.Configs), Tools: updater.ToolCount, Registered: registered,
				PID: os.Getpid(), StartedAt: time.Now(), Port: ln.Addr().(*net.TCPAddr).Port, Version: cmd.Root().Version,
			}
			reload := &mcpproxy.ReloadOptions{
				Current: set,
				Load: func() (mcpproxy.ServerSet, error) {
					next, _, err := proxyLoadSet(f)
					return next, err
				},
				// New definitions and the re-read catalog take effect before the
				// daemon closes a changed server's backends.
				Apply: func(next mcpproxy.ServerSet) {
					starter.Set(next.Configs, next.Resolved)
					updater.Replace(next.Catalog)
				},
			}
			skipped := mcpproxy.SkippedTools(served)
			for _, s := range skipped {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s\n", s)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP proxy: %d tools from %d servers at http://%s/mcp/<server> (idle stop after %v)\n",
				tools-len(skipped), len(served.Servers), ln.Addr(), f.idle)
			return mcpproxy.Serve(ctx, ln, mcpproxy.NewServers(served, pool), pool,
				mcpproxy.DaemonOptions{IdleTimeout: f.idle, Status: mon, Reload: reload, Log: cmd.ErrOrStderr()})
		},
	}
	f.bind(cmd, true, true)
	return cmd
}

func newMCPProxyStatusCmd() *cobra.Command {
	var o proxyStatusOpts
	var port int
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the running proxy's servers: state, tools, last call, last error",
		Long: "Says which servers `install` moved, any whose entry drifted from its proxy URL, stdio servers not behind " +
			"the proxy yet and whether an upgrade restart is pending, then reads the running proxy's /status " +
			"on 127.0.0.1. With --probe the proxy starts each configured server once and lists its tools before " +
			"reporting. Not running is reported, not an error.",
		RunE: func(cmd *cobra.Command, args []string) error {
			o.addr = fmt.Sprintf("127.0.0.1:%d", port)
			if home, err := os.UserHomeDir(); err == nil {
				in := proxyInstaller(home, port, cmd.OutOrStdout())
				if o.json {
					if st, err := in.InstallStatus(); err == nil {
						o.install = &st
					}
				} else {
					printInstallSection(in, cmd.OutOrStdout())
				}
			}
			return proxyStatus(cmd.Context(), o, cmd.OutOrStdout(), time.Now())
		},
	}
	cmd.Flags().IntVar(&port, "port", 7381, "Port the proxy listens on at 127.0.0.1")
	cmd.Flags().BoolVar(&o.json, "json", false, "Print the status as JSON")
	cmd.Flags().BoolVar(&o.probe, "probe", false, "Start each configured server once and list its tools first")
	cmd.Flags().DurationVar(&o.timeout, "timeout", time.Minute, "How long --probe may take")
	return cmd
}

type proxyStatusOpts struct {
	addr    string // host:port of the proxy
	json    bool
	probe   bool
	timeout time.Duration          // for probe; default one minute
	install *mcpproxy.InstallState // added to --json output when set
}

// proxyStatusJSON is the --json answer: the daemon's report plus the install state.
type proxyStatusJSON struct {
	mcpproxy.StatusReport
	Install *mcpproxy.InstallState `json:"install,omitempty"`
}

// proxyStatus asks the proxy at o.addr for its status and prints it.
func proxyStatus(ctx context.Context, o proxyStatusOpts, out io.Writer, now time.Time) error {
	if o.timeout <= 0 {
		o.timeout = time.Minute
	}
	method, target, wait := http.MethodGet, "http://"+o.addr+"/status", 5*time.Second
	if o.probe {
		method = http.MethodPost
		target = "http://" + o.addr + "/status/probe?timeout=" + url.QueryEscape(o.timeout.String())
		wait = o.timeout + 10*time.Second // the daemon answers at its timeout
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return err
	}
	if o.probe {
		req.Header.Set(mcpproxy.ProbeHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		if o.json {
			b, err := json.Marshal(struct {
				Running bool                   `json:"running"`
				Install *mcpproxy.InstallState `json:"install,omitempty"`
			}{false, o.install})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(out, string(b))
			return nil
		}
		_, _ = fmt.Fprintf(out, "MCP proxy is not running on %s\n", o.addr)
		return nil
	}
	if err != nil {
		return fmt.Errorf("ask the MCP proxy on %s for its status: %w", o.addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MCP proxy on %s answered %s", o.addr, resp.Status)
	}
	var rep mcpproxy.StatusReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rep); err != nil {
		return fmt.Errorf("read the MCP proxy's status from %s: %w", o.addr, err)
	}
	if o.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(proxyStatusJSON{rep, o.install})
	}
	printProxyStatus(out, o.addr, rep, now)
	return nil
}

// printProxyStatus prints the status table, then the stderr tail of each
// failed server.
func printProxyStatus(out io.Writer, addr string, rep mcpproxy.StatusReport, now time.Time) {
	_, _ = fmt.Fprintf(out, "MCP proxy on %s (pid %d, started %s)\n\n", addr, rep.PID, mcpproxy.Ago(now, rep.StartedAt))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SERVER\tSTATE\tTOOLS\tLAST CALL\tLAST ERROR")
	for _, s := range rep.Servers {
		lastErr := s.LastError
		if s.ProbeError != "" {
			lastErr = s.ProbeError
		}
		if lastErr == "" {
			lastErr = "-"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", printable(s.Server), s.State, s.Tools,
			mcpproxy.Ago(now, s.LastCall), printable(lastErr))
	}
	_ = w.Flush()
	for _, s := range rep.Servers {
		if s.State != mcpproxy.StateFailed || len(s.StderrTail) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(out, "\n%s stderr:\n", printable(s.Server))
		for _, line := range s.StderrTail {
			_, _ = fmt.Fprintf(out, "  %s\n", printable(line))
		}
	}
}

// printable replaces control characters, so a server's output cannot move
// the cursor or recolour the owner's terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

func newMCPProxyConfigCmd() *cobra.Command {
	var f proxyFlags
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the Claude Code entries for the proxy (does not edit any file)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgs, _, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, false)
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
// excluded names, and the ones whose saved definition install already
// resolved (start them without expanding again). With save, that set is
// written back, so removing the stdio entries from the Claude config
// afterwards loses nothing.
func proxyBackends(claudeConfig, serversFile string, exclude []string, save bool) (map[string]mcpproxy.ServerConfig, []string, error) {
	saved, resolved, err := mcpproxy.LoadServerFile(serversFile)
	if err != nil {
		return nil, nil, err
	}
	fromClaude, err := mcpproxy.LoadClaudeServers(claudeConfig, nil)
	if err != nil && len(saved) == 0 {
		return nil, nil, err // with nothing saved, the Claude config is required
	}
	// Servers install moved keep the definitions it resolved: under launchd
	// their ${VAR}s would expand to nothing.
	merged := mcpproxy.MergeServers(saved, fromClaude, resolved...)
	for _, name := range exclude {
		delete(merged, name)
	}
	if save {
		if err := mcpproxy.SaveServers(serversFile, merged); err != nil {
			return nil, nil, err
		}
	}
	var kept []string
	if resolved != nil {
		kept = []string{} // an empty moved list stays a list
	}
	for _, r := range resolved {
		if _, ok := merged[r]; ok {
			if _, ok := saved[r]; ok {
				kept = append(kept, r)
			}
		}
	}
	return merged, kept, nil
}

// proxyLoadSet reads what serve serves: the backends (proxyBackends under
// --exclude, then --only) and the whole catalog. Without --only, a servers
// file that lists the servers install moved is served as exactly those
// (none for an empty list), so install adds one by saving it and calling
// /reload; without a list every configured server is served. Serve calls it at start and on every
// POST /reload. It also returns the --only names that are not configured.
func proxyLoadSet(f proxyFlags) (mcpproxy.ServerSet, []string, error) {
	cfgs, resolved, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, false)
	if err != nil {
		return mcpproxy.ServerSet{}, nil, err
	}
	var missing []string
	switch {
	case len(f.only) > 0:
		cfgs, missing = proxyOnly(cfgs, f.only)
	case resolved != nil: // installed: exactly the moved list, empty or not
		moved := map[string]mcpproxy.ServerConfig{}
		for _, r := range resolved {
			moved[r] = cfgs[r]
		}
		cfgs = moved
	}
	cat, err := mcpproxy.LoadCatalog(f.catalog)
	if err != nil {
		return mcpproxy.ServerSet{}, nil, err
	}
	return mcpproxy.ServerSet{Configs: cfgs, Resolved: resolved, Catalog: cat}, missing, nil
}

// proxyCurrentDefinition returns a server's definition as a reload would
// load it now (proxyBackends); false when it cannot be read or the server
// is not configured.
func proxyCurrentDefinition(f proxyFlags) func(string) (mcpproxy.ServerConfig, bool) {
	return func(server string) (mcpproxy.ServerConfig, bool) {
		cfgs, _, err := proxyBackends(f.claudeConfig, f.servers, f.exclude, false)
		if err != nil {
			return mcpproxy.ServerConfig{}, false
		}
		c, ok := cfgs[server]
		return c, ok
	}
}

// proxyOnly keeps the servers named in only (all of them when only is
// empty) and returns the named ones that are not configured.
func proxyOnly(cfgs map[string]mcpproxy.ServerConfig, only []string) (map[string]mcpproxy.ServerConfig, []string) {
	if len(only) == 0 {
		return cfgs, nil
	}
	out := map[string]mcpproxy.ServerConfig{}
	var missing []string
	for _, s := range only {
		if c, ok := cfgs[s]; ok {
			out[s] = c
		} else {
			missing = append(missing, s)
		}
	}
	return out, missing
}

// proxyClaudeConfig explains how to switch Claude Code to the proxy: each
// replaced stdio entry becomes an HTTP entry of the same name, so tool
// names and permission rules stay as they are.
func proxyClaudeConfig(replaced []string, port int, serversFile string) string {
	entries := map[string]any{}
	for _, s := range replaced {
		entries[s] = map[string]string{"type": "http", "url": fmt.Sprintf("http://127.0.0.1:%d%s", port, mcpproxy.EndpointPath(s))}
	}
	data, _ := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	sorted := append([]string(nil), replaced...)
	sort.Strings(sorted)
	return fmt.Sprintf("Replace these entries in mcpServers in your Claude Code config:\n\n%s\n\n"+
		"Each replaces the stdio entry of the same name: %s\n\n"+
		"Their definitions are saved in %s by `aimux mcp proxy refresh`; once refresh has run,\n"+
		"the proxy keeps serving them without the stdio entries.\n", data, strings.Join(sorted, ", "), serversFile)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
