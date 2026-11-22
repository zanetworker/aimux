package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
)

func newModelCmd() *cobra.Command {
	model := &cobra.Command{Use: "model", Short: "Work with model APIs"}
	proxy := &cobra.Command{Use: "proxy", Short: "Expose a local Anthropic Messages API"}
	proxy.AddCommand(newModelProxyServeCmd(), newModelProxyStatusCmd())
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

func newModelProxyServeCmd() *cobra.Command {
	var port int
	var command string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the Codex model proxy on 127.0.0.1",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			backend := modelproxy.NewCodexBackend(command, cmd.ErrOrStderr())
			defer backend.Close()
			if port == modelproxy.StablePort {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("find home folder for the Codex usage file: %w", err)
				}
				go modelproxy.PollUsage(ctx, backend.ReadUsage, modelproxy.UsageFile(home, port), 2*time.Minute, cmd.ErrOrStderr())
			}
			server := &http.Server{
				Handler:           modelproxy.NewHandler(backend, key),
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
	return cmd
}
