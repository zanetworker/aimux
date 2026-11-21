package sandboxlaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// fakeRunner records every openshell call and answers from respond.
type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	stdins  []string
	respond func(argv []string) (string, error)
}

func (f *fakeRunner) run(_ context.Context, stdin io.Reader, argv ...string) (io.ReadCloser, error) {
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	f.stdins = append(f.stdins, in)
	f.mu.Unlock()
	out, err := f.respond(argv)
	return &fakeOut{Reader: strings.NewReader(out), err: err}, nil
}

type fakeOut struct {
	io.Reader
	err error
}

func (o *fakeOut) Close() error { return o.err }

// verbs returns "sandbox list", "sandbox create", ... per call.
func (f *fakeRunner) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var v []string
	for _, c := range f.calls {
		v = append(v, c[3]+" "+c[4])
	}
	return v
}

func (f *fakeRunner) call(verb string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c[3]+" "+c[4] == verb {
			return c
		}
	}
	return nil
}

func compiled(digest string) profile.Compiled {
	return profile.Compiled{Profile: "work-readonly", Digest: digest, PolicyDigest: digest, Sandbox: "aimux-work-readonly",
		Image: profile.SandboxImage, Provider: profile.SandboxProvider, Providers: []string{profile.SandboxProvider}, PolicyYAML: []byte("version: 1\n")}
}

// newTestCLI does not sleep between polls.
func newTestCLI(f *fakeRunner, dir string) Backend {
	b := NewCLIBackend(f.run, dir)
	b.(*cliBackend).pollEvery = 0
	return b
}

const listHeader = "NAME  CREATED  PHASE\n"

func TestCLIEnsure_CreatesWithPolicyAndProvider(t *testing.T) {
	dir := t.TempDir()
	lists := 0
	f := &fakeRunner{respond: func(argv []string) (string, error) {
		if argv[3] == "sandbox" && argv[4] == "list" {
			lists++
			switch lists {
			case 1:
				return listHeader + "wingman-work  2026-11-10 11:10:34  Ready\n", nil
			case 2:
				return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  Provisioning\n", nil
			}
			return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  Ready\n", nil
		}
		return "", nil
	}}
	b := newTestCLI(f, dir)
	if err := b.Ensure(context.Background(), compiled("sha256:one")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	policy := filepath.Join(dir, "aimux-work-readonly.policy.yaml")
	want := []string{"openshell", "-g", "local", "sandbox", "create", "--name", "aimux-work-readonly",
		"--from", "localhost/wingman-sandbox:2.1.319", "--policy", policy, "--provider", "wingman-claude",
		"--no-auto-providers", "--no-tty", "--detach", "--", "sleep", "infinity"}
	if got := f.call("sandbox create"); !slices.Equal(got, want) {
		t.Fatalf("create = %q\nwant     %q", got, want)
	}
	if v := f.verbs(); !slices.Equal(v, []string{"sandbox list", "sandbox create", "sandbox list", "sandbox list"}) {
		t.Fatalf("calls = %v", v)
	}
	if b, err := os.ReadFile(policy); err != nil || string(b) != "version: 1\n" { //nolint:gosec // G304: policy is a path in the test temp dir
		t.Fatalf("policy file = %q, %v", b, err)
	}
	for _, p := range []string{policy, filepath.Join(dir, "aimux-work-readonly.digest")} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want 0600", p, st, err)
		}
	}
	if d, _ := os.ReadFile(filepath.Join(dir, "aimux-work-readonly.digest")); string(d) != "sha256:one" { //nolint:gosec // G304: path is in the test temp dir
		t.Fatalf("digest = %q", d)
	}
}

// The runner gets one --provider per compiled provider (and none of the
// model provider); no providers is no --provider flag.
func TestEnsure_PassesEveryProvider(t *testing.T) {
	for _, tc := range []struct {
		providers []string
		want      []string
	}{
		{[]string{"aimux-github", "aimux-jira"}, []string{"--provider", "aimux-github", "--provider", "aimux-jira"}},
		{nil, nil},
	} {
		lists := 0
		f := &fakeRunner{respond: func(argv []string) (string, error) {
			if argv[4] == "list" {
				if lists++; lists > 1 {
					return listHeader + "aimux-tools  2026-11-11 10:00:00  Ready\n", nil
				}
				return listHeader, nil
			}
			return "", nil
		}}
		dir := t.TempDir()
		c := profile.Compiled{Sandbox: profile.RunnerSandbox, Image: profile.RunnerImage, Providers: tc.providers,
			PolicyDigest: "sha256:r", PolicyYAML: []byte("version: 1\n")}
		if err := newTestCLI(f, dir).Ensure(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		want := append([]string{"openshell", "-g", "local", "sandbox", "create", "--name", "aimux-tools",
			"--from", profile.RunnerImage, "--policy", filepath.Join(dir, "aimux-tools.policy.yaml")}, tc.want...)
		want = append(want, "--no-auto-providers", "--no-tty", "--detach", "--", "sleep", "infinity")
		got := f.call("sandbox create")
		if !slices.Equal(got, want) {
			t.Fatalf("create = %q\nwant     %q", got, want)
		}
		if slices.Contains(got, profile.SandboxProvider) {
			t.Fatal("runner got the model provider")
		}
	}
}

// phaseSeq answers each sandbox list with the next phase of
// aimux-work-readonly ("" lists it as gone), repeating the last one.
func phaseSeq(phases ...string) *fakeRunner {
	i := 0
	return &fakeRunner{respond: func(argv []string) (string, error) {
		if argv[4] != "list" {
			return "", nil
		}
		p := phases[min(i, len(phases)-1)]
		i++
		if p == "" {
			return listHeader + "wingman-work  2026-11-10 11:10:34  Ready\n", nil
		}
		return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  " + p + "\n", nil
	}}
}

// A dead sandbox (Error, Failed, Completed) holds nothing to keep: Ensure
// deletes it, waits until it is gone, and creates it from the current
// policy, with no policy set on the new one.
func TestCLIEnsure_ReplacesDeadSandbox(t *testing.T) {
	for _, dead := range []string{"Error", "Failed", "Completed"} {
		t.Run(dead, func(t *testing.T) {
			dir := t.TempDir()
			digest := filepath.Join(dir, "aimux-work-readonly.digest")
			if err := os.WriteFile(digest, []byte("sha256:old"), 0o600); err != nil {
				t.Fatal(err)
			}
			f := phaseSeq(dead, "Deleting", "", "Provisioning", "Ready")
			if err := newTestCLI(f, dir).Ensure(context.Background(), compiled("sha256:one")); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			want := []string{"sandbox list", "sandbox delete", "sandbox list", "sandbox list", "sandbox create", "sandbox list", "sandbox list"}
			if v := f.verbs(); !slices.Equal(v, want) {
				t.Fatalf("calls = %v\nwant    %v", v, want)
			}
			if got := f.call("sandbox delete"); !slices.Equal(got[5:], []string{"aimux-work-readonly"}) {
				t.Fatalf("delete = %q", got)
			}
			if d, _ := os.ReadFile(digest); string(d) != "sha256:one" { //nolint:gosec // G304: path is in the test temp dir
				t.Fatalf("digest = %q, want the new one", d)
			}
		})
	}
}

// One replacement per Ensure: a new sandbox that dies too fails the turn
// with its phase named, and is not deleted again.
func TestCLIEnsure_DeadAfterReplaceFails(t *testing.T) {
	f := phaseSeq("Error", "", "Error")
	err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:one"))
	if err == nil || !strings.Contains(err.Error(), "phase Error") {
		t.Fatalf("err = %v, want the Error phase named", err)
	}
	if n := slices.Index(f.verbs(), "sandbox delete"); n < 0 || slices.Contains(f.verbs()[n+1:], "sandbox delete") {
		t.Fatalf("calls = %v, want exactly one delete", f.verbs())
	}
}

// Stopped is someone's choice: Ensure leaves it and says so.
func TestCLIEnsure_StoppedNotReplaced(t *testing.T) {
	f := phaseSeq("Stopped")
	err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:one"))
	if err == nil || !strings.Contains(err.Error(), "phase Stopped") {
		t.Fatalf("err = %v, want the Stopped phase named", err)
	}
	if f.call("sandbox delete") != nil || f.call("sandbox create") != nil {
		t.Fatalf("calls = %v, want no delete or create", f.verbs())
	}
}

// A delete that never finishes fails instead of creating over it.
func TestCLIEnsure_DeleteNeverFinishes(t *testing.T) {
	f := phaseSeq("Error", "Deleting")
	err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:one"))
	if err == nil || !strings.Contains(err.Error(), "still") {
		t.Fatalf("err = %v, want the stuck delete named", err)
	}
	if f.call("sandbox create") != nil {
		t.Fatalf("calls = %v, want no create", f.verbs())
	}
}

var errExit = errors.New("exit status 1")

func readyRunner(policySet func() error) *fakeRunner {
	return &fakeRunner{respond: func(argv []string) (string, error) {
		switch argv[3] + " " + argv[4] {
		case "sandbox list":
			return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  Ready\n", nil
		case "policy set":
			return "", policySet()
		}
		return "", nil
	}}
}

func TestCLIEnsure_UpdatesOnlyWhenDigestChanged(t *testing.T) {
	dir := t.TempDir()
	f := readyRunner(func() error { return nil })
	b := newTestCLI(f, dir)
	digest := filepath.Join(dir, "aimux-work-readonly.digest")
	if err := os.WriteFile(digest, []byte("sha256:one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(context.Background(), compiled("sha256:one")); err != nil {
		t.Fatal(err)
	}
	if v := f.verbs(); slices.Contains(v, "policy set") || slices.Contains(v, "sandbox create") {
		t.Fatalf("same digest changed the sandbox: %v", v)
	}
	if err := b.Ensure(context.Background(), compiled("sha256:two")); err != nil {
		t.Fatal(err)
	}
	want := []string{"openshell", "-g", "local", "policy", "set", "--policy",
		filepath.Join(dir, "aimux-work-readonly.policy.yaml"), "--yes", "--wait", "aimux-work-readonly"}
	if got := f.call("policy set"); !slices.Equal(got, want) {
		t.Fatalf("policy set = %q, want %q", got, want)
	}
	if d, _ := os.ReadFile(digest); string(d) != "sha256:two" { //nolint:gosec // G304: path is in the test temp dir
		t.Fatalf("digest = %q, want sha256:two", d)
	}
}

func TestCLIEnsure_UnknownDigestUpdates(t *testing.T) {
	f := readyRunner(func() error { return nil })
	if err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:one")); err != nil {
		t.Fatal(err)
	}
	if f.call("policy set") == nil {
		t.Fatalf("an existing sandbox with no recorded digest kept its policy: %v", f.verbs())
	}
}

// The CLI counterpart of the SDK's stale ExpectedResourceVersion: the gateway
// answers a concurrent policy write with Conflict; Ensure retries once.
func TestCLIEnsure_StaleVersionRetriedOnce(t *testing.T) {
	conflict := errors.New("exit status 1: Conflict: persist policy revision failed due to concurrent modification (current resource_version: 18)")
	n := 0
	f := readyRunner(func() error {
		n++
		if n == 1 {
			return conflict
		}
		return nil
	})
	if err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:two")); err != nil {
		t.Fatalf("Ensure after one conflict: %v", err)
	}
	if n != 2 {
		t.Fatalf("policy set calls = %d, want 2", n)
	}

	n = 0
	f = readyRunner(func() error { n++; return conflict })
	err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:two"))
	if err == nil || !strings.Contains(err.Error(), "concurrent modification") || n != 2 {
		t.Fatalf("err = %v after %d calls, want the conflict after exactly 2", err, n)
	}

	n = 0
	f = readyRunner(func() error { n++; return errExit })
	if err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:two")); err == nil || n != 1 {
		t.Fatalf("err = %v after %d calls; a non-conflict error must not retry", err, n)
	}
}

func TestCLIExec_UploadsConfigAndRemovesIt(t *testing.T) {
	f := &fakeRunner{respond: func([]string) (string, error) { return `{"type":"result"}` + "\n", nil }}
	b := newTestCLI(f, t.TempDir())
	cfg := `{"mcpServers":{"jira":{"type":"http","url":"http://h:7390/mcp/jira","headers":{"Authorization":"Bearer SECRET-TOKEN"}}}}`
	args := []string{"claude", "-p", "--mcp-config", harnessconfig.MCPConfigPlaceholder}
	prompt := "say ok\nsecond line"
	rc, err := b.Exec(context.Background(), compiled("sha256:one"), cfg, prompt, nil, args)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	out, _ := io.ReadAll(rc)
	if err := rc.Close(); err != nil || string(out) != `{"type":"result"}`+"\n" {
		t.Fatalf("out = %q, close = %v", out, err)
	}
	want := append([]string{"openshell", "-g", "local", "sandbox", "exec", "--no-login-shell", "--no-tty",
		"aimux-work-readonly", "--", "sh", "-c", WorkWrapper, "sh"}, args...)
	if !slices.Equal(f.calls[0], want) {
		t.Fatalf("argv = %q\nwant   %q", f.calls[0], want)
	}
	for _, a := range f.calls[0] {
		if strings.Contains(a, "SECRET-TOKEN") {
			t.Fatal("the token is on the command line")
		}
		if strings.Contains(a, "say ok") {
			t.Fatal("the prompt is on the command line")
		}
	}
	if f.stdins[0] != cfg+"\n"+prompt {
		t.Fatalf("stdin = %q, want the config as line 1, then the prompt", f.stdins[0])
	}
}

// A large page goes through whole: stdin has no argument-size limit.
func TestCLIExec_LargePromptOnStdinUntruncated(t *testing.T) {
	f := &fakeRunner{respond: func([]string) (string, error) { return "", nil }}
	b := newTestCLI(f, t.TempDir())
	prompt := "Question about this page:\n" + strings.Repeat("page text ", 30<<10) // 300 KiB
	rc, err := b.Exec(context.Background(), compiled("d"), "{}", prompt, nil, []string{"claude", "-p", "--mcp-config", harnessconfig.MCPConfigPlaceholder})
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if f.stdins[0] != "{}\n"+prompt {
		t.Fatalf("stdin is %d bytes, want %d", len(f.stdins[0]), len("{}\n"+prompt))
	}
	for _, a := range f.calls[0] {
		if strings.Contains(a, "page text") {
			t.Fatal("page text on the command line")
		}
	}
}

func TestCLIExec_RefusesUnsafeInput(t *testing.T) {
	b := newTestCLI(&fakeRunner{respond: func([]string) (string, error) { return "", nil }}, t.TempDir())
	ph := []string{"claude", "--mcp-config", harnessconfig.MCPConfigPlaceholder}
	for name, tc := range map[string]struct {
		c    profile.Compiled
		cfg  string
		args []string
	}{
		"multi-line config": {compiled("d"), "{\n}", ph},
		"no placeholder":    {compiled("d"), "{}", []string{"claude", "-p"}},
		"flag-like sandbox": {profile.Compiled{Sandbox: "-g"}, "{}", ph},
		"empty args":        {compiled("d"), "{}", nil},
	} {
		if _, err := b.Exec(context.Background(), tc.c, tc.cfg, "q", nil, tc.args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCLIDownload_CatsTo0600File(t *testing.T) {
	f := &fakeRunner{respond: func([]string) (string, error) { return "line1\nline2\n", nil }}
	local := filepath.Join(t.TempDir(), "sub", "s.jsonl")
	if err := newTestCLI(f, t.TempDir()).Download(context.Background(), "aimux-x", "/sandbox/a.jsonl", local); err != nil {
		t.Fatalf("Download: %v", err)
	}
	want := []string{"openshell", "-g", "local", "sandbox", "exec", "--no-login-shell", "--no-tty", "aimux-x", "--", "cat", "--", "/sandbox/a.jsonl"}
	if !slices.Equal(f.calls[0], want) {
		t.Fatalf("argv = %q", f.calls[0])
	}
	st, err := os.Stat(local)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v %v, want 0600", st, err)
	}
	if b, _ := os.ReadFile(local); string(b) != "line1\nline2\n" { //nolint:gosec // G304: path is in the test temp dir
		t.Fatalf("content = %q", b)
	}

	f = &fakeRunner{respond: func([]string) (string, error) { return "partial", errExit }}
	failed := filepath.Join(t.TempDir(), "f.jsonl")
	if err := newTestCLI(f, t.TempDir()).Download(context.Background(), "aimux-x", "/sandbox/a.jsonl", failed); err == nil {
		t.Fatal("a failed cat succeeded")
	}
	if _, err := os.Stat(failed); err == nil {
		t.Fatal("a failed download left a file")
	}
	if err := newTestCLI(f, t.TempDir()).Download(context.Background(), "aimux-x", "relative.jsonl", failed); err == nil {
		t.Fatal("a relative remote path was accepted")
	}
}

// The .digest file records the policy digest: a re-pin (same manifest
// digest, new policy) updates the sandbox policy.
func TestCLIEnsure_KeyedOnPolicyDigest(t *testing.T) {
	dir := t.TempDir()
	f := readyRunner(func() error { return nil })
	b := newTestCLI(f, dir)
	c := compiled("sha256:manifest")
	c.PolicyDigest = "sha256:policy-1"
	if err := os.WriteFile(filepath.Join(dir, "aimux-work-readonly.digest"), []byte("sha256:policy-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if f.call("policy set") != nil {
		t.Fatalf("same policy digest changed the sandbox: %v", f.verbs())
	}
	c.PolicyDigest = "sha256:policy-2"
	if err := b.Ensure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if f.call("policy set") == nil {
		t.Fatalf("new policy digest with the same manifest digest kept the old policy: %v", f.verbs())
	}
	if d, _ := os.ReadFile(filepath.Join(dir, "aimux-work-readonly.digest")); string(d) != "sha256:policy-2" { //nolint:gosec // G304: path is in the test temp dir
		t.Fatalf("digest file = %q, want the policy digest", d)
	}
}

func TestRunTool_Argv(t *testing.T) {
	f := &fakeRunner{respond: func([]string) (string, error) { return `[{"number":1}]`, nil }}
	b := newTestCLI(f, t.TempDir()).(*cliBackend)
	argv := []string{"/usr/bin/gh", "issue", "list", "--repo", "cli/cli", "--json", "number,title,state,url"}
	out, stderr, exit, err := b.RunTool(context.Background(), "aimux-tools", argv)
	if err != nil || exit != 0 || string(out) != `[{"number":1}]` || len(stderr) != 0 {
		t.Fatalf("RunTool = %q %q %d %v", out, stderr, exit, err)
	}
	want := []string{"openshell", "-g", "local", "sandbox", "exec", "--no-login-shell", "--no-tty", "--timeout", "30",
		"--env", "GH_CONFIG_DIR=/tmp/gh", "--env", "XDG_CACHE_HOME=/tmp/cache",
		"aimux-tools", "--", "/usr/bin/gh", "issue", "list", "--repo", "cli/cli", "--json", "number,title,state,url"}
	if !slices.Equal(f.calls[0], want) {
		t.Fatalf("argv = %q\nwant   %q", f.calls[0], want)
	}
	if f.stdins[0] != "" {
		t.Fatalf("stdin = %q, want none", f.stdins[0])
	}

	// The exec timeout follows the caller's deadline, rounded up.
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	if _, _, _, err := b.RunTool(ctx, "aimux-tools", []string{"/usr/bin/gh", "--version"}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[1][8]; got != "5" {
		t.Fatalf("--timeout %s, want 5 from a 4.5 s deadline", got)
	}
}

func TestRunTool_ExitAndStderr(t *testing.T) {
	failing := func(err error) *fakeRunner {
		return &fakeRunner{respond: func(argv []string) (string, error) {
			if argv[3]+" "+argv[4] == "sandbox list" {
				return listHeader + "aimux-tools  2026-11-16 10:00:00  Ready\n", nil
			}
			return "partial out", err
		}}
	}
	argv := []string{"/usr/bin/gh", "repo", "view", "--", "cli/cli"}

	f := failing(&CommandError{Name: "openshell -g local sandbox", Err: errExit, Code: 1, Stderr: "gh: HTTP 403"})
	out, stderr, exit, err := newTestCLI(f, t.TempDir()).(*cliBackend).RunTool(context.Background(), "aimux-tools", argv)
	if err != nil || exit != 1 || string(stderr) != "gh: HTTP 403" || string(out) != "partial out" {
		t.Fatalf("exit 1: %q %q %d %v", out, stderr, exit, err)
	}

	// Not an exit status (the command never ran, or was killed): an error.
	for name, cerr := range map[string]error{
		"plain":  errors.New("openshell: not found"),
		"killed": &CommandError{Name: "openshell", Err: errors.New("signal: killed"), Code: -1},
	} {
		f = failing(cerr)
		if _, _, _, err := newTestCLI(f, t.TempDir()).(*cliBackend).RunTool(context.Background(), "aimux-tools", argv); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	f = failing(nil)
	b := newTestCLI(f, t.TempDir()).(*cliBackend)
	for name, tc := range map[string]struct {
		sandbox string
		argv    []string
	}{
		"bad sandbox": {"-rm", argv},
		"no argv":     {"aimux-tools", nil},
		"relative":    {"aimux-tools", []string{"gh", "--version"}},
	} {
		if _, _, _, err := b.RunTool(context.Background(), tc.sandbox, tc.argv); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused calls ran: %q", f.calls)
	}
}

// ExecRunner's Close reports a non-zero exit as a CommandError carrying the
// code and the stderr tail, which RunTool reads.
func TestExecRunner_CommandError(t *testing.T) {
	rc, err := ExecRunner()(context.Background(), nil, "sh", "-c", "echo out; echo err-line >&2; exit 7")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(rc)
	cerr := rc.Close()
	var ce *CommandError
	if !errors.As(cerr, &ce) || ce.Code != 7 || ce.Stderr != "err-line" || string(out) != "out\n" {
		t.Fatalf("close = %#v (%v), out %q", cerr, cerr, out)
	}
	if !strings.Contains(cerr.Error(), "exit status 7") || !strings.Contains(cerr.Error(), "err-line") {
		t.Fatalf("message = %q", cerr)
	}
}

func providerCalls(f *fakeRunner) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if len(c) > 5 && c[3] == "sandbox" && c[4] == "provider" {
			out = append(out, strings.Join(c[5:], " "))
		}
	}
	return out
}

// Providers attach only at create, so a changed provider list on a running
// sandbox is attached and detached; a sandbox with no recorded list keeps
// the one it was created with.
func TestCLIEnsure_SyncsProvidersOnChange(t *testing.T) {
	dir := t.TempDir()
	f := readyRunner(func() error { return nil })
	b := newTestCLI(f, dir)
	c := compiled("sha256:two")
	c.Providers = []string{"aimux-jira", "wingman-claude"}
	if err := os.WriteFile(filepath.Join(dir, "aimux-work-readonly.providers"), []byte("aimux-github\nwingman-claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	want := []string{"attach --wait aimux-work-readonly aimux-jira", "detach --wait aimux-work-readonly aimux-github"}
	if got := providerCalls(f); !slices.Equal(got, want) {
		t.Fatalf("provider calls = %q, want %q", got, want)
	}
	if p, _ := os.ReadFile(filepath.Join(dir, "aimux-work-readonly.providers")); string(p) != "aimux-jira\nwingman-claude\n" { //nolint:gosec // G304: test temp dir
		t.Fatalf("providers file = %q", p)
	}

	// No recorded list: nothing to diff against; the list is recorded.
	dir = t.TempDir()
	f = readyRunner(func() error { return nil })
	if err := newTestCLI(f, dir).Ensure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := providerCalls(f); len(got) != 0 {
		t.Fatalf("provider calls = %q with no recorded list", got)
	}
	if p, _ := os.ReadFile(filepath.Join(dir, "aimux-work-readonly.providers")); string(p) != "aimux-jira\nwingman-claude\n" { //nolint:gosec // G304: test temp dir
		t.Fatalf("providers file = %q", p)
	}

	// A failed attach leaves the digest unrecorded, so the next Ensure retries.
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aimux-work-readonly.providers"), []byte("wingman-claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f = &fakeRunner{respond: func(argv []string) (string, error) {
		switch argv[3] + " " + argv[4] {
		case "sandbox list":
			return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  Ready\n", nil
		case "sandbox provider":
			return "", errExit
		}
		return "", nil
	}}
	if err := newTestCLI(f, dir).Ensure(context.Background(), c); err == nil || !strings.Contains(err.Error(), "aimux-jira") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aimux-work-readonly.digest")); err == nil {
		t.Fatal("digest recorded after a failed attach")
	}
}

func TestCLIEnsure_CreateRecordsProviders(t *testing.T) {
	dir := t.TempDir()
	lists := 0
	f := &fakeRunner{respond: func(argv []string) (string, error) {
		if argv[3] == "sandbox" && argv[4] == "list" {
			lists++
			if lists == 1 {
				return listHeader, nil
			}
			return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  Ready\n", nil
		}
		return "", nil
	}}
	if err := newTestCLI(f, dir).Ensure(context.Background(), compiled("sha256:one")); err != nil {
		t.Fatal(err)
	}
	if p, _ := os.ReadFile(filepath.Join(dir, "aimux-work-readonly.providers")); string(p) != "wingman-claude\n" { //nolint:gosec // G304: test temp dir
		t.Fatalf("providers file = %q", p)
	}
}

// toolsRunner answers the runner sandbox's calls: provider list --names
// with have, sandbox list with aimux-tools Ready.
func toolsRunner(have string) *fakeRunner {
	return &fakeRunner{respond: func(argv []string) (string, error) {
		switch argv[3] + " " + argv[4] {
		case "provider list":
			return have, nil
		case "sandbox list":
			return listHeader + "aimux-tools  2026-11-16 10:00:00  Ready\n", nil
		case "sandbox exec":
			return "gh version 2.103.3 (2026-10-26)\n", nil
		}
		return "", nil
	}}
}

func TestToolSandbox_DropsSourceWithoutProvider(t *testing.T) {
	f := toolsRunner("wingman-claude\naimux-jira\n")
	var asked []string
	compile := func(have func(string) bool) (profile.Compiled, map[string]error, error) {
		asked = nil
		for _, p := range []string{"aimux-github", "aimux-jira"} {
			if have(p) {
				asked = append(asked, p)
			}
		}
		return profile.Compiled{Sandbox: profile.RunnerSandbox, Image: profile.RunnerImage, PolicyDigest: "sha256:r", PolicyYAML: []byte("version: 1\n"), Providers: []string{"aimux-jira"}},
			map[string]error{"github": errors.New("source github: provider aimux-github is not on the OpenShell gateway")}, nil
	}
	ts := NewToolSandbox(f.run, t.TempDir(), compile)
	ts.b.pollEvery = 0
	if err := ts.For("github").Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "aimux-github") {
		t.Fatalf("github Ensure = %v", err)
	}
	if err := ts.For("jira").Ensure(context.Background()); err != nil {
		t.Fatalf("jira Ensure = %v", err)
	}
	if strings.Join(asked, ",") != "aimux-jira" {
		t.Fatalf("have reported %v", asked)
	}
	want := []string{"openshell", "-g", "local", "provider", "list", "--names"}
	if got := f.call("provider list"); !slices.Equal(got, want) {
		t.Fatalf("provider list = %q", got)
	}
	out, _, exit, err := ts.For("jira").RunTool(context.Background(), []string{"/usr/bin/gh", "--version"})
	if err != nil || exit != 0 || !strings.HasPrefix(string(out), "gh version") {
		t.Fatalf("RunTool = %q %d %v", out, exit, err)
	}
	if got := f.call("sandbox exec"); got[len(got)-3] != "--" || got[len(got)-4] != profile.RunnerSandbox {
		t.Fatalf("exec = %q", got)
	}

	// A compile failure or an unreachable gateway fails every source.
	bad := NewToolSandbox(f.run, t.TempDir(), func(func(string) bool) (profile.Compiled, map[string]error, error) {
		return profile.Compiled{}, nil, errors.New("pack gh: broken")
	})
	if err := bad.For("jira").Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("compile error = %v", err)
	}
	down := NewToolSandbox((&fakeRunner{respond: func([]string) (string, error) { return "", errExit }}).run, t.TempDir(), compile)
	if err := down.For("jira").Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "list providers") {
		t.Fatalf("gateway down = %v", err)
	}
}

// RunnerTools compiles the runner from the snapshot it is bound to, with
// the gateway's providers, and creates aimux-tools from the runner image.
func TestRunnerTools_CompilesFromSnapshot(t *testing.T) {
	created := false
	f := &fakeRunner{respond: func(argv []string) (string, error) {
		switch argv[3] + " " + argv[4] {
		case "provider list":
			return "wingman-claude\naimux-github\n", nil
		case "sandbox create":
			created = true
		case "sandbox list":
			if !created {
				return listHeader, nil
			}
			return listHeader + "aimux-tools  2026-11-16 10:00:00  Ready\n", nil
		}
		return "", nil
	}}
	reg := toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"github": {Type: toolreg.TypeCLI, Pack: "gh"}}}
	snap := toolreg.NewSnapshot(reg, toolreg.PinTable{"github": {"gh_repo_view": {Hash: "h", Source: "pack:gh"}}}, nil)
	dir := t.TempDir()
	if err := RunnerTools(f.run, dir)(snap)("github").Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	create := f.call("sandbox create")
	if !slices.Contains(create, profile.RunnerImage) || !slices.Contains(create, "aimux-github") || !slices.Contains(create, profile.RunnerSandbox) {
		t.Fatalf("create = %q", create)
	}
	policy, err := os.ReadFile(filepath.Join(dir, "aimux-tools.policy.yaml")) //nolint:gosec // G304: test temp dir
	if err != nil || !strings.Contains(string(policy), "tools_github") {
		t.Fatalf("policy = %s (%v)", policy, err)
	}
}

// openshell exits 1 for a sandbox that is gone or not Ready, the same
// status as gh's own failures: after a failure RunTool checks the phase.
func TestRunTool_NotReady(t *testing.T) {
	argv := []string{"/usr/bin/gh", "repo", "view", "--", "cli/cli"}
	machine := func(list string, execErr error) *fakeRunner {
		return &fakeRunner{respond: func(a []string) (string, error) {
			if a[3]+" "+a[4] == "sandbox list" {
				return listHeader + list, nil
			}
			return "", execErr
		}}
	}
	exit1 := &CommandError{Name: "openshell", Err: errExit, Code: 1, Stderr: "sandbox not found"}
	for name, f := range map[string]*fakeRunner{
		"gone":         machine("", exit1),
		"stopped":      machine("aimux-tools  2026-11-16 10:00:00  Stopped\n", exit1),
		"not an exit":  machine("", errors.New("openshell: transport error")),
		"provisioning": machine("aimux-tools  2026-11-16 10:00:00  Provisioning\n", exit1),
	} {
		_, _, _, err := newTestCLI(f, t.TempDir()).(*cliBackend).RunTool(context.Background(), "aimux-tools", argv)
		if !errors.Is(err, mcpproxy.ErrRunnerNotReady) {
			t.Errorf("%s: err = %v, want ErrRunnerNotReady", name, err)
		}
	}
	// Ready: gh's exit 1 is the tool's answer.
	f := machine("aimux-tools  2026-11-16 10:00:00  Ready\n", exit1)
	if _, _, exit, err := newTestCLI(f, t.TempDir()).(*cliBackend).RunTool(context.Background(), "aimux-tools", argv); err != nil || exit != 1 {
		t.Fatalf("ready: exit %d err %v", exit, err)
	}
	// Success never checks the phase.
	f = machine("", nil)
	if _, _, _, err := newTestCLI(f, t.TempDir()).(*cliBackend).RunTool(context.Background(), "aimux-tools", argv); err != nil || f.call("sandbox list") != nil {
		t.Fatalf("success: %v, list %q", err, f.call("sandbox list"))
	}
}

// drainOut is a command's stdout as ExecRunner's Close treats it: read to
// the end, Close reports the exit; left unread, the command is killed and
// Close reports nothing (a kill is the caller's choice).
type drainOut struct {
	*strings.Reader
	err error
}

func (d drainOut) Close() error {
	if d.Len() > 0 {
		return nil
	}
	return d.err
}

// Output past maxToolOutput is drained, not killed, so the exit status is
// kept and a failing command is not reported as a success.
func TestRunTool_OverflowKeepsExit(t *testing.T) {
	big := strings.Repeat("a", maxToolOutput+100)
	for _, code := range []int{0, 3} {
		var cerr error
		if code != 0 {
			cerr = &CommandError{Name: "openshell", Err: errExit, Code: code}
		}
		run := func(_ context.Context, _ io.Reader, a ...string) (io.ReadCloser, error) {
			if a[3]+" "+a[4] == "sandbox list" {
				return io.NopCloser(strings.NewReader(listHeader + "aimux-tools  2026-11-16 10:00:00  Ready\n")), nil
			}
			return drainOut{Reader: strings.NewReader(big), err: cerr}, nil
		}
		b := NewCLIBackend(run, t.TempDir()).(*cliBackend)
		out, _, exit, err := b.RunTool(context.Background(), "aimux-tools", []string{"/usr/bin/gh", "--version"})
		if err != nil || exit != code || len(out) != maxToolOutput {
			t.Fatalf("code %d: exit %d err %v, %d bytes", code, exit, err, len(out))
		}
	}
}

// RunnerProbe is the read-only runner for status, diff and doctor: it
// never creates or updates aimux-tools, and a runner that is not Ready
// is down with a hint to approve or start serve.
func TestRunnerProbe_ReadOnly(t *testing.T) {
	ready := toolsRunner("aimux-github\n")
	reg := toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"github": {Type: toolreg.TypeCLI, Pack: "gh"}}}
	snap := toolreg.NewSnapshot(reg, nil, nil)
	tr := RunnerProbe(ready.run, t.TempDir())(snap)("github")
	if err := tr.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out, _, _, err := tr.RunTool(context.Background(), []string{"/usr/bin/gh", "--version"}); err != nil || !strings.HasPrefix(string(out), "gh version") {
		t.Fatalf("version: %q %v", out, err)
	}
	for _, v := range ready.verbs() {
		if v != "sandbox list" && v != "sandbox exec" {
			t.Errorf("probe ran %s", v)
		}
	}

	none := &fakeRunner{respond: func([]string) (string, error) { return listHeader, nil }}
	err := RunnerProbe(none.run, t.TempDir())(snap)("github").Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tool runner not started") || !strings.Contains(err.Error(), "aimux tools approve") || !strings.Contains(err.Error(), "aimux mcp proxy gateway install") {
		t.Fatalf("not started: %v", err)
	}
	if v := none.verbs(); !slices.Equal(v, []string{"sandbox list"}) {
		t.Fatalf("calls = %v", v)
	}
}

// A failed runner is replaced like any dead sandbox: deleted, then created.
func TestToolSandbox_FailedReplaced(t *testing.T) {
	lists := 0
	f := &fakeRunner{respond: func(a []string) (string, error) {
		switch a[3] + " " + a[4] {
		case "provider list":
			return "aimux-github\n", nil
		case "sandbox list":
			lists++
			phase := []string{"Failed", "Failed", "", "Ready"}[min(lists-1, 3)]
			if phase == "" {
				return listHeader, nil
			}
			return listHeader + "aimux-tools  2026-11-16 10:00:00  " + phase + "\n", nil
		}
		return "", nil
	}}
	ts := NewToolSandbox(f.run, t.TempDir(), func(func(string) bool) (profile.Compiled, map[string]error, error) {
		return profile.Compiled{Sandbox: profile.RunnerSandbox, Image: profile.RunnerImage, PolicyDigest: "d", PolicyYAML: []byte("version: 1\n")}, nil, nil
	})
	if err := ts.For("github").Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if f.call("sandbox delete") == nil || f.call("sandbox create") == nil {
		t.Fatalf("calls = %v, want delete then create", f.verbs())
	}
}

// Ensure never starts a stopped sandbox (it only creates a missing one or
// replaces a dead one), so a stopped runner says how to start it, without
// "setup".
func TestToolSandbox_StoppedSaysHow(t *testing.T) {
	for phase, want := range map[string]string{
		"Stopped": "openshell -g local sandbox start aimux-tools",
	} {
		f := &fakeRunner{respond: func(a []string) (string, error) {
			switch a[3] + " " + a[4] {
			case "provider list":
				return "aimux-github\n", nil
			case "sandbox list":
				return listHeader + "aimux-tools  2026-11-16 10:00:00  " + phase + "\n", nil
			}
			return "", nil
		}}
		ts := NewToolSandbox(f.run, t.TempDir(), func(func(string) bool) (profile.Compiled, map[string]error, error) {
			return profile.Compiled{Sandbox: profile.RunnerSandbox, Image: profile.RunnerImage, PolicyDigest: "d", PolicyYAML: []byte("version: 1\n")}, nil, nil
		})
		err := ts.For("github").Ensure(context.Background())
		var re *mcpproxy.RunnerError
		if !errors.As(err, &re) || !strings.Contains(re.Next, want) || strings.Contains(err.Error(), "aimux sandbox setup") {
			t.Errorf("%s: %v", phase, err)
		}
		for _, v := range f.verbs() {
			if v == "sandbox create" || v == "policy set" {
				t.Errorf("%s: ran %s", phase, v)
			}
		}
	}
	// The probe's "not started" carries the approve hint as its next step.
	none := &fakeRunner{respond: func([]string) (string, error) { return listHeader, nil }}
	err := RunnerProbe(none.run, t.TempDir())(toolreg.Snapshot{})("github").Ensure(context.Background())
	var re *mcpproxy.RunnerError
	if !errors.As(err, &re) || !strings.Contains(re.Next, "aimux tools approve") {
		t.Fatalf("probe: %v", err)
	}
}

// Through the probe, a listing that finds aimux-tools gone reports "not
// started" and creates nothing: its recovery Ensure only looks.
func TestRunnerProbe_ListingWhenGoneCreatesNothing(t *testing.T) {
	ready := true
	f := &fakeRunner{respond: func(a []string) (string, error) {
		switch a[3] + " " + a[4] {
		case "sandbox list":
			if ready {
				return listHeader + "aimux-tools  2026-11-16 10:00:00  Ready\n", nil
			}
			return listHeader, nil
		case "sandbox exec":
			if !ready {
				return "", &CommandError{Name: "openshell", Err: errExit, Code: 1, Stderr: "sandbox not found"}
			}
			return "gh version 2.103.3\n", nil
		}
		return "", nil
	}}
	pack, err := clipack.Builtin("gh")
	if err != nil {
		t.Fatal(err)
	}
	b := mcpproxy.NewCLIBackend("github", pack, RunnerProbe(f.run, t.TempDir())(toolreg.Snapshot{})("github"))
	if _, err := b.ListToolsRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready = false
	_, err = b.ListToolsRaw(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tool runner not started") || strings.Contains(err.Error(), "aimux sandbox setup") {
		t.Fatalf("err = %v", err)
	}
	for _, v := range f.verbs() {
		if v != "sandbox list" && v != "sandbox exec" {
			t.Errorf("probe ran %s", v)
		}
	}
}
