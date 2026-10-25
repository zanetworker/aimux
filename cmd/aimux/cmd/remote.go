package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/search"
)

type remoteSettings struct {
	port            int
	allowed         []string
	adapter         string
	maxRuns         int
	approvalTimeout time.Duration
}

// resolveRemoteSettings applies flags over remote_access config over
// defaults. It refuses to run with nobody allowed.
func resolveRemoteSettings(cfg config.RemoteAccessConfig, port int, allow []string) (remoteSettings, error) {
	s := remoteSettings{port: 7382, adapter: defaultClaudeAdapter, maxRuns: 2, approvalTimeout: 10 * time.Minute, allowed: cfg.AllowedLogins}
	if cfg.Port > 0 {
		s.port = cfg.Port
	}
	if port > 0 {
		s.port = port
	}
	if len(allow) > 0 {
		s.allowed = allow
	}
	if cfg.Adapter != "" {
		s.adapter = cfg.Adapter
	}
	if cfg.MaxRuns > 0 {
		s.maxRuns = cfg.MaxRuns
	}
	if cfg.ApprovalTimeout != "" {
		d, err := time.ParseDuration(cfg.ApprovalTimeout)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("remote_access.approval_timeout %q is not a duration like 10m", cfg.ApprovalTimeout)
		}
		s.approvalTimeout = d
	}
	if len(s.allowed) == 0 {
		return s, errors.New("nobody is allowed: set remote_access.allowed_logins in ~/.aimux/config.yaml or pass --allow <tailscale-login>")
	}
	return s, nil
}

// resolveTrustedHosts returns the Host names the API accepts: configured
// names, or this machine's MagicDNS name from tailscale.
func resolveTrustedHosts(configured []string, detect func() (string, error)) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	name, err := detect()
	if err != nil || strings.TrimSuffix(name, ".") == "" {
		return nil, fmt.Errorf("cannot tell which Host names to trust: set remote_access.hostnames (your tailnet name, e.g. mac.tailnet.ts.net) or start tailscale (%v)", err)
	}
	return []string{strings.TrimSuffix(name, ".")}, nil
}

// tailscaleDNSName asks tailscale for this machine's MagicDNS name.
func tailscaleDNSName() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}
	return st.Self.DNSName, nil
}

func tailscaleServeHint(port int) string {
	return fmt.Sprintf("Expose it on your tailnet (HTTPS, tailnet only):\n  tailscale serve --bg --https=8443 http://127.0.0.1:%d\n", port)
}

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "API for your sessions from other devices, through tailscale serve",
	}
	cmd.AddCommand(newRemoteServeCmd())
	return cmd
}

func newRemoteServeCmd() *cobra.Command {
	var (
		port  int
		allow []string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the /v1 API on 127.0.0.1 for tailscale serve to expose",
		Long: "Serve the remote API: list Claude sessions, follow them, continue idle ones and answer " +
			"approvals from another device. It listens on 127.0.0.1 only and admits a request only when " +
			"tailscale serve forwarded it for a login in remote_access.allowed_logins. Its contract is " +
			"api/remote-v1.openapi.yaml, also served at /v1/openapi.yaml.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("read %s: %w", config.DefaultPath(), err)
			}
			s, err := resolveRemoteSettings(cfg.RemoteAccess, port, allow)
			if err != nil {
				return err
			}
			name, adapterArgs, err := parseAdapter(s.adapter)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			projects := filepath.Join(home, ".claude", "projects")
			registry := filepath.Join(home, ".claude", "sessions")

			var broker *remoteapi.Broker
			manager := acprun.NewManager(acprun.ManagerOptions{
				MaxRuns:     s.maxRuns,
				Fingerprint: acprun.ClaudeFingerprint(projects),
				Reserve:     sessionReservation(home),
				Start: func(ctx context.Context, id, cwd string) (acprun.Runner, error) {
					return acprun.Start(ctx, acprun.Spec{Command: name, Args: adapterArgs, Cwd: cwd}, id, broker.Approver())
				},
				StartNew: func(ctx context.Context, cwd string) (acprun.Runner, string, error) {
					r, err := acprun.StartNew(ctx, acprun.Spec{Command: name, Args: adapterArgs, Cwd: cwd}, broker.Approver())
					if err != nil {
						return nil, "", err
					}
					return r, r.SessionID(), nil
				},
			})
			defer manager.Close()
			router := &acprun.Router{Lookup: acprun.ClaudeLookup(projects, registry), Manager: manager}
			broker = remoteapi.NewBroker(remoteapi.BrokerDeps{
				Check: router.Check, Send: router.Send, Cancel: manager.Cancel,
			}, remoteapi.BrokerOptions{ApprovalTimeout: s.approvalTimeout})
			claude := &provider.Claude{}
			// Keyword ranking only (below), in an index of its own: without
			// embeddings, refreshing the shared index would mark changed
			// sessions as seen before the CLI and TUI embed them. One service
			// refreshes it in the background; a second one answers queries, so
			// a search reads the last committed index (SQLite WAL) instead of
			// waiting for a refresh that can take minutes.
			remoteIndex := filepath.Join(home, ".aimux", "remote-search.db")
			searcher := search.DefaultService(io.Discard)
			searcher.Embedder, searcher.DBPath = nil, remoteIndex
			reader := search.DefaultService(io.Discard)
			reader.Embedder, reader.DBPath = nil, remoteIndex
			index := &remoteapi.LocalIndex{
				ProjectsDir: projects, RegistryDir: registry, Broker: broker,
				Discover: history.Discover, ParseTrace: claude.ParseTrace, Owned: manager.Owns,
				Created: &remoteapi.CreatedStore{Path: filepath.Join(home, ".aimux", "remote-created.json")},
				Running: manager.Running,
				// Keyword ranking: every hit shows where the words matched, and a
				// query nothing contains finds nothing. The index is refreshed in
				// the background (below); refreshing per query takes seconds.
				SearchIndex: func(ctx context.Context, q string, limit int, ids []string) ([]search.Result, error) {
					rs, _, err := reader.Query(ctx, q, search.QueryOpts{Mode: search.ModeKeyword, Limit: limit, IncludeAutomated: true, NoRefresh: true, IDs: ids})
					return rs, err
				},
			}
			api := &remoteapi.API{Version: cmd.Root().Version, Broker: broker, Sessions: index.Sessions, Session: index.Session,
				Search: index.Search, Projects: index.Projects,
				Create: func(ctx context.Context, cwd, text string) (string, error) {
					id, err := manager.New(ctx, cwd)
					if err != nil {
						return "", err
					}
					if err := index.Created.Add(id, cwd, text); err != nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err) // the session works; it may only be hidden from the list
					}
					return id, nil
				},
			}

			hosts, err := resolveTrustedHosts(cfg.RemoteAccess.Hostnames, tailscaleDNSName)
			if err != nil {
				return err
			}
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
			if err != nil {
				return fmt.Errorf("listen on 127.0.0.1:%d: %w (choose another port with --port)", s.port, err)
			}
			srv := &http.Server{Handler: api.Handler(s.allowed, hosts), ReadHeaderTimeout: 10 * time.Second}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				t := time.NewTicker(time.Minute)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						manager.Reap()
					}
				}
			}()
			go func() {
				for {
					if err := searcher.Refresh(ctx); err != nil && ctx.Err() == nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err)
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Minute):
					}
				}
			}()
			go func() {
				<-ctx.Done()
				shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shut)
			}()
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "aimux remote API on http://%s/v1 for %v via %v\n%s", ln.Addr(), s.allowed, hosts, tailscaleServeHint(s.port))
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "Port on 127.0.0.1 (default 7382, or remote_access.port)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "Tailscale logins to admit (overrides remote_access.allowed_logins)")
	return cmd
}
