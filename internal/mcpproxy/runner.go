package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zanetworker/aimux/internal/clipack"
)

// ToolRunner is the runner sandbox (aimux-tools), bound by the caller to
// one source.
type ToolRunner interface {
	// Ensure creates or updates the sandbox; an error says why this
	// source cannot run there.
	Ensure(ctx context.Context) error
	// RunTool runs argv (binary first, no shell) in the sandbox. A command
	// that exited returns its output and status with a nil error.
	RunTool(ctx context.Context, argv []string) (stdout, stderr []byte, exit int, err error)
}

// ErrRunnerNotReady is a RunTool failure because the runner sandbox is
// gone or not Ready (openshell exits 1 for that, as a tool may): the
// caller ensures it again.
var ErrRunnerNotReady = errors.New("the tool runner sandbox is not ready")

// RunnerError is a runner failure that carries its own next step (a
// runner never started, a stopped one); other failures get
// runnerSetupHint.
type RunnerError struct {
	Err  error
	Next string
}

func (e *RunnerError) Error() string { return e.Err.Error() + " (" + e.Next + ")" }

func (e *RunnerError) Unwrap() error { return e.Err }

// nextStep is the one next step for a runner failure.
func nextStep(err error) string {
	var re *RunnerError
	if errors.As(err, &re) {
		return re.Next
	}
	return runnerSetupHint
}

var (
	// runnerCallTimeout bounds one tool command (a var so tests can shorten it).
	runnerCallTimeout = 30 * time.Second
	// runnerRetryEnsure bounds the Ensure a call makes when the runner went
	// away, so a runner stuck short of Ready fails the call instead of
	// holding it for Ensure's whole wait.
	runnerRetryEnsure = 60 * time.Second
	// runnerLog hears runner failures in full; the agent gets a generic
	// error result.
	runnerLog = log.Printf
)

const (
	maxRunnerOutput = 256 << 10
	maxRunnerStderr = 2 << 10
	runnerCutMarker = "\n[output cut at 256 KiB]"
	runnerSetupHint = "run aimux sandbox setup, then aimux sandbox status"
)

// versionRE is the first dotted version in `<binary> --version` output.
var versionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// cliBackend serves a CLI pack's commands as tools, each run in the runner
// sandbox with the argv the pack validated.
type cliBackend struct {
	server string
	pack   clipack.Pack
	run    ToolRunner

	mu      sync.Mutex
	version string // the binary's version when it started
	// changed is the version a later listing (or a restart on the retry
	// path) found when it differs from version: the runner's binary was
	// replaced, so calls are refused until the tools are approved again
	// (a new snapshot restarts the backend).
	changed string
	// justStarted: start read the version and no listing used it yet.
	justStarted bool
}

// NewCLIBackend serves pack's commands for server through run. It starts
// on first use: Ensure, then `<binary> --version` once, whose version goes
// into the tools' annotations (and so their pins).
func NewCLIBackend(server string, pack clipack.Pack, run ToolRunner) Backend {
	return &cliBackend{server: server, pack: pack, run: run}
}

// start ensures the sandbox and reads the binary's version, once; a
// failure is retried on the next use.
func (b *cliBackend) start(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.version != "" {
		return b.version, nil
	}
	if err := b.run.Ensure(ctx); err != nil {
		var re *RunnerError
		if errors.As(err, &re) || strings.Contains(err.Error(), "aimux sandbox setup") {
			return "", fmt.Errorf("%s: tool runner: %w", b.server, err)
		}
		return "", fmt.Errorf("%s: tool runner: %w (%s)", b.server, err, runnerSetupHint)
	}
	v, err := b.readVersion(ctx)
	if err != nil {
		return "", err
	}
	b.version, b.changed, b.justStarted = v, "", true
	return v, nil
}

// readVersion runs `<binary> --version` in the runner.
func (b *cliBackend) readVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runnerCallTimeout)
	defer cancel()
	out, stderr, exit, err := b.run.RunTool(ctx, []string{b.pack.Binary, "--version"})
	if err != nil {
		return "", fmt.Errorf("%s: %s --version: %w", b.server, b.pack.Binary, err)
	}
	if exit != 0 {
		return "", fmt.Errorf("%s: %s --version: exit status %d: %s", b.server, b.pack.Binary, exit, cutUTF8(stderr, maxRunnerStderr))
	}
	first, _, _ := strings.Cut(string(out), "\n")
	v := versionRE.FindString(first)
	if v == "" {
		return "", fmt.Errorf("%s: %s --version printed no version: %q", b.server, b.pack.Binary, cutUTF8([]byte(first), 200))
	}
	return v, nil
}

// observe records the version a listing or restart found against the one
// the backend started with, and returns the version to list.
func (b *cliBackend) observe(v string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.changed = ""
	if v != b.version {
		b.changed = v
	}
	return v
}

// ListToolsRaw reads the binary's version on every listing (the gateway
// relists at most once a minute, and to verify a call), so a binary
// replaced under a running backend lists as changed tools.
func (b *cliBackend) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
	v, err := b.start(ctx)
	if err != nil {
		return nil, err
	}
	if !b.fresh() {
		v, err = b.readVersion(ctx)
		if errors.Is(err, ErrRunnerNotReady) {
			v, err = b.recover(ctx)
		}
		if err != nil {
			return nil, err
		}
		v = b.observe(v)
	}
	return b.pack.Tools(v)
}

// recover is the one step the call and listing paths take when the runner
// went away after start: forget the start, Ensure once (bounded by
// runnerRetryEnsure; a read-only runner only reports it is not started)
// and read the version again. A runner back with another binary is marked
// changed. It returns the version now in the runner.
func (b *cliBackend) recover(ctx context.Context) (string, error) {
	was := b.reset()
	ectx, cancel := context.WithTimeout(ctx, runnerRetryEnsure)
	defer cancel()
	v, err := b.start(ectx)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.justStarted = false // this caller uses the version start read
	if was != "" && v != was {
		b.version, b.changed = was, v
	}
	b.mu.Unlock()
	return v, nil
}

// fresh reports whether start just read the version, so the listing
// need not read it again (the marker is consumed).
func (b *cliBackend) fresh() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.justStarted
	b.justStarted = false
	return f
}

// changedError is the refusal for a call while the binary differs from
// the one the backend started with.
func (b *cliBackend) changedError(tool string) *mcp.CallToolResult {
	b.mu.Lock()
	was, now := b.version, b.changed
	b.mu.Unlock()
	return mcp.NewToolResultError(fmt.Sprintf("%s: tool changed since it was pinned: %s in the tool runner is now %s (was %s); review with aimux tools diff, then aimux tools approve", tool, b.pack.Binary, now, was))
}

func (b *cliBackend) isChanged() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed != ""
}

// CallTool validates the arguments before anything runs: a tool the pack
// lacks or an argument it refuses is a RequestError. A command that fails
// is an IsError result; the agent sees no host detail about the sandbox.
func (b *cliBackend) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tool := req.Params.Name
	command, ok := strings.CutPrefix(tool, b.pack.Name+"_")
	if !ok {
		return nil, &RequestError{Err: fmt.Errorf("unknown tool %q", tool)}
	}
	argv, err := b.pack.Argv(command, req.GetArguments())
	if err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%s: %w", tool, err)}
	}
	notReady := func(err error) *mcp.CallToolResult {
		runnerLog("%s: %s: %v", b.server, tool, err)
		return mcp.NewToolResultError(tool + ": the tool runner is not ready (" + nextStep(err) + ")")
	}
	if _, err := b.start(ctx); err != nil {
		return notReady(err), nil
	}
	if b.isChanged() {
		return b.changedError(tool), nil
	}
	out, stderr, exit, err := b.runOnce(ctx, argv)
	if errors.Is(err, ErrRunnerNotReady) { // gone since it started: ensure once, retry once
		if _, rerr := b.recover(ctx); rerr != nil {
			return notReady(rerr), nil
		}
		if b.isChanged() { // it came back with another binary
			return b.changedError(tool), nil
		}
		out, stderr, exit, err = b.runOnce(ctx, argv)
	}
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return mcp.NewToolResultError(fmt.Sprintf("%s: timed out after %s", tool, runnerCallTimeout)), nil
		case errors.Is(err, ErrRunnerNotReady):
			return notReady(err), nil
		}
		runnerLog("%s: %s: run in the tool runner: %v", b.server, tool, err)
		return mcp.NewToolResultError(tool + ": the tool runner failed; see the aimux log"), nil
	}
	if exit != 0 {
		if method, path, ok := policyDenial(out, stderr); ok {
			return mcp.NewToolResultError(fmt.Sprintf("%s: the network policy refused %s %s (tools here may only read)", tool, method, path)), nil
		}
		msg := fmt.Sprintf("%s: exit status %d", tool, exit)
		if s := strings.TrimSpace(cutUTF8(stderr, maxRunnerStderr)); s != "" {
			msg += ": " + s
		}
		return mcp.NewToolResultError(msg), nil
	}
	if len(out) > maxRunnerOutput {
		return mcp.NewToolResultText(cutUTF8(out, maxRunnerOutput) + runnerCutMarker), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

// runOnce runs argv with runnerCallTimeout; a timeout is
// context.DeadlineExceeded.
func (b *cliBackend) runOnce(ctx context.Context, argv []string) (stdout, stderr []byte, exit int, err error) {
	cctx, cancel := context.WithTimeout(ctx, runnerCallTimeout)
	defer cancel()
	stdout, stderr, exit, err = b.run.RunTool(cctx, argv)
	if err != nil && ctx.Err() == nil && cctx.Err() != nil {
		err = context.DeadlineExceeded
	}
	return stdout, stderr, exit, err
}

// reset forgets the start, so the next use ensures the runner again, and
// returns the version it started with.
func (b *cliBackend) reset() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	was := b.version
	b.version = ""
	return was
}

func (b *cliBackend) Close() error { return nil }

// policyDenial reads OpenShell's denial body out of a CLI's output: gh
// prints the 403 body, then "gh: HTTP 403" (no headers to check, so the
// body's error and the 403 together decide). It returns the refused
// request's method and path.
func policyDenial(stdout, stderr []byte) (method, path string, ok bool) {
	all := append(append(append([]byte{}, stdout...), '\n'), stderr...)
	if !bytes.Contains(all, []byte("HTTP 403")) {
		return "", "", false
	}
	i := bytes.Index(all, []byte(`"policy_denied"`))
	if i < 0 {
		return "", "", false
	}
	start := bytes.LastIndexByte(all[:i], '{')
	if start < 0 {
		return "", "", false
	}
	var d struct {
		Error  string `json:"error"`
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(bytes.NewReader(all[start:])).Decode(&d); err != nil || d.Error != "policy_denied" || d.Method == "" {
		return "", "", false
	}
	return d.Method, d.Path, true
}

// cutUTF8 is at most n bytes of b, not ending inside a rune.
func cutUTF8(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	b = b[:n]
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return string(b)
}
