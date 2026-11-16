package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// fakeTools is a ToolRunner: --version answers version, other commands
// answer from run.
type fakeTools struct {
	mu        sync.Mutex
	ensureErr error
	ensures   int
	version   string
	calls     [][]string
	run       func(ctx context.Context, argv []string) ([]byte, []byte, int, error)
}

func (f *fakeTools) Ensure(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	return f.ensureErr
}

func (f *fakeTools) RunTool(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	f.mu.Unlock()
	if len(argv) == 2 && argv[1] == "--version" {
		return []byte(f.version + "\n"), nil, 0, nil
	}
	if f.run == nil {
		return nil, nil, 0, nil
	}
	return f.run(ctx, argv)
}

// toolCalls is every RunTool argv but --version.
func (f *fakeTools) toolCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) != 2 || c[1] != "--version" {
			out = append(out, c)
		}
	}
	return out
}

func ghPack(t *testing.T) clipack.Pack {
	t.Helper()
	p, err := clipack.Builtin("gh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runnerReq(name string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}}
}

func TestRunnerBackend_ListsPackTools(t *testing.T) {
	f := &fakeTools{version: "gh version 2.83.1 (2025-11-13)\nhttps://github.com/cli/cli/releases/tag/v2.83.1"}
	b := NewCLIBackend("github", ghPack(t), f)
	tools, err := b.ListToolsRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ghPack(t).Tools("2.83.1")
	if len(tools) == 0 || len(tools) != len(want) {
		t.Fatalf("%d tools, want %d", len(tools), len(want))
	}
	for i := range tools {
		if !bytes.Equal(tools[i], want[i]) {
			t.Fatalf("tool %d = %s\nwant %s", i, tools[i], want[i])
		}
	}
	var tool struct {
		Annotations map[string]any `json:"annotations"`
	}
	if err := json.Unmarshal(tools[0], &tool); err != nil || tool.Annotations["x-aimux-binary"] != "2.83.1" {
		t.Fatalf("annotations = %v (%v)", tool.Annotations, err)
	}
	// Each listing reads the version again; Ensure runs once.
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || !slices.Equal(f.calls[1], []string{"/usr/bin/gh", "--version"}) || f.ensures != 1 {
		t.Fatalf("calls = %q, ensures %d", f.calls, f.ensures)
	}

	// Output that names no version is an error, not an empty pin field.
	f = &fakeTools{version: "gh: command not found"}
	if _, err := NewCLIBackend("github", ghPack(t), f).ListToolsRaw(context.Background()); err == nil {
		t.Fatal("no version accepted")
	}
}

func TestRunnerBackend_CallRunsArgv(t *testing.T) {
	f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte(`[{"number":1,"title":"t"}]`), []byte("warning: ignored"), 0, nil
	}}
	b := NewCLIBackend("github", ghPack(t), f)
	res, err := b.CallTool(context.Background(), runnerReq("gh_issue_list", map[string]any{"repo": "cli/cli"}))
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if got := resultText(t, res); got != `[{"number":1,"title":"t"}]` {
		t.Fatalf("text = %q", got)
	}
	want, err := ghPack(t).Argv("issue_list", map[string]any{"repo": "cli/cli"})
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.toolCalls(); len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("argv = %q, want %q", calls, want)
	}
}

func TestRunnerBackend_RefusesBadArgsWithoutRunning(t *testing.T) {
	f := &fakeTools{version: "gh version 2.83.1"}
	b := NewCLIBackend("github", ghPack(t), f)
	for name, req := range map[string]mcp.CallToolRequest{
		"flag value":    runnerReq("gh_issue_list", map[string]any{"repo": "--help"}),
		"unknown key":   runnerReq("gh_issue_list", map[string]any{"repo": "cli/cli", "web": true}),
		"unknown tool":  runnerReq("gh_issue_create", map[string]any{"repo": "cli/cli"}),
		"other pack":    runnerReq("kubectl_get", nil),
		"missing input": runnerReq("gh_issue_list", nil),
	} {
		_, err := b.CallTool(context.Background(), req)
		var rerr *RequestError
		if !errors.As(err, &rerr) {
			t.Errorf("%s: err = %v, want a RequestError", name, err)
		}
	}
	if len(f.calls) != 0 || f.ensures != 0 {
		t.Fatalf("refused calls reached the runner: %q, %d ensures", f.calls, f.ensures)
	}
}

func TestRunnerBackend_NonZeroExit(t *testing.T) {
	stderr := "HTTP 403: policy_denied" + strings.Repeat("x", 3000)
	f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte("partial"), []byte(stderr), 1, nil
	}}
	res, err := NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "HTTP 403: policy_denied") || !strings.Contains(text, "exit status 1") {
		t.Fatalf("text = %q", text)
	}
	if strings.Count(text, "x") > 2048 || strings.Contains(text, "partial") {
		t.Fatalf("stderr not cut to 2 KiB, or stdout leaked: %d bytes", len(text))
	}

	// Silent failure: the exit status alone.
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) { return nil, nil, 4, nil }
	res, _ = NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if !res.IsError || !strings.Contains(resultText(t, res), "exit status 4") {
		t.Fatalf("silent failure = %+v", res)
	}
}

// gh prints the gateway's denial body on stdout and "gh: HTTP 403" after
// it (spike Check 2b). The agent hears that the network policy refused
// the request, with its method and path, and not the whole body.
func TestRunnerBackend_PolicyDenial(t *testing.T) {
	body := `{"error":"policy_denied","policy":"tools_github","rule":"POST /graphql","detail":"GraphQL operation blocked by endpoint policy","layer":"l7","protocol":"rest","method":"POST","path":"/graphql","host":"api.github.com","port":443,"binary":"/usr/bin/gh","next_steps":[]}`
	for name, out := range map[string]struct{ stdout, stderr string }{
		"gh":        {body, "gh: HTTP 403"},
		"same line": {body + "gh: HTTP 403", ""},
	} {
		f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
			return []byte(out.stdout), []byte(out.stderr), 1, nil
		}}
		res, err := NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
		if err != nil || !res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		text := resultText(t, res)
		if !strings.Contains(text, "network policy refused POST /graphql") || strings.Contains(text, "rule_missing") || strings.Contains(text, "tools_github") {
			t.Errorf("%s: text = %q", name, text)
		}
	}

	// No 403, or another error: not a denial.
	for name, out := range map[string]struct{ stdout, stderr string }{
		"no 403":       {body, "gh: HTTP 500"},
		"github's 403": {`{"message":"Resource not accessible by integration"}`, "gh: HTTP 403"},
	} {
		f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
			return []byte(out.stdout), []byte(out.stderr), 1, nil
		}}
		res, _ := NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
		if text := resultText(t, res); strings.Contains(text, "network policy") {
			t.Errorf("%s: read as a denial: %q", name, text)
		}
	}
}

func TestRunnerBackend_TimeoutAndCap(t *testing.T) {
	old := runnerCallTimeout
	runnerCallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { runnerCallTimeout = old })

	f := &fakeTools{version: "gh version 2.83.1", run: func(ctx context.Context, _ []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return nil, nil, 0, ctx.Err()
	}}
	start := time.Now()
	res, err := NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "timed out") {
		t.Fatalf("slow call: %v %+v", err, res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}

	big := bytes.Repeat([]byte("a"), 300<<10)
	big[256<<10-1] = 0xc3 // first byte of a two-byte rune cut at the limit
	big[256<<10] = 0xa9
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) { return big, nil, 0, nil }
	res, err = NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || res.IsError {
		t.Fatalf("big call: %v %+v", err, res)
	}
	text := resultText(t, res)
	body, marker, ok := strings.Cut(text, "\n[output cut at 256 KiB]")
	if !ok || marker != "" || len(body) != 256<<10-1 || strings.ContainsRune(body, 0xfffd) {
		t.Fatalf("cut: %d bytes, marker %v", len(body), ok)
	}

	// An output exactly at the limit is whole.
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) { return big[:256<<10-1], nil, 0, nil }
	res, _ = NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if text := resultText(t, res); strings.Contains(text, "[output cut") {
		t.Fatal("output under the limit was cut")
	}
}

func TestRunnerBackend_EnsureFailsCleanly(t *testing.T) {
	f := &fakeTools{version: "gh version 2.83.1", ensureErr: errors.New("create sandbox aimux-tools: image localhost/aimux-tools:2.103.3 not found in /Users/someone/.aimux")}
	b := NewCLIBackend("github", ghPack(t), f)
	res, err := b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "aimux sandbox setup") || strings.Contains(text, "/Users/") {
		t.Fatalf("text = %q", text)
	}
	if len(f.calls) != 0 {
		t.Fatalf("RunTool ran: %q", f.calls)
	}
	// The listing (the gateway's log, aimux tools status) gets the detail.
	if _, err := b.ListToolsRaw(context.Background()); err == nil || !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "aimux sandbox setup") {
		t.Fatalf("list err = %v", err)
	}
	// A failed start is retried on the next use.
	f.ensureErr = nil
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The token never reaches aimux: the sandbox sees only OpenShell's
// placeholder (openshell:resolve:env:...), and the proxy swaps the real
// value in on the wire. So output passes through as the runner returned
// it, a placeholder stays a placeholder, and nothing on this path reads a
// secret store.
func TestRunnerBackend_NoCredentialInOutput(t *testing.T) {
	const placeholder = "openshell:resolve:env:GH_TOKEN"
	out := "token in env: " + placeholder
	f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte(out), []byte("debug: Authorization: token " + placeholder), 0, nil
	}}
	res, err := NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || res.IsError || resultText(t, res) != out {
		t.Fatalf("call: %v %+v", err, res)
	}
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, []byte("gh: Bad credentials for " + placeholder), 1, nil
	}
	res, _ = NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if !strings.Contains(resultText(t, res), placeholder) {
		t.Fatalf("placeholder changed: %q", resultText(t, res))
	}
	// No secret store and no host command on this path.
	file, err := parser.ParseFile(token.NewFileSet(), "runner.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if strings.Contains(p, "keyring") || p == "os/exec" {
			t.Errorf("runner.go imports %s", p)
		}
	}
}

// cli and http-openapi sources both start in the runner when it is bound.
func TestReadonly_StartsRunnerSources(t *testing.T) {
	f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte("ok"), nil, 0, nil
	}}
	var bound []string
	var mu sync.Mutex
	cfg := ReadonlyConfig{HTTP: map[string]HTTPServerConfig{}, Notes: map[string]string{},
		Runner: map[string]toolreg.Source{
			"github": {Type: toolreg.TypeCLI, Pack: "gh"},
			"api":    githubSpec(t),
		},
		Allow: Allowlist{"github": {"gh_repo_view"}, "api": {"api_meta_get_zen"}},
		Tools: func(source string) ToolRunner {
			mu.Lock()
			bound = append(bound, source)
			mu.Unlock()
			return f
		},
	}
	cat, errs := ReadonlyCatalog(context.Background(), cfg)
	if errs["github"] != nil || len(cat.Servers["github"].Tools) == 0 {
		t.Fatalf("catalog: %v %d tools", errs, len(cat.Servers["github"].Tools))
	}
	if errs["api"] != nil || len(cat.Servers["api"].Tools) != 2 {
		t.Fatalf("api: %v %d tools", errs["api"], len(cat.Servers["api"].Tools))
	}

	servers, pool, errs, err := BuildReadonly(context.Background(), cfg, passPins{}, nil)
	if err != nil || errs["github"] != nil || servers["github"] == nil {
		t.Fatalf("build: %v %v", err, errs)
	}
	t.Cleanup(pool.Close)
	if servers["api"] == nil || errs["api"] != nil {
		t.Fatalf("http-openapi not served: %v", errs["api"])
	}
	if !slices.Contains(bound, "github") || !slices.Contains(bound, "api") {
		t.Fatalf("bound = %v", bound)
	}
}

// A runner that went away after the first start: the call re-Ensures once
// and retries once.
func TestRunnerBackend_ReEnsuresWhenRunnerGone(t *testing.T) {
	calls := 0
	f := &fakeTools{version: "gh version 2.83.1"}
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		calls++
		if calls == 1 {
			return nil, nil, 0, fmt.Errorf("aimux-tools: %w", ErrRunnerNotReady)
		}
		return []byte("ok"), nil, 0, nil
	}
	b := NewCLIBackend("github", ghPack(t), f)
	res, err := b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || res.IsError || resultText(t, res) != "ok" {
		t.Fatalf("call: %v %+v", err, res)
	}
	if f.ensures != 2 || calls != 2 {
		t.Fatalf("ensures %d, tool runs %d; want 2 and 2", f.ensures, calls)
	}

	// Still not ready after the retry: an error result, no third run.
	calls = 0
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		calls++
		return nil, nil, 0, ErrRunnerNotReady
	}
	res, err = b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "aimux sandbox setup") || calls != 2 {
		t.Fatalf("still gone: %v %+v, %d runs", err, res, calls)
	}

	// The re-Ensure fails: an error result naming setup, no retry.
	f = &fakeTools{version: "gh version 2.83.1"}
	calls = 0
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		calls++
		f.ensureErr = errors.New("create sandbox aimux-tools: image missing")
		return nil, nil, 0, ErrRunnerNotReady
	}
	b = NewCLIBackend("github", ghPack(t), f)
	res, err = b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "aimux sandbox setup") || calls != 1 || f.ensures != 2 {
		t.Fatalf("ensure fails: %v %+v, %d runs, %d ensures", err, res, calls, f.ensures)
	}

	// gh's own exit 1 on a Ready runner stays a tool error: no Ensure.
	f = &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, []byte("not found"), 1, nil
	}}
	res, _ = NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if !res.IsError || f.ensures != 1 {
		t.Fatalf("tool error: %+v, %d ensures", res, f.ensures)
	}
}

// A runner failure that is not an exit status reaches the agent as a
// generic error result; the detail (openshell output, host paths) goes to
// the log only.
func TestRunnerBackend_RunFailureIsGeneric(t *testing.T) {
	var logged []string
	old := runnerLog
	runnerLog = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { runnerLog = old })
	f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, nil, 0, errors.New("openshell: dial /Users/someone/.openshell/gateway.sock: refused")
	}}
	res, err := NewCLIBackend("github", ghPack(t), f).CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if text := resultText(t, res); strings.Contains(text, "/Users/") || !strings.Contains(text, "the tool runner failed; see the aimux log") {
		t.Fatalf("text = %q", text)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "gateway.sock") || !strings.Contains(logged[0], "gh_repo_view") {
		t.Fatalf("logged = %q", logged)
	}
}

// One next step per situation: a runner that was never started names
// approve or serve; a failed Ensure names setup. Never both.
func TestRunnerBackend_OneHintPerSituation(t *testing.T) {
	cases := map[string]struct {
		err        error
		want, deny string
	}{
		"not started":   {&RunnerError{Err: errors.New("tool runner not started"), Next: "run aimux tools approve <set> (a set of github) or start aimux remote serve"}, "aimux tools approve", "aimux sandbox setup"},
		"ensure failed": {errors.New("create sandbox aimux-tools: image missing"), "aimux sandbox setup", "aimux tools approve"},
	}
	for name, c := range cases {
		f := &fakeTools{version: "gh version 2.83.1", ensureErr: c.err}
		b := NewCLIBackend("github", ghPack(t), f)
		res, _ := b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
		text := resultText(t, res)
		_, lerr := b.ListToolsRaw(context.Background())
		for _, s := range []string{text, lerr.Error()} {
			if !strings.Contains(s, c.want) || strings.Contains(s, c.deny) {
				t.Errorf("%s: %q", name, s)
			}
		}
	}
}

// The retry's Ensure is bounded: a runner stuck short of Ready cannot hold
// a tool call for the 5 minutes Ensure would wait.
func TestRunnerBackend_RetryEnsureBounded(t *testing.T) {
	old := runnerRetryEnsure
	runnerRetryEnsure = 50 * time.Millisecond
	t.Cleanup(func() { runnerRetryEnsure = old })
	f := &blockingTools{fakeTools: fakeTools{version: "gh version 2.83.1"}}
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) { return nil, nil, 0, ErrRunnerNotReady }
	b := NewCLIBackend("github", ghPack(t), f)
	if _, err := b.ListToolsRaw(context.Background()); err != nil { // first start succeeds
		t.Fatal(err)
	}
	f.block.Store(true)
	start := time.Now()
	res, err := b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "not ready") {
		t.Fatalf("call: %v %+v", err, res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

// blockingTools is fakeTools whose Ensure, once block is set, waits for
// its context (a runner that never becomes Ready).
type blockingTools struct {
	fakeTools
	block atomic.Bool
}

func (b *blockingTools) Ensure(ctx context.Context) error {
	if b.block.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return b.fakeTools.Ensure(ctx)
}

// aimux-tools rebuilt with another gh under a running backend: the next
// listing carries the new version (so pins see the tools as changed), and
// calls are refused until the tools are approved again.
func TestRunnerBackend_VersionChangeIsSeen(t *testing.T) {
	f := &fakeTools{version: "gh version 2.83.1", run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte("ok"), nil, 0, nil
	}}
	b := NewCLIBackend("github", ghPack(t), f)
	call := func() *mcp.CallToolResult {
		res, err := b.CallTool(context.Background(), runnerReq("gh_repo_view", map[string]any{"repo": "cli/cli"}))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.version = "gh version 2.84.0"
	tools, err := b.ListToolsRaw(context.Background())
	if err != nil || !strings.Contains(string(tools[0]), `"x-aimux-binary":"2.84.0"`) {
		t.Fatalf("listing after the change: %v %s", err, tools[0])
	}
	runs := len(f.toolCalls())
	if res := call(); !res.IsError || !strings.Contains(resultText(t, res), "changed") || !strings.Contains(resultText(t, res), "aimux tools approve") {
		t.Fatalf("call after the change = %+v", res)
	}
	if len(f.toolCalls()) != runs {
		t.Fatal("the changed binary ran")
	}
	// Back to the started version: calls run again.
	f.version = "gh version 2.83.1"
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := call(); res.IsError {
		t.Fatalf("call after the rollback = %+v", res)
	}

	// Found on the retry path: the runner came back with another gh.
	f = &fakeTools{version: "gh version 2.83.1"}
	n := 0
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		n++
		f.version = "gh version 2.84.0"
		return nil, nil, 0, ErrRunnerNotReady
	}
	b = NewCLIBackend("github", ghPack(t), f)
	if res := call(); !res.IsError || !strings.Contains(resultText(t, res), "changed") || n != 1 {
		t.Fatalf("retry with a new binary = %+v, %d runs", res, n)
	}
}

// A listing that finds the runner gone recovers as a call does: one
// bounded Ensure, then the version again. A read-only runner (the probe)
// answers that Ensure with "not started", and the listing reports it.
func TestRunnerBackend_ListingRecovers(t *testing.T) {
	gone := false
	f := &fakeTools{version: "gh version 2.83.1"}
	b := NewCLIBackend("github", ghPack(t), &goneTools{fakeTools: f, gone: &gone})
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone = true
	tools, err := b.ListToolsRaw(context.Background())
	if err != nil || len(tools) == 0 || f.ensures != 2 {
		t.Fatalf("listing after the runner went: %v, %d tools, %d ensures", err, len(tools), f.ensures)
	}

	// Ensure fails: the not-ready error, with one hint.
	for name, c := range map[string]struct {
		err        error
		want, deny string
	}{
		"setup":       {errors.New("create sandbox aimux-tools: image missing"), "aimux sandbox setup", "aimux tools approve"},
		"not started": {&RunnerError{Err: errors.New("tool runner not started"), Next: "run aimux tools approve <set> (a set of github) or start aimux remote serve"}, "aimux tools approve", "aimux sandbox setup"},
	} {
		gone = false
		f = &fakeTools{version: "gh version 2.83.1"}
		b = NewCLIBackend("github", ghPack(t), &goneTools{fakeTools: f, gone: &gone})
		if _, err := b.ListToolsRaw(context.Background()); err != nil {
			t.Fatal(err)
		}
		gone, f.ensureErr = true, c.err
		_, err := b.ListToolsRaw(context.Background())
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), c.deny) || f.ensures != 2 {
			t.Errorf("%s: %v, %d ensures", name, err, f.ensures)
		}
	}
}

// goneTools is fakeTools whose RunTool says the runner is not ready while
// gone is set and Ensure has not run since.
type goneTools struct {
	*fakeTools
	gone *bool
}

func (g *goneTools) Ensure(ctx context.Context) error {
	err := g.fakeTools.Ensure(ctx)
	if err == nil {
		*g.gone = false
	}
	return err
}

func (g *goneTools) RunTool(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	if *g.gone {
		return nil, nil, 0, ErrRunnerNotReady
	}
	return g.fakeTools.RunTool(ctx, argv)
}
