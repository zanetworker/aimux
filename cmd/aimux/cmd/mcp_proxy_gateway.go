package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	var allowWorktree bool
	cmd := &cobra.Command{
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
			if err := guardWorktreeBinary(a.Binary, allowWorktree, cmd.ErrOrStderr()); err != nil {
				return err
			}
			worker := toolgateway.ControlClient{Path: toolgateway.ControlSocketPath(home), Timeout: 2 * time.Second}
			// Ctrl-C or SIGTERM stops the wait for the new worker; the
			// install is then rolled back like one that never answered.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return withGatewayLifecycle(home, func() error {
				return installGatewayAgent(ctx, a, cmd.OutOrStdout(), worker, gatewayReadyWait)
			})
		},
	}
	cmd.Flags().BoolVar(&allowWorktree, "allow-worktree-binary", false, "Install from an aimux built inside a git worktree (the launchd job stops when the worktree is removed)")
	return cmd
}

// gatewayReadyWait is how long install waits for the restarted worker to
// answer: it opens its control socket only after listing every backend
// (four at a time, an HTTP backend up to 5 s to start and 5 s per page),
// so the budget leaves room for several slow but healthy ones.
// gatewayReadyPoll is how often it asks.
const (
	gatewayReadyWait = 90 * time.Second
	gatewayReadyPoll = 250 * time.Millisecond
)

// installGatewayAgent writes the worker's plist for a.Binary and restarts
// the job so launchd runs it (mcpproxy.Agent.InstallChecked), waiting up to
// wait for a worker started after the restart to answer on its control
// socket (st); a failed bootstrap or a worker that never answers is undone
// there. It says which binary the job ran before when that was another.
func installGatewayAgent(ctx context.Context, a mcpproxy.Agent, out io.Writer, st gatewayStatuser, wait time.Duration) error {
	// launchd opens the log files before starting the worker and Install
	// writes the plist there: both folders must exist.
	if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o755); err != nil { //nolint:gosec // ~/Library/LaunchAgents is world-readable by design
		return err
	}
	if err := os.MkdirAll(a.LogDir, 0o700); err != nil {
		return err
	}
	was, err := a.InstallChecked(gatewayReadyWithin(ctx, st, a, wait))
	if err != nil {
		return fmt.Errorf("tool gateway worker (waited up to %v): %w", wait, err)
	}
	switched := ""
	if was != "" && was != a.Binary {
		switched = ", was " + was
	}
	_, _ = fmt.Fprintf(out, "Installed %s (%s%s); logs in %s\n", gatewayAgentLabel, a.Binary, switched, a.LogDir)
	return nil
}

// cctxFor is ctx ending at deadline as well.
func cctxFor(ctx context.Context, deadline time.Time) context.Context {
	c, cancel := context.WithDeadline(ctx, deadline)
	context.AfterFunc(c, cancel) // released when c ends
	return c
}

// jobPID is a.PID bounded by ctx: launchctl print runs without a context,
// so a stalled one is left behind (it exits on its own) rather than waited
// for past the deadline or Ctrl-C.
func jobPID(ctx context.Context, a mcpproxy.Agent) (int, error) {
	type result struct {
		pid int
		err error
	}
	done := make(chan result, 1)
	go func() {
		pid, err := a.PID()
		done <- result{pid, err}
	}()
	select {
	case r := <-done:
		return r.pid, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// gatewayReadyWithin is InstallChecked's readiness check with one deadline,
// wait from now, for the install and its rollback re-check together: the
// re-check gets what the first check left, never a second full wait.
func gatewayReadyWithin(ctx context.Context, st gatewayStatuser, a mcpproxy.Agent, wait time.Duration) func(since time.Time) mcpproxy.Readiness {
	deadline := time.Now().Add(wait)
	return func(since time.Time) mcpproxy.Readiness {
		return gatewayAnswers(ctx, st, a, since, time.Until(deadline))
	}
}

// gatewayAnswers polls st, for at most wait in all (each call gets what is
// left of it, 2 s at most), until the launchd job's own worker, started
// after since, answers: Ready. A worker that answers without a pid (an
// older aimux) cannot be told from a foreground gateway serve: ReadyUnknown,
// as is a check with no time left or one ctx stopped. Otherwise NotReady.
func gatewayAnswers(ctx context.Context, st gatewayStatuser, a mcpproxy.Agent, since time.Time, wait time.Duration) mcpproxy.Readiness {
	sleep := a.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	deadline := time.Now().Add(wait)
	if wait <= 0 || ctx.Err() != nil {
		return mcpproxy.ReadyUnknown // not asked at all
	}
	legacy := false
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		if ctx.Err() != nil {
			return mcpproxy.NotReady // interrupted: roll back
		}
		cctx, cancel := context.WithTimeout(ctx, min(left, 2*time.Second))
		s, err := st.Status(cctx)
		cancel()
		if err == nil && !s.StartedAt.Before(since) {
			if s.PID == 0 {
				legacy = true
			} else if job, perr := jobPID(cctxFor(ctx, deadline), a); perr == nil && job == s.PID {
				return mcpproxy.Ready // the launchd job's own worker, not a foreground serve
			}
		}
		sleep(min(gatewayReadyPoll, max(time.Until(deadline), 0)))
	}
	if legacy {
		return mcpproxy.ReadyUnknown
	}
	return mcpproxy.NotReady
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
			if err := withGatewayLifecycle(home, gatewayAgent(home).Uninstall); err != nil {
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
	return gatewayStatusLineAt(ctx, c, time.Now)
}

// gatewayStatusLineAt is gatewayStatusLine with clock now, read once the
// worker answered: it says when the worker started and when each down
// backend was last checked (retried every minute), so a last-known state
// is not read as live.
func gatewayStatusLineAt(ctx context.Context, c gatewayStatuser, clock func() time.Time) string {
	st, err := c.Status(ctx)
	now := clock()
	switch {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		return "tool gateway: not running (Workspace turns fail): run aimux mcp proxy gateway install"
	case err != nil:
		return "tool gateway: unknown: " + err.Error()
	}
	line := "tool gateway: running"
	if !st.StartedAt.IsZero() {
		line += " (started " + mcpproxy.Ago(now, st.StartedAt) + ")"
	}
	line += fmt.Sprintf(", %d leases, %d backends", st.Leases, len(st.Backends))
	if len(st.Down) > 0 {
		down := make([]string, len(st.Down))
		for i, n := range st.Down {
			down[i] = n
			if at, ok := st.Checked[n]; ok { // an older worker reports no times
				down[i] += " (checked " + mcpproxy.Ago(now, at) + ")"
			}
		}
		line += ", down: " + strings.Join(down, ", ")
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
