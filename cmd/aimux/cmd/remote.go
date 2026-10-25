package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func tailscaleServeHint(port int, hosts []string) string {
	hint := fmt.Sprintf("Expose it on your tailnet (HTTPS, tailnet only):\n  tailscale serve --bg --https=8443 http://127.0.0.1:%d\n", port)
	if len(hosts) > 0 {
		hint += fmt.Sprintf("Then open the web client on your phone: https://%s:8443/app/\n", hosts[0])
	}
	return hint
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
			"tailscale serve forwarded it for a login in remote_access.allowed_logins.",
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
			})
			defer manager.Close()
			router := &acprun.Router{Lookup: acprun.ClaudeLookup(projects, registry), Manager: manager}
			broker = remoteapi.NewBroker(remoteapi.BrokerDeps{
				Check: router.Check, Send: router.Send, Cancel: manager.Cancel,
			}, remoteapi.BrokerOptions{ApprovalTimeout: s.approvalTimeout})
			claude := &provider.Claude{}
			index := &remoteapi.LocalIndex{
				ProjectsDir: projects, RegistryDir: registry, Broker: broker,
				Discover: history.Discover, ParseTrace: claude.ParseTrace, Owned: manager.Owns,
			}
			api := &remoteapi.API{Broker: broker, Sessions: index.Sessions, Session: index.Session}

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
				<-ctx.Done()
				shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shut)
			}()
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "aimux remote API on http://%s/v1 for %v via %v\n%s", ln.Addr(), s.allowed, hosts, tailscaleServeHint(s.port, hosts))
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
