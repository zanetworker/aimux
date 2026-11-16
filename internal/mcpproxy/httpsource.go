package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/toolreg"
)

const (
	// curlMaxTime bounds one request inside the runner (under runnerCallTimeout).
	curlMaxTime = "25"
	// curlStatusPrefix starts the line curl's --write-out adds to stderr:
	// the status and the X-OpenShell-Policy header, so a denial is told
	// from the API's own 403 without parsing headers out of the body.
	curlStatusPrefix = "aimux-http "
)

// curlArgv is the one command an http-openapi call runs: a GET (no -X, no
// body) to u over https only, no URL globbing, JSON accepted. Only u comes
// from the agent, built by httpsource.BuildURL.
func curlArgv(u string) []string {
	return []string{"curl", "-sS", "--fail-with-body", "--max-time", curlMaxTime, "--proto", "=https", "--globoff",
		"-H", "Accept: application/json", "-w", "%{stderr}" + curlStatusPrefix + `%{http_code} %header{x-openshell-policy}\n`, u}
}

// httpSourceBackend serves an http-openapi source's GET operations as
// tools, each call a curl in the runner sandbox.
type httpSourceBackend struct {
	server string
	src    toolreg.Source
	run    ToolRunner

	spec httpsource.SpecCache

	mu      sync.Mutex
	started bool // Ensure succeeded for this backend
	// baseline is each tool's operation as this backend's first listing
	// described it, never updated by later listings: the definitions the
	// gateway built this backend's servers from and filtered by the pins.
	// A call whose operation differs now is refused as changed. Approving
	// makes a new snapshot, which restarts runner backends (a new baseline).
	baseline map[string]httpsource.Operation
}

// NewHTTPSourceBackend serves src's GET operations for server through run.
// Tools are listed from the local spec (no runner needed); the first call
// ensures the runner.
func NewHTTPSourceBackend(server string, src toolreg.Source, run ToolRunner) Backend {
	return &httpSourceBackend{server: server, src: src, run: run}
}

// operations is the spec's operations, parsed again only when the file
// changed (so a changed file lists as changed tools). A new parse logs the
// operations it skipped and why.
func (b *httpSourceBackend) operations() ([]httpsource.Operation, error) {
	if b.src.Credential != "" {
		return nil, fmt.Errorf("%s: %w", b.server, httpsource.ErrCredential)
	}
	ops, ignored, read, err := b.spec.Load(b.src.Spec)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.server, err)
	}
	if read {
		for _, ig := range ignored {
			runnerLog("%s: spec %s: skipped %s %s %s: %s", b.server, b.src.Spec, strings.ToUpper(ig.Method), ig.Path, ig.OperationID, ig.Reason)
		}
	}
	return ops, nil
}

func (b *httpSourceBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	ops, err := b.operations()
	if err != nil {
		return nil, err
	}
	b.setBaseline(ops)
	return httpsource.Tools(b.server, b.src.BaseURL, ops)
}

// setBaseline records ops as the baseline unless one is recorded already.
func (b *httpSourceBackend) setBaseline(ops []httpsource.Operation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.baseline != nil {
		return
	}
	b.baseline = make(map[string]httpsource.Operation, len(ops))
	for _, op := range ops {
		b.baseline[httpsource.ToolName(b.server, op)] = op
	}
}

// changedSinceListing reports how op differs from tool's baseline
// (description aside, which pins do not cover); "" when it does not. ops
// is the spec now: with no baseline yet (a call before any listing), it
// becomes the baseline, so the call is checked against what it lists.
func (b *httpSourceBackend) changedSinceListing(tool string, op httpsource.Operation, ops []httpsource.Operation) string {
	b.setBaseline(ops)
	b.mu.Lock()
	was, ok := b.baseline[tool]
	b.mu.Unlock()
	switch {
	case !ok:
		return "it was not in the listing this backend started with"
	case was.Path != op.Path:
		return fmt.Sprintf("GET %s is now GET %s", was.Path, op.Path)
	case !reflect.DeepEqual(sortedParams(was.Params), sortedParams(op.Params)):
		return "its parameters changed"
	}
	return ""
}

// start ensures the runner once; a failure is retried on the next call.
func (b *httpSourceBackend) start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return nil
	}
	if err := ensureRunner(ctx, b.server, b.run); err != nil {
		return err
	}
	b.started = true
	return nil
}

// recover forgets the start and ensures the runner again, bounded by
// runnerRetryEnsure.
func (b *httpSourceBackend) recover(ctx context.Context) error {
	b.mu.Lock()
	b.started = false
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, runnerRetryEnsure)
	defer cancel()
	return b.start(ctx)
}

// CallTool checks the tool and builds its URL before anything runs: an
// unknown tool or a refused argument is a RequestError. A failed request
// is an IsError result; the agent sees no host detail about the sandbox.
func (b *httpSourceBackend) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tool := req.Params.Name
	if !strings.HasPrefix(tool, b.server+"_") {
		return nil, &RequestError{Err: fmt.Errorf("unknown tool %q", tool)}
	}
	ops, err := b.operations()
	if err != nil {
		if errors.Is(err, httpsource.ErrCredential) {
			return mcp.NewToolResultError(tool + ": " + httpsource.ErrCredential.Error()), nil
		}
		runnerLog("%s: %s: %v", b.server, tool, err)
		return mcp.NewToolResultError(tool + ": the source's OpenAPI file could not be read; see the aimux log"), nil
	}
	var op *httpsource.Operation
	for i := range ops {
		if httpsource.ToolName(b.server, ops[i]) == tool {
			op = &ops[i]
		}
	}
	if op == nil {
		return nil, &RequestError{Err: fmt.Errorf("unknown tool %q", tool)}
	}
	if why := b.changedSinceListing(tool, *op, ops); why != "" {
		return mcp.NewToolResultError(fmt.Sprintf("%s: tool changed since it was pinned: %s; review with aimux tools diff, then aimux tools approve", tool, why)), nil
	}
	u, err := httpsource.BuildURL(b.src.BaseURL, op.Path, req.GetArguments(), op.Params)
	if err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%s: %w", tool, err)}
	}
	if err := b.start(ctx); err != nil {
		return runnerNotReady(b.server, tool, err), nil
	}
	argv := curlArgv(u)
	out, stderr, exit, err := runBounded(ctx, b.run, argv)
	if errors.Is(err, ErrRunnerNotReady) { // gone since it started: ensure once, retry once
		if rerr := b.recover(ctx); rerr != nil {
			return runnerNotReady(b.server, tool, rerr), nil
		}
		out, stderr, exit, err = runBounded(ctx, b.run, argv)
	}
	if err != nil {
		return runFailure(b.server, tool, err), nil
	}
	if exit == 0 {
		return cappedText(out), nil
	}
	return mcp.NewToolResultError(httpFailure(tool, u, out, stderr, exit)), nil
}

// httpFailure is the agent's message for a curl that exited non-zero: the
// network policy's denial, the API's HTTP error with its body, a timeout,
// or curl's own error.
func httpFailure(tool, u string, out, stderr []byte, exit int) string {
	code, policy, rest := curlStatus(stderr)
	switch {
	case code == 403 && policy != "" && policyDenied(out):
		path := u
		if parsed, err := url.Parse(u); err == nil {
			path = parsed.EscapedPath()
		}
		return fmt.Sprintf("%s: the network policy refused GET %s (only approved read operations may run)", tool, path)
	case code >= 400:
		msg := fmt.Sprintf("%s: HTTP %d", tool, code)
		if s := strings.TrimSpace(cutUTF8(out, maxRunnerStderr)); s != "" {
			msg += ": " + s
		}
		return msg
	case exit == 28:
		return fmt.Sprintf("%s: timed out after %ss", tool, curlMaxTime)
	}
	msg := fmt.Sprintf("%s: request failed (curl exit %d)", tool, exit)
	if s := strings.TrimSpace(cutUTF8(rest, maxRunnerStderr)); s != "" {
		msg += ": " + s
	}
	return msg
}

// curlStatus finds the last status line curlArgv's --write-out wrote to
// stderr: the HTTP status (0 when no response), the X-OpenShell-Policy
// header, and stderr without that line.
func curlStatus(stderr []byte) (code int, policy string, rest []byte) {
	lines := bytes.Split(stderr, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		after, ok := bytes.CutPrefix(lines[i], []byte(curlStatusPrefix))
		if !ok {
			continue
		}
		codeText, hdr, _ := strings.Cut(string(after), " ")
		code, _ = strconv.Atoi(codeText)
		rest = bytes.Join(append(append([][]byte{}, lines[:i]...), lines[i+1:]...), []byte("\n"))
		return code, strings.TrimSpace(hdr), rest
	}
	return 0, "", stderr
}

// policyDenied reports whether body is OpenShell's denial ({"error":
// "policy_denied", ...}); the body's protocol field is not used.
func policyDenied(body []byte) bool {
	var d struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &d) == nil && d.Error == "policy_denied"
}

// sortedParams is params ordered by location then name: their order in the
// spec is not part of the tool (the pinned schema keys them by name).
func sortedParams(params []httpsource.Param) []httpsource.Param {
	out := slices.Clone(params)
	slices.SortFunc(out, func(a, b httpsource.Param) int {
		return strings.Compare(a.In+"\x00"+a.Name, b.In+"\x00"+b.Name)
	})
	return out
}

func (b *httpSourceBackend) Close() error { return nil }
