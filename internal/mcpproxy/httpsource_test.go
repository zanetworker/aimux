package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// githubSpec is an http-openapi source for the mixed.yaml fixture.
func githubSpec(t *testing.T) toolreg.Source {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "httpsource", "testdata", "mixed.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "github.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
		t.Fatal(err)
	}
	return toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: path, BaseURL: "https://api.github.com"}
}

// statusLine is the line curl's --write-out adds to stderr.
func statusLine(code int, policy string) string {
	return fmt.Sprintf("aimux-http %d %s\n", code, policy)
}

var issuesArgs = map[string]any{"owner": "cli", "repo": "cli", "state": "open"}

func TestHTTPSourceBackend_ListsSpecTools(t *testing.T) {
	src := githubSpec(t)
	f := &fakeTools{}
	got, err := NewHTTPSourceBackend("github", src, f).ListToolsRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ops, err := httpsource.SourceOperations(src)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := httpsource.Tools("github", src.BaseURL, ops)
	if len(got) != 2 || len(got) != len(want) {
		t.Fatalf("%d tools, want %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("tool %d = %s\nwant %s", i, got[i], want[i])
		}
	}
	// The tools come from the local spec: listing needs no runner.
	if f.ensures != 0 || len(f.calls) != 0 {
		t.Fatalf("listing used the runner: ensures %d calls %q", f.ensures, f.calls)
	}
}

func TestHTTPSourceBackend_CurlArgv(t *testing.T) {
	f := &fakeTools{run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte(`[{"number":1}]`), []byte(statusLine(200, "")), 0, nil
	}}
	res, err := NewHTTPSourceBackend("github", githubSpec(t), f).CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || res.IsError || resultText(t, res) != `[{"number":1}]` {
		t.Fatalf("call: %v %+v", err, res)
	}
	want := []string{"/usr/bin/curl", "-q", "-sS", "--fail-with-body", "--max-time", "25", "--proto", "=https", "--globoff",
		"-H", "Accept: application/json", "-w", "%{stderr}aimux-http %{http_code} %header{x-openshell-policy}\\n",
		"https://api.github.com/repos/cli/cli/issues?state=open"}
	calls := f.toolCalls()
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("argv = %q\nwant   %q", calls, want)
	}
	if f.ensures != 1 {
		t.Fatalf("ensures = %d", f.ensures)
	}
}

// Arguments are checked before anything runs: an unknown tool or key, a
// value that would leave its path segment, a missing required parameter.
func TestHTTPSourceBackend_RefusesBadArgsWithoutRunning(t *testing.T) {
	f := &fakeTools{}
	b := NewHTTPSourceBackend("github", githubSpec(t), f)
	for name, req := range map[string]struct {
		tool string
		args map[string]any
	}{
		"unknown tool":    {"github_issues_create", issuesArgs},
		"other prefix":    {"gitlab_issues_list_for_repo", issuesArgs},
		"unknown key":     {"github_issues_list_for_repo", map[string]any{"owner": "a", "repo": "b", "method": "POST"}},
		"dot segment":     {"github_issues_list_for_repo", map[string]any{"owner": "..", "repo": "b"}},
		"missing":         {"github_issues_list_for_repo", map[string]any{"owner": "a"}},
		"leading dash":    {"github_issues_list_for_repo", map[string]any{"owner": "-K", "repo": "b"}},
		"enum miss":       {"github_issues_list_for_repo", map[string]any{"owner": "a", "repo": "b", "state": "merged"}},
		"integer as text": {"github_issues_list_for_repo", map[string]any{"owner": "a", "repo": "b", "per_page": "5"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := b.CallTool(context.Background(), runnerReq(req.tool, req.args))
			var re *RequestError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want a RequestError", err)
			}
		})
	}
	if len(f.calls) != 0 || f.ensures != 0 {
		t.Fatalf("ran: %q, ensures %d", f.calls, f.ensures)
	}
}

// A 403 with X-OpenShell-Policy and a policy_denied body is the network
// policy; GitHub's own 403 (no header) is an HTTP error.
func TestHTTPSourceBackend_PolicyDenial(t *testing.T) {
	body := `{"error":"policy_denied","policy":"tools_github","rule":"GET /repos/cli/cli/issues","detail":"GET /repos/cli/cli/issues not permitted by policy","method":"GET","path":"/repos/cli/cli/issues"}`
	f := &fakeTools{run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte(body), []byte("curl: (22) The requested URL returned error: 403\n" + statusLine(403, "tools_github")), 22, nil
	}}
	b := NewHTTPSourceBackend("github", githubSpec(t), f)
	res, err := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || !res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if text := resultText(t, res); text != "github_issues_list_for_repo: the network policy refused GET /repos/cli/cli/issues (only approved read operations may run)" {
		t.Fatalf("text = %q", text)
	}

	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte(`{"message":"API rate limit exceeded"}`), []byte("curl: (22) The requested URL returned error: 403\n" + statusLine(403, "")), 22, nil
	}
	res, _ = b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if text := resultText(t, res); !res.IsError || strings.Contains(text, "network policy") || !strings.Contains(text, "HTTP 403") || !strings.Contains(text, "rate limit") {
		t.Fatalf("upstream 403: %q", text)
	}
	// A header without the policy_denied body is not a denial either.
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte(`{"error":"other"}`), []byte(statusLine(403, "tools_github")), 22, nil
	}
	res, _ = b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if text := resultText(t, res); strings.Contains(text, "network policy") {
		t.Fatalf("header alone: %q", text)
	}
}

func TestHTTPSourceBackend_HTTPAndCurlErrors(t *testing.T) {
	cases := map[string]struct {
		out, stderr string
		exit        int
		want        string
	}{
		"404":     {`{"message":"Not Found"}`, "curl: (22) The requested URL returned error: 404\n" + statusLine(404, ""), 22, `github_issues_list_for_repo: HTTP 404: {"message":"Not Found"}`},
		"connect": {"", "curl: (7) Failed to connect to api.github.com port 443\n" + statusLine(0, ""), 7, "github_issues_list_for_repo: request failed (curl exit 7): curl: (7) Failed to connect to api.github.com port 443"},
		"timeout": {"", "curl: (28) Operation timed out after 25001 milliseconds\n" + statusLine(0, ""), 28, "github_issues_list_for_repo: timed out after 25s"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeTools{run: func(context.Context, []string) ([]byte, []byte, int, error) {
				return []byte(c.out), []byte(c.stderr), c.exit, nil
			}}
			res, err := NewHTTPSourceBackend("github", githubSpec(t), f).CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
			if err != nil || !res.IsError || resultText(t, res) != c.want {
				t.Fatalf("call: %v %q", err, resultText(t, res))
			}
		})
	}
}

func TestHTTPSourceBackend_TimeoutAndCap(t *testing.T) {
	old := runnerCallTimeout
	runnerCallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { runnerCallTimeout = old })
	f := &fakeTools{run: func(ctx context.Context, _ []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return nil, nil, 0, ctx.Err()
	}}
	res, err := NewHTTPSourceBackend("github", githubSpec(t), f).CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "timed out") {
		t.Fatalf("slow call: %v %+v", err, res)
	}

	big := bytes.Repeat([]byte("a"), 300<<10)
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		return big, []byte(statusLine(200, "")), 0, nil
	}
	res, err = NewHTTPSourceBackend("github", githubSpec(t), f).CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || res.IsError {
		t.Fatalf("big call: %v %+v", err, res)
	}
	if text := resultText(t, res); len(text) != 256<<10+len(runnerCutMarker) || !strings.HasSuffix(text, runnerCutMarker) {
		t.Fatalf("cut: %d bytes", len(text))
	}
}

// The shared recover step: a runner gone since the first call is ensured
// once and the request retried once.
func TestHTTPSourceBackend_ReEnsuresWhenRunnerGone(t *testing.T) {
	calls := 0
	f := &fakeTools{}
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		calls++
		if calls == 1 {
			return nil, nil, 0, ErrRunnerNotReady
		}
		return []byte("ok"), []byte(statusLine(200, "")), 0, nil
	}
	b := NewHTTPSourceBackend("github", githubSpec(t), f)
	res, err := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || res.IsError || resultText(t, res) != "ok" || f.ensures != 2 || calls != 2 {
		t.Fatalf("call: %v %+v, ensures %d runs %d", err, res, f.ensures, calls)
	}
	// Still gone: an error result, no third run.
	calls = 0
	f.run = func(context.Context, []string) ([]byte, []byte, int, error) {
		calls++
		return nil, nil, 0, ErrRunnerNotReady
	}
	res, _ = b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if !res.IsError || !strings.Contains(resultText(t, res), "not ready") || calls != 2 {
		t.Fatalf("still gone: %+v, %d runs", res, calls)
	}
	// An Ensure that fails: the agent gets the next step, no host detail.
	f = &fakeTools{ensureErr: errors.New("create sandbox aimux-tools: image missing in /Users/someone/.aimux")}
	res, _ = NewHTTPSourceBackend("github", githubSpec(t), f).CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if text := resultText(t, res); !res.IsError || !strings.Contains(text, "aimux sandbox setup") || strings.Contains(text, "/Users/") || len(f.calls) != 0 {
		t.Fatalf("ensure fails: %q, calls %q", text, f.calls)
	}
}

// A missing spec is that source's error, naming the source and the path;
// the agent gets a generic error, the log the detail.
func TestHTTPSourceBackend_SpecError(t *testing.T) {
	var logged []string
	old := runnerLog
	runnerLog = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { runnerLog = old })
	src := githubSpec(t)
	src.Spec = filepath.Join(t.TempDir(), "gone.yaml")
	b := NewHTTPSourceBackend("github", src, &fakeTools{})
	if _, err := b.ListToolsRaw(context.Background()); err == nil || !strings.Contains(err.Error(), "github") || !strings.Contains(err.Error(), src.Spec) {
		t.Fatalf("list err = %v", err)
	}
	res, err := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || !res.IsError || strings.Contains(resultText(t, res), src.Spec) {
		t.Fatalf("call: %v %q", err, resultText(t, res))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], src.Spec) {
		t.Fatalf("logged = %q", logged)
	}
}

// A source that names a credential serves nothing yet, and says why.
func TestHTTPSourceBackend_CredentialRefused(t *testing.T) {
	src := githubSpec(t)
	src.Credential = "aimux-github" // #nosec G101 -- a provider name, not a credential
	f := &fakeTools{}
	b := NewHTTPSourceBackend("github", src, f)
	if _, err := b.ListToolsRaw(context.Background()); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("list err = %v", err)
	}
	res, err := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "credential") || f.ensures != 0 || len(f.calls) != 0 {
		t.Fatalf("call: %v %+v, ensures %d", err, res, f.ensures)
	}
}

// rewriteSpec replaces src's spec with replace applied, at the given
// modification time.
func rewriteSpec(t *testing.T, src toolreg.Source, old, replacement string, mod time.Time) {
	t.Helper()
	b, err := os.ReadFile(src.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(old)) {
		t.Fatalf("spec lacks %q", old)
	}
	if err := os.WriteFile(src.Spec, bytes.Replace(b, []byte(old), []byte(replacement), 1), 0o600); err != nil { // #nosec G703 -- a spec under t.TempDir
		t.Fatal(err)
	}
	if err := os.Chtimes(src.Spec, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// The parsed spec is kept until the file's modification time or size
// changes.
func TestHTTPSourceBackend_CachesSpec(t *testing.T) {
	src := githubSpec(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src.Spec, t0, t0); err != nil {
		t.Fatal(err)
	}
	b := NewHTTPSourceBackend("github", src, &fakeTools{})
	names := func() []string {
		t.Helper()
		raws, err := b.ListToolsRaw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range raws {
			var tool struct{ Name string }
			_ = json.Unmarshal(r, &tool)
			out = append(out, tool.Name)
		}
		return out
	}
	if got := names(); !slices.Contains(got, "github_meta_get_zen") {
		t.Fatalf("tools = %q", got)
	}
	// Same size, same time: the cached parse is used (the rename is not seen).
	rewriteSpec(t, src, "meta/get-zen", "meta/get-zzz", t0)
	if got := names(); !slices.Contains(got, "github_meta_get_zen") {
		t.Fatalf("cache missed: %q", got)
	}
	// A new modification time: read again.
	rewriteSpec(t, src, "meta/get-zzz", "meta/get-yyy", t0.Add(time.Minute))
	if got := names(); !slices.Contains(got, "github_meta_get_yyy") {
		t.Fatalf("cache kept a stale spec: %q", got)
	}
}

// A call whose operation changed since the last listing (the gateway's
// pin check listed it) is refused, as a cli tool whose binary changed is.
// A description change alone is not a change.
func TestHTTPSourceBackend_ChangedSinceListingRefused(t *testing.T) {
	src := githubSpec(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	f := &fakeTools{run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte("ok"), []byte(statusLine(200, "")), 0, nil
	}}
	b := NewHTTPSourceBackend("github", src, f)
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	rewriteSpec(t, src, "summary: List repository issues", "summary: Lists the issues", t0)
	res, err := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || res.IsError {
		t.Fatalf("description change refused: %v %q", err, resultText(t, res))
	}
	rewriteSpec(t, src, "/repos/{owner}/{repo}/issues:", "/repos/{owner}/{repo}/pulls:", t0.Add(time.Minute))
	calls := len(f.toolCalls())
	res, err = b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if err != nil || !res.IsError {
		t.Fatalf("changed call: %v %+v", err, res)
	}
	text := resultText(t, res)
	for _, want := range []string{"changed since it was pinned", "aimux tools diff", "aimux tools approve"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q lacks %q", text, want)
		}
	}
	if len(f.toolCalls()) != calls {
		t.Fatal("a changed operation ran")
	}
	// A later relist (the gateway's background relist, or a pin check that
	// fails) lists the new definition, but does not make it the one calls
	// run against: still refused.
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res, _ := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs)); !res.IsError || !strings.Contains(resultText(t, res), "changed since it was pinned") {
		t.Fatalf("after relisting: %q", resultText(t, res))
	}
	// Approving makes a new snapshot, which restarts the backend: the new
	// backend's first listing is its baseline, and the call runs.
	b = NewHTTPSourceBackend("github", src, f)
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res, _ := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs)); res.IsError {
		t.Fatalf("after restart: %q", resultText(t, res))
	}
}

// A call that arrives before any listing lists first, so it too is checked
// against a baseline from then on.
func TestHTTPSourceBackend_CallBeforeListingListsFirst(t *testing.T) {
	src := githubSpec(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	f := &fakeTools{run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte("ok"), []byte(statusLine(200, "")), 0, nil
	}}
	b := NewHTTPSourceBackend("github", src, f)
	if res, err := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs)); err != nil || res.IsError {
		t.Fatalf("first call: %v %q", err, resultText(t, res))
	}
	rewriteSpec(t, src, "/repos/{owner}/{repo}/issues:", "/repos/{owner}/{repo}/pulls:", t0)
	calls := len(f.toolCalls())
	res, _ := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if !res.IsError || !strings.Contains(resultText(t, res), "changed since it was pinned") || len(f.toolCalls()) != calls {
		t.Fatalf("changed after an unlisted first call: %q", resultText(t, res))
	}
}

// Parameter order is not a change (the pinned definition does not depend
// on it); a parameter's type is.
func TestHTTPSourceBackend_ParamOrderIsNotAChange(t *testing.T) {
	src := githubSpec(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	f := &fakeTools{run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return []byte("ok"), []byte(statusLine(200, "")), 0, nil
	}}
	b := NewHTTPSourceBackend("github", src, f)
	before, err := b.ListToolsRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reordered := `        - name: per_page
          in: query
          schema: {type: integer}
        - name: state
          in: query
          schema: {type: string, enum: [open, closed, all]}
`
	rewriteSpec(t, src, `        - name: state
          in: query
          schema: {type: string, enum: [open, closed, all]}
        - name: per_page
          in: query
          schema: {type: integer}
`, reordered, t0)
	if res, _ := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs)); res.IsError {
		t.Fatalf("reordered parameters refused: %q", resultText(t, res))
	}
	after, err := b.ListToolsRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before[0], after[0]) {
		t.Fatalf("tool definition depends on parameter order:\n%s\n%s", before[0], after[0])
	}
	rewriteSpec(t, src, "schema: {type: integer}", "schema: {type: boolean}", t0.Add(time.Minute))
	res, _ := b.CallTool(context.Background(), runnerReq("github_issues_list_for_repo", issuesArgs))
	if !res.IsError || !strings.Contains(resultText(t, res), "changed since it was pinned") {
		t.Fatalf("type change: %q", resultText(t, res))
	}
}
