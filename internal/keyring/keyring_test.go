package keyring

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSecret looks like a key; tests assert it never appears in errors.
const fakeSecret = "sk-or-FAKE-0123456789" // #nosec G101 -- fake value for the leak checks

// exitErr is a real *exec.ExitError with the given status.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run() // #nosec G204 -- test helper, numeric arg
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("could not make exit status %d: %v", code, err)
	}
	return err
}

type runCall struct {
	bin  string
	args []string
}

// fakeRun replaces the exec seam; fn decides the output.
func fakeRun(t *testing.T, fn func(ctx context.Context) ([]byte, error)) *[]runCall {
	t.Helper()
	var calls []runCall
	old := run
	run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		calls = append(calls, runCall{bin, args})
		return fn(ctx)
	}
	t.Cleanup(func() { run = old })
	return &calls
}

func out(s string, err error) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return []byte(s), err }
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), fakeSecret) {
		t.Fatalf("error leaks the secret: %v", err)
	}
}

func TestKeychainSource(t *testing.T) {
	calls := fakeRun(t, out(fakeSecret+"\n", nil))
	key, err := keychainSource{service: "aimux-openrouter"}.Lookup(context.Background())
	if err != nil || key != fakeSecret {
		t.Fatalf("got %q, %v", key, err)
	}
	c := (*calls)[0]
	if c.bin != "/usr/bin/security" || strings.Join(c.args, " ") != "find-generic-password -s aimux-openrouter -w" {
		t.Fatalf("call %+v", c)
	}
}

func TestKeychainSource_Errors(t *testing.T) {
	fakeRun(t, out("", exitErr(t, 44)))
	if _, err := (keychainSource{service: "svc"}).Lookup(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exit 44: want ErrNotFound, got %v", err)
	}
	fakeRun(t, out(fakeSecret, exitErr(t, 36))) // locked keychain: output must not leak
	_, err := keychainSource{service: "svc"}.Lookup(context.Background())
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("exit 36: want a non-not-found error, got %v", err)
	}
	noSecret(t, err)
	if !strings.Contains(err.Error(), "svc") || !strings.Contains(err.Error(), "36") {
		t.Fatalf("error should name the service and status: %v", err)
	}
	fakeRun(t, out("  \n", nil))
	if _, err := (keychainSource{service: "svc"}).Lookup(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty: want ErrNotFound, got %v", err)
	}
}

func TestKeychainSource_HonoursContext(t *testing.T) {
	fakeRun(t, func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := keychainSource{service: "svc"}.Lookup(ctx)
	if time.Since(start) > time.Second || err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("took %v, err %v", time.Since(start), err)
	}
}

func TestSecretServiceSource(t *testing.T) {
	oldLook := lookPath
	t.Cleanup(func() { lookPath = oldLook })

	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	calls := fakeRun(t, out(fakeSecret, nil))
	if _, err := (secretServiceSource{service: "svc"}).Lookup(context.Background()); !errors.Is(err, ErrNotFound) || len(*calls) != 0 {
		t.Fatalf("no secret-tool: want ErrNotFound and no exec, got %v, %d calls", err, len(*calls))
	}

	lookPath = func(string) (string, error) { return "/usr/bin/secret-tool", nil }
	calls = fakeRun(t, out(fakeSecret+"\n", nil))
	key, err := secretServiceSource{service: "aimux-openrouter"}.Lookup(context.Background())
	if err != nil || key != fakeSecret {
		t.Fatalf("got %q, %v", key, err)
	}
	if c := (*calls)[0]; c.bin != "/usr/bin/secret-tool" || strings.Join(c.args, " ") != "lookup service aimux-openrouter" {
		t.Fatalf("call %+v", c)
	}

	fakeRun(t, out("", exitErr(t, 1)))
	if _, err := (secretServiceSource{service: "svc"}).Lookup(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exit 1: want ErrNotFound, got %v", err)
	}
	fakeRun(t, out(fakeSecret, exitErr(t, 2)))
	_, err = secretServiceSource{service: "svc"}.Lookup(context.Background())
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("exit 2: want a non-not-found error, got %v", err)
	}
	noSecret(t, err)
}

func TestEnv(t *testing.T) {
	old := getenv
	t.Cleanup(func() { getenv = old })
	getenv = func(name string) string {
		if name == "AIMUX_TEST_KEY" {
			return " " + fakeSecret + "\n"
		}
		return ""
	}
	if key, err := Env("AIMUX_TEST_KEY").Lookup(context.Background()); err != nil || key != fakeSecret {
		t.Fatalf("got %q, %v", key, err)
	}
	if _, err := Env("AIMUX_UNSET").Lookup(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unset: want ErrNotFound, got %v", err)
	}
}

func writeKey(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys", "openrouter")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fakeSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFile(t *testing.T) {
	path := writeKey(t, 0o600)
	if key, err := File(path).Lookup(context.Background()); err != nil || key != fakeSecret {
		t.Fatalf("got %q, %v", key, err)
	}
	if _, err := File(filepath.Join(t.TempDir(), "absent")).Lookup(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: want ErrNotFound, got %v", err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o400, 0o700} {
		_, err := File(writeKey(t, mode)).Lookup(context.Background())
		if !errors.Is(err, ErrInsecure) || errors.Is(err, ErrNotFound) {
			t.Fatalf("mode %o: want ErrInsecure, got %v", mode, err)
		}
		noSecret(t, err)
	}
}

func TestFile_RefusesOtherOwner(t *testing.T) {
	old := ownedByMe
	t.Cleanup(func() { ownedByMe = old })
	ownedByMe = func(os.FileInfo) bool { return false }
	if _, err := File(writeKey(t, 0o600)).Lookup(context.Background()); !errors.Is(err, ErrInsecure) {
		t.Fatalf("want ErrInsecure, got %v", err)
	}
}

func TestFile_ExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	if got := expandHome("~/.config/aimux/keys/x"); got != filepath.Join(home, ".config", "aimux", "keys", "x") {
		t.Fatalf("got %q", got)
	}
	if got := DefaultKeyFile("openrouter"); got != filepath.Join(home, ".config", "aimux", "keys", "openrouter") {
		t.Fatalf("default %q", got)
	}
}

// stub is a Source with a fixed result that counts lookups.
type stub struct {
	key   string
	err   error
	calls int
}

func (s *stub) Lookup(context.Context) (string, error) { s.calls++; return s.key, s.err }
func (s *stub) String() string                         { return "stub" }

func TestChain_FirstFound(t *testing.T) {
	a := &stub{err: ErrNotFound}
	b := &stub{key: "from-b"}
	c := &stub{key: "from-c"}
	key, err := Chain(a, b, c)(context.Background())
	if err != nil || key != "from-b" || c.calls != 0 {
		t.Fatalf("got %q, %v, c called %d", key, err, c.calls)
	}
}

func TestChain_SkipsErrorsButReportsThem(t *testing.T) {
	broken := &stub{err: errors.New("keychain locked")}
	found := &stub{key: "k"}
	if key, err := Chain(broken, found)(context.Background()); err != nil || key != "k" {
		t.Fatalf("a later source should still answer: %q, %v", key, err)
	}
	_, err := Chain(&stub{err: ErrNotFound}, &stub{err: errors.New("keychain locked")})(context.Background())
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a failing source must not look like not-found: %v", err)
	}
	if _, err := Chain(&stub{err: ErrNotFound}, &stub{key: ""})(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("all missing: want ErrNotFound, got %v", err)
	}
	if _, err := Chain()(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no sources: want ErrNotFound, got %v", err)
	}
}

// Chain keeps no cache: each call reads the sources, so a key replaced in
// its source is returned next time (the caller owns any cache).
func TestChain_ReadsSourcesEveryCall(t *testing.T) {
	s := &stub{key: "k1"}
	get := Chain(s)
	if k, err := get(context.Background()); err != nil || k != "k1" {
		t.Fatal(k, err)
	}
	s.key = "k2"
	if k, err := get(context.Background()); err != nil || k != "k2" {
		t.Fatalf("after the source changed: %q, %v; want k2", k, err)
	}
	if s.calls != 2 {
		t.Fatalf("looked up %d times, want 2", s.calls)
	}
}
