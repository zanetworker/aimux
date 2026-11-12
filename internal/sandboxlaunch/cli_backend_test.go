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

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/profile"
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
		Image: profile.SandboxImage, Provider: profile.SandboxProvider, PolicyYAML: []byte("version: 1\n")}
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

func TestCLIEnsure_ErrorPhaseFails(t *testing.T) {
	f := &fakeRunner{respond: func(argv []string) (string, error) {
		if argv[4] == "list" {
			return listHeader + "aimux-work-readonly  2026-11-11 10:00:00  Error\n", nil
		}
		return "", nil
	}}
	err := newTestCLI(f, t.TempDir()).Ensure(context.Background(), compiled("sha256:one"))
	if err == nil || !strings.Contains(err.Error(), "Error") {
		t.Fatalf("err = %v, want the Error phase named", err)
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
	rc, err := b.Exec(context.Background(), compiled("sha256:one"), cfg, prompt, args)
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
	rc, err := b.Exec(context.Background(), compiled("d"), "{}", prompt, []string{"claude", "-p", "--mcp-config", harnessconfig.MCPConfigPlaceholder})
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
		if _, err := b.Exec(context.Background(), tc.c, tc.cfg, "q", tc.args); err == nil {
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
