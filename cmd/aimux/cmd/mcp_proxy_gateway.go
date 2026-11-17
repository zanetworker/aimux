package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/gatewayworker"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

func newMCPProxyGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "The sandbox tool gateway worker: pinned read-only tools for OpenShell sandboxes on 127.0.0.1:7390",
		Long: "The sandbox tool gateway is the second worker of the aimux mcp proxy service. It serves the read-only tools " +
			"approved in ~/.config/aimux/tool-sources.yaml to OpenShell sandboxes (host.containers.internal:7390), with its own " +
			"pool of backends, and takes each sandbox turn's token as a lease from aimux remote serve over ~/.aimux/gateway.sock.",
	}
	cmd.AddCommand(newMCPProxyGatewayServeCmd(), newMCPProxyGatewayInstallCmd(), newMCPProxyGatewayUninstallCmd(), newMCPProxyGatewayStatusCmd())
	return cmd
}

func newMCPProxyGatewayServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the sandbox tool gateway worker in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return gatewayworker.Run(ctx, home, log.New(cmd.ErrOrStderr(), "", log.LstdFlags), net.Listen)
		},
	}
}

// gatewayAgentLabel is the gateway worker's launchd job.
const gatewayAgentLabel = "com.aimux.tool-gateway"

// gatewayAgent is the gateway worker's launchd agent under home; install
// sets Binary.
func gatewayAgent(home string) mcpproxy.Agent {
	return mcpproxy.Agent{
		Label:     gatewayAgentLabel,
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", gatewayAgentLabel+".plist"),
		LogDir:    filepath.Join(home, ".aimux", "logs", "tool-gateway"),
		Args:      []string{"mcp", "proxy", "gateway", "serve"},
		Run:       runLaunchctl,
		UID:       os.Getuid(),
		// launchd starts the worker without the shell's environment; the
		// openshell CLI is found through this PATH. Never more: the plist is
		// world-readable.
		Env: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home},
	}
}

func newMCPProxyGatewayInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Run the sandbox tool gateway worker under launchd (com.aimux.tool-gateway), started at login and restarted if it stops",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			a := gatewayAgent(home)
			if a.Binary, err = stableBinary("aimux mcp proxy gateway install"); err != nil {
				return err
			}
			// launchd opens the log files before starting the worker and
			// Install writes the plist there: both folders must exist.
			if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o755); err != nil { //nolint:gosec // ~/Library/LaunchAgents is world-readable by design
				return err
			}
			if err := os.MkdirAll(a.LogDir, 0o700); err != nil {
				return err
			}
			if err := a.Install(); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Installed %s (%s); logs in %s\n", gatewayAgentLabel, a.Binary, a.LogDir)
			return nil
		},
	}
}

func newMCPProxyGatewayUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the sandbox tool gateway worker and remove its launchd job; Workspace turns fail until it is installed again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			if err := gatewayAgent(home).Uninstall(); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", gatewayAgentLabel)
			return nil
		},
	}
}

func newMCPProxyGatewayStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the sandbox tool gateway worker runs, its leases and its backends",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			line := gatewayStatusLine(cmd.Context(), toolgateway.ControlClient{Path: toolgateway.ControlSocketPath(home)})
			w := cmd.OutOrStdout()
			if !strings.HasPrefix(line, "tool gateway: running") {
				w = cmd.ErrOrStderr() // a problem, as other status commands report them
			}
			_, _ = fmt.Fprintln(w, line)
			return nil
		},
	}
}

// gatewayStatuser is what the status line needs of a ControlClient.
type gatewayStatuser interface {
	Status(ctx context.Context) (toolgateway.ControlStatus, error)
}

// gatewayStatusLine is the gateway worker in one line, for status and
// doctor.
func gatewayStatusLine(ctx context.Context, c gatewayStatuser) string {
	st, err := c.Status(ctx)
	switch {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		return "tool gateway: not running (Workspace turns fail): run aimux mcp proxy gateway install"
	case err != nil:
		return "tool gateway: unknown: " + err.Error()
	}
	line := fmt.Sprintf("tool gateway: running, %d leases, %d backends", st.Leases, len(st.Backends))
	if len(st.Down) > 0 {
		line += ", down: " + strings.Join(st.Down, ", ")
	}
	return line
}

// gatewayWorkerCheck is doctor's view of the gateway worker.
func gatewayWorkerCheck(ctx context.Context, c gatewayStatuser) doctorCheck {
	const id = "gateway.worker"
	line := strings.TrimPrefix(gatewayStatusLine(ctx, c), "tool gateway: ")
	switch _, err := c.Status(ctx); {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		return doctorCheck{ID: id, Status: checkFail, Detail: "not running, so Workspace turns fail", Fix: "aimux mcp proxy gateway install"}
	case err != nil:
		return doctorCheck{ID: id, Status: checkWarn, Detail: line, Fix: "aimux mcp proxy gateway status; logs in ~/.aimux/logs/tool-gateway"}
	}
	return ok(id, line)
}
