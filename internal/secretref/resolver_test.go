package secretref

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestResolver(t *testing.T, items map[string]string, env map[string]string) (*Resolver, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := NewResolver(Options{
		Keychain: func(_ context.Context, item string) (string, error) {
			v, ok := items[item]
			if !ok {
				return "", errors.New("item not found")
			}
			return v, nil
		},
		Getenv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Now:    clk.now,
	})
	return r, clk
}

func TestRef_ExactlyOneBackend(t *testing.T) {
	cases := map[string]Ref{
		"none": {},
		"two":  {Env: &EnvRef{Name: "A"}, File: &FileRef{Path: "/x"}},
	}
	for name, r := range cases {
		if r.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if err := (Ref{Env: &EnvRef{Name: "A"}}).Validate(); err != nil {
		t.Errorf("one backend: %v", err)
	}
}

func TestKeychain_FieldFromJSONItem(t *testing.T) {
	r, _ := newTestResolver(t, map[string]string{"aimux/JirAIa": `{"env":{"JIRA_TOKEN":"faketokenaaaa"}}`}, nil)
	v, err := r.Resolve(context.Background(), "JirAIa", Refs{Env: map[string]Ref{"JIRA_TOKEN": {Keychain: &KeychainRef{Item: "aimux/JirAIa", Field: "env.JIRA_TOKEN"}}}})
	if err != nil || v.Env["JIRA_TOKEN"] != "faketokenaaaa" {
		t.Fatalf("got %v, %v", v.Env, err)
	}
}

func TestKeychain_Plan4aHeaderItemReadable(t *testing.T) {
	item := `{"headers":{"X.Api":{"value":"Bearer fakeheaderbbbb","literal":"\"Bearer ${T}\""}}}`
	r, _ := newTestResolver(t, map[string]string{"aimux-mcp-proxy/pp": item}, nil)
	v, err := r.Resolve(context.Background(), "pp", Refs{Headers: map[string]Ref{"X.Api": {Keychain: &KeychainRef{Item: "aimux-mcp-proxy/pp", Field: "headers.X.Api.value"}}}})
	if err != nil || v.Headers["X.Api"] != "Bearer fakeheaderbbbb" {
		t.Fatalf("got %v, %v", v.Headers, err)
	}
}

func TestKeychain_WholeItemWhenNoField(t *testing.T) {
	r, _ := newTestResolver(t, map[string]string{"aimux-github": "plainfakevalue"}, nil)
	v, err := r.Resolve(context.Background(), "gh", Refs{Env: map[string]Ref{"GH_TOKEN": {Keychain: &KeychainRef{Item: "aimux-github"}}}})
	if err != nil || v.Env["GH_TOKEN"] != "plainfakevalue" {
		t.Fatalf("got %v, %v", v.Env, err)
	}
}

func writeFile(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFile_RejectsSymlink(t *testing.T) {
	target := writeFile(t, 0o600, "A=1\n")
	link := filepath.Join(t.TempDir(), "link.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r, _ := newTestResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {File: &FileRef{Path: link, Key: "A"}}}})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal, got %v", err)
	}
}

func TestFile_RejectsGroupReadable(t *testing.T) {
	p := writeFile(t, 0o640, "A=1\n")
	r, _ := newTestResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {File: &FileRef{Path: p, Key: "A"}}}})
	if err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("want a mode refusal, got %v", err)
	}
}

func TestFile_RejectsOtherOwner(t *testing.T) {
	p := writeFile(t, 0o600, "A=1\n")
	r := NewResolver(Options{Owner: func(fs.FileInfo) bool { return false }})
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {File: &FileRef{Path: p, Key: "A"}}}})
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("want an owner refusal, got %v", err)
	}
}

func TestFile_StrictDotenv(t *testing.T) {
	p := writeFile(t, 0o600, "# c\nA=\"quoted ${X}\"\nB='$(whoami)'\nC=plain value\n")
	r, _ := newTestResolver(t, nil, nil)
	v, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{
		"A": {File: &FileRef{Path: p, Key: "A"}}, "B": {File: &FileRef{Path: p, Key: "B"}}, "C": {File: &FileRef{Path: p, Key: "C"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Env["A"] != "quoted ${X}" || v.Env["B"] != "$(whoami)" || v.Env["C"] != "plain value" {
		t.Fatalf("not literal: %#v", v.Env)
	}
	bad := writeFile(t, 0o600, "export A=1\n")
	if _, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {File: &FileRef{Path: bad, Key: "A"}}}}); err == nil {
		t.Fatal("export lines must be refused")
	}
	if _, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"Z": {File: &FileRef{Path: p, Key: "Z"}}}}); err == nil {
		t.Fatal("a missing name must fail")
	}
}

func TestCmd_NoShell(t *testing.T) {
	r, _ := newTestResolver(t, nil, nil)
	v, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{"/bin/echo", "$HOME"}}}}})
	if err != nil || v.Env["A"] != "$HOME" {
		t.Fatalf("got %q, %v", v.Env["A"], err)
	}
}

func TestCmd_RelativeArgv0Refused(t *testing.T) {
	r, _ := newTestResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{"echo", "x"}}}}})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("want absolute-path refusal, got %v", err)
	}
}

func TestCmd_TimeoutKillsGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "grandchild.pid")
	script := writeScript(t, "#!/bin/sh\n(sleep 30; echo late) & echo $! > "+marker+"\nsleep 30\n")
	r, _ := newTestResolver(t, nil, nil)
	start := time.Now()
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}, Timeout: 300 * time.Millisecond}}}})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("resolve took %v", time.Since(start))
	}
	pid, _ := os.ReadFile(marker) //nolint:gosec // G304: test temp file
	time.Sleep(200 * time.Millisecond)
	if p := strings.TrimSpace(string(pid)); p != "" {
		if exec.Command("kill", "-0", p).Run() == nil { //nolint:gosec // test: probe the grandchild pid it wrote
			t.Fatalf("grandchild %s still running", p)
		}
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.sh")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	return p
}

func TestCmd_StderrNeverInError(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\necho sentinel-stderr-77 >&2\necho sentinel-stdout-77\nexit 1\n")
	r, _ := newTestResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}}}}})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "sentinel") || !strings.Contains(err.Error(), "exit status 1") || !strings.Contains(err.Error(), script) {
		t.Fatalf("error: %v", err)
	}
}

func TestCmd_OutputCap(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nhead -c 70000 /dev/zero | tr '\\0' a\n")
	r, _ := newTestResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}}}}})
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("want an output cap error, got %v", err)
	}
}

func TestCmd_TTLCacheAndFailureCache(t *testing.T) {
	count := filepath.Join(t.TempDir(), "n")
	script := writeScript(t, "#!/bin/sh\necho x >> "+count+"\necho value-"+"$(wc -l < "+count+" | tr -d ' ')\n")
	r, clk := newTestResolver(t, nil, nil)
	ref := Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}, TTL: time.Minute}}}}
	v1, _ := r.Resolve(context.Background(), "s", ref)
	v2, _ := r.Resolve(context.Background(), "s", ref)
	if v1.Env["A"] != "value-1" || v2.Env["A"] != "value-1" {
		t.Fatalf("ttl cache: %q %q", v1.Env["A"], v2.Env["A"])
	}
	clk.t = clk.t.Add(2 * time.Minute)
	if v3, _ := r.Resolve(context.Background(), "s", ref); v3.Env["A"] != "value-2" {
		t.Fatalf("after ttl: %q", v3.Env["A"])
	}
	noTTL := Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}}}}}
	if v4, _ := r.Resolve(context.Background(), "s", noTTL); v4.Env["A"] != "value-3" {
		t.Fatalf("no ttl resolves each time: %q", v4.Env["A"])
	}

	fails := filepath.Join(t.TempDir(), "f")
	bad := writeScript(t, "#!/bin/sh\necho x >> "+fails+"\nexit 3\n")
	badRef := Refs{Env: map[string]Ref{"B": {Cmd: &CmdRef{Argv: []string{bad}}}}}
	_, _ = r.Resolve(context.Background(), "s", badRef)
	_, _ = r.Resolve(context.Background(), "s", badRef)
	if b, _ := os.ReadFile(fails); strings.Count(string(b), "x") != 1 { //nolint:gosec // G304: test temp file
		t.Fatalf("a failure is cached for 30 s; ran %d times", strings.Count(string(b), "x"))
	}
	clk.t = clk.t.Add(31 * time.Second)
	_, _ = r.Resolve(context.Background(), "s", badRef)
	if b, _ := os.ReadFile(fails); strings.Count(string(b), "x") != 2 { //nolint:gosec // G304: test temp file
		t.Fatal("after 30 s the command runs again")
	}
}

func TestEnv_DaemonEnvOnly(t *testing.T) {
	r, _ := newTestResolver(t, nil, map[string]string{"HAVE": "env-value-1"})
	v, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Env: &EnvRef{Name: "HAVE"}}}})
	if err != nil || v.Env["A"] != "env-value-1" {
		t.Fatalf("got %v, %v", v.Env, err)
	}
	_, err = r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"B": {Env: &EnvRef{Name: "MISSING"}}}})
	if err == nil || !strings.Contains(err.Error(), "aimux's environment") {
		t.Fatalf("want an error naming aimux's environment, got %v", err)
	}
}

func TestResolver_ScrubberRegisteredBeforeReturn(t *testing.T) {
	r, _ := newTestResolver(t, map[string]string{"aimux/x": `{"env":{"T":"registeredfakecccc"}}`}, nil)
	if _, err := r.Resolve(context.Background(), "x", Refs{Env: map[string]Ref{"T": {Keychain: &KeychainRef{Item: "aimux/x", Field: "env.T"}}}}); err != nil {
		t.Fatal(err)
	}
	if got := r.Scrubber().Scrub("boom: registeredfakecccc here"); strings.Contains(got, "registeredfakecccc") {
		t.Fatalf("not scrubbed: %q", got)
	}
}

func TestScrub_EncodedForms(t *testing.T) {
	s := NewScrubber()
	s.Add("p@ss/w ord+1234")
	s.Add("Basic " + base64.StdEncoding.EncodeToString([]byte("alice:hunter2-secret")))
	s.Add("short7")
	for _, in := range []string{
		"raw p@ss/w ord+1234",
		"query p%40ss%2Fw+ord%2B1234",
		"path p@ss%2Fw%20ord+1234",
		`json "p@ss\/w ord+1234"`,
		"decoded alice:hunter2-secret",
		"pass hunter2-secret",
	} {
		if out := s.Scrub(in); strings.Contains(out, "1234") || strings.Contains(out, "hunter2") {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if out := s.Scrub("short7 and xshort7x"); out != "[redacted] and xshort7x" {
		t.Errorf("short value scrubbed only as a whole token: %q", out)
	}
}

func TestCheck_NeverHasValue(t *testing.T) {
	r, _ := newTestResolver(t, map[string]string{"aimux/x": `{"env":{"T":"checkfakedddd"}}`}, nil)
	res := r.Check(context.Background(), "x", Refs{Env: map[string]Ref{
		"T": {Keychain: &KeychainRef{Item: "aimux/x", Field: "env.T"}},
		"U": {Env: &EnvRef{Name: "NOPE"}},
	}})
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %+v", res)
	}
	for _, c := range res {
		if strings.Contains(c.Reason+c.Name+c.Backend, "checkfakedddd") {
			t.Fatalf("value in check result %+v", c)
		}
	}
	if !res[0].OK || res[1].OK {
		t.Fatalf("ok flags: %+v", res)
	}
}

func TestCmd_CallerCancelIsNotCached(t *testing.T) {
	r, _ := newTestResolver(t, nil, nil)
	ref := Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{"/bin/echo", "ok-value"}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = r.Resolve(ctx, "s", ref)
	if v, err := r.Resolve(context.Background(), "s", ref); err != nil || v.Env["A"] != "ok-value" {
		t.Fatalf("a cancelled call must not poison the cache: %v, %v", v.Env, err)
	}
}

func TestCmd_CachePerPolicy(t *testing.T) {
	count := filepath.Join(t.TempDir(), "n")
	script := writeScript(t, "#!/bin/sh\necho x >> "+count+"\necho v\n")
	r, clk := newTestResolver(t, nil, nil)
	long := Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}, TTL: time.Hour}}}}
	short := Refs{Env: map[string]Ref{"A": {Cmd: &CmdRef{Argv: []string{script}, TTL: time.Second}}}}
	_, _ = r.Resolve(context.Background(), "s", long)
	clk.t = clk.t.Add(2 * time.Second)
	_, _ = r.Resolve(context.Background(), "s", short)
	if b, _ := os.ReadFile(count); strings.Count(string(b), "x") != 2 { //nolint:gosec // test temp file
		t.Fatal("a 1 s ttl reference must not reuse an hour-ttl entry")
	}
}

func TestScrub_AdjacentShortTokens(t *testing.T) {
	s := NewScrubber()
	s.Add("abc12")
	if out := s.Scrub("abc12 abc12,abc12"); strings.Contains(out, "abc12") {
		t.Fatalf("adjacent tokens: %q", out)
	}
}

func TestScrub_BothJSONSpellings(t *testing.T) {
	s := NewScrubber()
	s.Add("a/b\"quoted-secret")
	for _, in := range []string{`"a/b\"quoted-secret"`, `"a\/b\"quoted-secret"`} {
		if out := s.Scrub(in); strings.Contains(out, "quoted-secret") {
			t.Errorf("%s -> %s", in, out)
		}
	}
}

func TestKeychain_ScopedReadErrorDoesNotFallBack(t *testing.T) {
	calls := 0
	r := NewResolver(Options{Keychain: func(context.Context, string) (string, error) { calls++; return "", errors.New("locked") }})
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {Keychain: &KeychainRef{Item: "aimux/s"}}}})
	if err == nil || calls != 1 {
		t.Fatalf("want one read and an error, got %d reads, %v", calls, err)
	}
}

func TestDefaultKeychain_FallsBackOnlyWhenAbsent(t *testing.T) {
	scoped, legacy := 0, 0
	look := keychainLookup(
		func(context.Context, string) (string, error) { scoped++; return "", errors.New("locked") },
		func(context.Context, string) (string, error) { legacy++; return "other", nil })
	if _, err := look(context.Background(), "aimux/s"); err == nil || legacy != 0 {
		t.Fatalf("a locked scoped item must not fall back: err=%v legacy=%d", err, legacy)
	}
	look = keychainLookup(
		func(context.Context, string) (string, error) { return "", keyring.ErrNotFound },
		func(context.Context, string) (string, error) { return "legacyvalue", nil })
	if v, err := look(context.Background(), "aimux-github"); err != nil || v != "legacyvalue" {
		t.Fatalf("absent scoped item falls back: %q %v", v, err)
	}
}

func TestFile_NamedPipeDoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	r, _ := newTestResolver(t, nil, nil)
	done := make(chan error, 1)
	go func() {
		_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {File: &FileRef{Path: p}}}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("want a regular-file refusal, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resolving a named pipe blocked")
	}
}
