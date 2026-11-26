package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/modelproxy"
)

// How the proxy a shell points at answered.
const (
	routeProxyOK       = "ok"
	routeProxyDegraded = "degraded"
	routeProxyDown     = "down"      // nothing answers on the port
	routeProxyNotAimux = "not aimux" // something answers there, and it is not the model proxy
)

// routeProxy is what the proxy on the shell's port said of itself.
type routeProxy struct {
	State   string   `json:"state"`
	Build   string   `json:"build,omitempty"`
	Engines []string `json:"engines"`
	// EngineOn: whether the engine the session is on is one this proxy answers with. Absent when the proxy does
	// not answer, or is a build from before it listed its engines.
	EngineOn *bool `json:"engine_on,omitempty"`
}

type routeReport struct {
	SchemaVersion int `json:"schema_version"`
	modelproxy.Route
	Proxy *routeProxy `json:"proxy,omitempty"`
}

// errRouteBroken: the shell's sessions go through a proxy that cannot answer them.
var errRouteBroken = errors.New("this shell's sessions go through the model proxy, and it cannot answer them as things are")

func newModelProxyRouteCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Say where this shell's Claude Code sessions go: direct or through the model proxy, on which engine, and whether the proxy answers",
		Long: "Reads the shell's environment and, when its sessions go through a model proxy on this machine, that\n" +
			"proxy's keyless health answer. It prints one line, for a status line or a prompt:\n\n" +
			"  direct · anthropic claude-opus-5-5\n" +
			"  proxy :7385 ok · codex gpt-6.2-sol\n" +
			"  proxy :7385 DOWN · codex gpt-6.2-sol\n\n" +
			"--model names the model the session is on now, which follows /model; without it the model the shell pins\n" +
			"is used, if it pins one. --json gives the same as an object: through_proxy, port, mode (direct, engines,\n" +
			"codex, unmarked, elsewhere), engine, model, and proxy with its state (ok, degraded, down, not aimux),\n" +
			"build, engines and engine_on. It never prints the proxy key and sends it nowhere.\n" +
			"Exits 1 when the sessions go through a proxy that does not answer, or whose engine for that model is off.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return runProxyRoute(cmd.Context(), os.Getenv, model, jsonOutput, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "The model the session is on now, as Claude Code names it (codex:gpt-6.2-sol, claude-opus-5-5)")
	return cmd
}

func runProxyRoute(ctx context.Context, getenv func(string) string, model string, asJSON bool, out io.Writer) error {
	report := routeReport{SchemaVersion: 1, Route: modelproxy.RouteOf(getenv, model)}
	var broken error
	if report.Through {
		report.Proxy = askRouteProxy(ctx, report.Route)
		p := report.Proxy
		if (p.State != routeProxyOK && p.State != routeProxyDegraded) || (p.EngineOn != nil && !*p.EngineOn) {
			broken = errRouteBroken
		}
	}
	if asJSON {
		if err := writeJSON(out, report); err != nil {
			return err
		}
		return broken
	}
	_, _ = fmt.Fprintln(out, routeLine(report))
	return broken
}

// askRouteProxy reads the keyless health answer of the proxy the route names. It waits half a second at most: a
// status line calls this on every refresh.
func askRouteProxy(ctx context.Context, route modelproxy.Route) *routeProxy {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	p := &routeProxy{State: routeProxyDown, Engines: []string{}}
	live, err := readModelLiveness(ctx, fmt.Sprintf("127.0.0.1:%d", route.Port))
	switch {
	case errors.Is(err, errStatusWrongProcess) || errors.Is(err, errStatusNoHealth):
		p.State = routeProxyNotAimux
		return p
	case err != nil:
		// An answer that is not the proxy's (a page, an empty body) is something else listening; no answer at
		// all is nothing there.
		var opErr *net.OpError
		noAnswer := errors.As(err, &opErr) || errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err)
		if !noAnswer {
			p.State = routeProxyNotAimux
		}
		return p
	case live.Service != modelproxy.LivenessService || (live.Status != routeProxyOK && live.Status != routeProxyDegraded):
		p.State = routeProxyNotAimux
		return p
	}
	p.State, p.Build = live.Status, live.Version
	if len(live.Engines) > 0 {
		p.Engines = live.Engines
		on := slices.Contains(live.Engines, route.Engine)
		p.EngineOn = &on
	}
	return p
}

func routeLine(r routeReport) string {
	what := r.Engine
	if r.Model != "" {
		what += " " + r.Model
	}
	switch {
	case r.Mode == modelproxy.RouteElsewhere:
		return "another gateway"
	case !r.Through:
		return "direct · " + what
	}
	state := r.Proxy.State
	if state == routeProxyDown || state == routeProxyNotAimux {
		state = map[string]string{routeProxyDown: "DOWN", routeProxyNotAimux: "NOT AIMUX"}[state]
	}
	line := fmt.Sprintf("proxy :%d %s · %s", r.Port, state, what)
	if r.Proxy.EngineOn != nil && !*r.Proxy.EngineOn {
		line += fmt.Sprintf(" (the %s engine is off)", r.Engine)
	}
	return line
}
