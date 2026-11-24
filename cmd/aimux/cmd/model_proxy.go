package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
)

func newModelCmd() *cobra.Command {
	model := &cobra.Command{Use: "model", Short: "Work with model APIs"}
	proxy := &cobra.Command{Use: "proxy", Short: "Expose a local Anthropic Messages API"}
	proxy.AddCommand(newModelProxyServeCmd(), newModelProxyStatusCmd(), newModelProxyStatsCmd())
	model.AddCommand(proxy)
	return model
}

func newModelProxyStatusCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the model proxy is answering, and how its last turn and Codex check went",
		Long: "Reads the running proxy's /health on 127.0.0.1 and prints: the proxy's own state and uptime, whether " +
			"its Codex app-server is running, how many turns it has sent to Codex and how the last one went, and the " +
			"same for the rate-limit checks behind the HUD's usage file (they cost no tokens, so a recent one proves " +
			"the Codex login still works). Nothing here starts Codex or spends tokens. Exits non-zero when the proxy " +
			"is degraded or not running, so a monitor can act on the exit code.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			key, err := keyring.Stored(modelproxy.KeyService).Lookup(cmd.Context())
			if err != nil {
				if errors.Is(err, keyring.ErrNotFound) {
					return fmt.Errorf("no proxy key in the %s secret-store entry yet; start the proxy once with: aimux model proxy serve", modelproxy.KeyService)
				}
				return fmt.Errorf("read the model proxy key: %w", err)
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			return modelProxyStatus(cmd.Context(), addr, key, cmd.OutOrStdout(), time.Now)
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Port the proxy listens on at 127.0.0.1")
	return cmd
}

func newModelProxyStatsCmd() *cobra.Command {
	var port int
	var since time.Duration
	var session string
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Summarise the proxy's recent requests: how each kind went and how long it took",
		Long: "Reads the request history the proxy writes (one JSON line per answered request, no prompts or " +
			"answers) and prints a row per outcome, slowest first: how many, the mean and 95th percentile time, " +
			"the average prompt size and how many failed. Fallbacks also print what they say about themselves, " +
			"which is where the next fix usually hides. When the window holds more than one conversation it also " +
			"lists them, costliest first, by a short one-way tag; --session takes a session id (hashed to its " +
			"tag) or the tag itself. Reads files only, so the proxy need not be running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find home folder for the request log: %w", err)
			}
			return modelProxyStats(modelproxy.RequestLogFile(home, port), since, session, cmd.OutOrStdout(), time.Now)
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Which listener's history to read")
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "How far back to summarise, such as 2h or 168h")
	cmd.Flags().StringVar(&session, "session", "", "Only one conversation: a Claude Code session id, or the tag the history shows")
	return cmd
}

func newModelProxyServeCmd() *cobra.Command {
	var port int
	var command, serviceTier string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the Codex model proxy on 127.0.0.1",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if serviceTier != modelproxy.ServiceTierPriority && serviceTier != modelproxy.ServiceTierStandard {
				cmd.SilenceUsage = true
				return fmt.Errorf("unknown --service-tier %q: choose %s or %s",
					serviceTier, modelproxy.ServiceTierPriority, modelproxy.ServiceTierStandard)
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			ln, err := net.Listen("tcp4", addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w (choose another port with --port)", addr, err)
			}
			defer func() { _ = ln.Close() }()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			key, err := modelproxy.EnsureKey(ctx, keyring.Stored(modelproxy.KeyService), func(k string) error {
				return keyring.Store(modelproxy.KeyService, k)
			})
			if err != nil {
				return err
			}
			backend := modelproxy.NewCodexBackend(modelproxy.CodexOptions{
				Command: command, ServiceTier: serviceTier, Stderr: cmd.ErrOrStderr(),
			})
			defer backend.Close()
			var handlerOptions []modelproxy.HandlerOption
			if home, err := os.UserHomeDir(); err == nil {
				// The bound port, not the flag: --port 0 asks the OS for one, and two such listeners must not
				// share a history file.
				bound := port
				if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
					bound = tcp.Port
				}
				path := modelproxy.RequestLogFile(home, bound)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "model proxy: no request history: %v\n", err)
				}
				// A proxy that cannot keep its history should still answer: say so and serve on.
				if requests, err := modelproxy.OpenRequestLog(path, modelproxy.RequestLogLimit); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "model proxy: no request history: %v\n", err)
				} else {
					defer func() { _ = requests.Close() }()
					handlerOptions = append(handlerOptions, modelproxy.WithRequestLog(requests))
				}
			}
			if port == modelproxy.StablePort {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("find home folder for the Codex usage file: %w", err)
				}
				go modelproxy.PollUsage(ctx, backend.ReadUsage, modelproxy.UsageFile(home, port), 2*time.Minute, cmd.ErrOrStderr())
			}
			server := &http.Server{
				Handler:           modelproxy.NewHandler(backend, key, handlerOptions...),
				ReadHeaderTimeout: 5 * time.Second,
				BaseContext:       func(net.Listener) context.Context { return ctx },
			}
			stopServer := context.AfterFunc(ctx, func() { _ = server.Close() })
			defer stopServer()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Model proxy at http://%s\n", ln.Addr())
			if err := server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Port on 127.0.0.1 (clients send the key stored in the "+modelproxy.KeyService+" Keychain entry)")
	cmd.Flags().StringVar(&command, "codex-command", "", "Codex executable (default: codex-lean if available, otherwise codex)")
	cmd.Flags().StringVar(&serviceTier, "service-tier", modelproxy.ServiceTierPriority,
		"OpenAI service tier: priority is Codex's Fast tier (about twice the tokens a second, spending the plan's allowance faster); standard leaves Codex's own default")
	return cmd
}
