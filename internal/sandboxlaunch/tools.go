package sandboxlaunch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// RunnerCompile compiles the runner sandbox for the providers the gateway
// has (profile.CompileRunnerAvailable bound to one tool snapshot): the
// sources it leaves out come back in the error map by name.
type RunnerCompile func(have func(provider string) bool) (profile.Compiled, map[string]error, error)

// ToolSandbox is the runner sandbox (profile.RunnerSandbox) for one tool
// snapshot. Each source's tools reach it through For.
type ToolSandbox struct {
	b       *cliBackend
	compile RunnerCompile
	mu      sync.Mutex // one Ensure at a time: sources start in parallel
}

// NewToolSandbox drives the runner sandbox with the openshell CLI through
// run; policyDir is NewCLIBackend's.
func NewToolSandbox(run Runner, policyDir string, compile RunnerCompile) *ToolSandbox {
	return &ToolSandbox{b: NewCLIBackend(run, policyDir).(*cliBackend), compile: compile}
}

// SourceTools is the runner sandbox as one source's backend uses it.
type SourceTools struct {
	t      *ToolSandbox
	source string
}

// For is the runner sandbox bound to source.
func (t *ToolSandbox) For(source string) SourceTools { return SourceTools{t: t, source: source} }

// Ensure compiles the runner against the gateway's providers and creates
// or updates the sandbox. A source left out for a missing provider gets
// that error while the sandbox still serves the others.
func (s SourceTools) Ensure(ctx context.Context) error {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	out, err := s.t.b.output(ctx, nil, "provider", "list", "--names")
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	names := strings.Fields(out)
	c, dropped, err := s.t.compile(func(p string) bool { return slices.Contains(names, p) })
	if err != nil {
		return err
	}
	// Ensure creates a missing sandbox and replaces a dead one, but never
	// starts one someone stopped: say how.
	if phase, ok, err := s.t.b.phase(ctx, profile.RunnerSandbox); err != nil {
		return err
	} else if ok && terminalPhases[phase] && !deadPhases[phase] {
		return stoppedRunner(phase)
	}
	if err := s.t.b.Ensure(ctx, c); err != nil {
		return err
	}
	return dropped[s.source]
}

// RunTool runs argv in the runner sandbox (cliBackend.RunTool).
func (s SourceTools) RunTool(ctx context.Context, argv []string) (stdout, stderr []byte, exit int, err error) {
	return s.t.b.RunTool(ctx, profile.RunnerSandbox, argv)
}

// RunnerTools is what toolgateway.ReadonlyServer.Runner and
// mcpproxy.ReadonlyConfig.Tools take: for each snapshot, one ToolSandbox
// compiled from it with the builtin packs and the http-openapi sources'
// specs, bound per source.
func RunnerTools(run Runner, policyDir string) func(snap toolreg.Snapshot) func(source string) mcpproxy.ToolRunner {
	return func(snap toolreg.Snapshot) func(string) mcpproxy.ToolRunner {
		ts := NewToolSandbox(run, policyDir, func(have func(string) bool) (profile.Compiled, map[string]error, error) {
			return profile.CompileRunnerAvailable(snap, clipack.Builtin, httpsource.SourceOperations, have)
		})
		return func(source string) mcpproxy.ToolRunner { return ts.For(source) }
	}
}

// RunnerProbe is RunnerTools read-only, for status, diff and doctor: its
// Ensure only checks that aimux-tools exists and is Ready (it never
// creates or updates it), and RunTool runs in it as usual.
func RunnerProbe(run Runner, policyDir string) func(snap toolreg.Snapshot) func(source string) mcpproxy.ToolRunner {
	return func(toolreg.Snapshot) func(string) mcpproxy.ToolRunner {
		b := NewCLIBackend(run, policyDir).(*cliBackend)
		return func(source string) mcpproxy.ToolRunner { return probeTools{b: b, source: source} }
	}
}

type probeTools struct {
	b      *cliBackend
	source string
}

func (p probeTools) Ensure(ctx context.Context) error {
	phase, ok, err := p.b.phase(ctx, profile.RunnerSandbox)
	if err != nil {
		return err
	}
	switch {
	case ok && terminalPhases[phase]:
		return stoppedRunner(phase)
	case !ok || phase != "Ready":
		return &mcpproxy.RunnerError{Err: errors.New("tool runner not started"),
			Next: fmt.Sprintf("run aimux tools approve <set> (a set of %s), or start the tool gateway worker: aimux mcp proxy gateway install", p.source)}
	}
	return nil
}

// stoppedRunner is a runner in a terminal phase, with how to bring it back.
func stoppedRunner(phase string) error {
	s := profile.RunnerSandbox
	next := fmt.Sprintf("delete it with openshell -g local sandbox delete %s, then run aimux tools approve <set> or restart the tool gateway worker (launchctl kickstart -k gui/$(id -u)/com.aimux.tool-gateway) to create it again", s)
	if phase == "Stopped" {
		next = fmt.Sprintf("start it with openshell -g local sandbox start %s, or %s", s, next)
	}
	return &mcpproxy.RunnerError{Err: fmt.Errorf("the tool runner %s is %s", s, strings.ToLower(phase)), Next: next}
}

func (p probeTools) RunTool(ctx context.Context, argv []string) (stdout, stderr []byte, exit int, err error) {
	return p.b.RunTool(ctx, profile.RunnerSandbox, argv)
}
