// Package keyring finds the API keys aimux needs at call time, from the
// first of several sources that has one: the platform's secret store
// (macOS Keychain, or the Secret Service on Linux through secret-tool), an
// environment variable read inside the process, or a file only its owner
// can read. Keys never sit in aimux config or on a command line. Keys are
// never logged, and errors name the source and an exit status or file mode,
// never the key or a tool's output.
package keyring

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrNotFound means a source has no key (missing item, unset variable,
	// absent file or tool, or an empty value). The chain moves on.
	ErrNotFound = errors.New("no key")
	// ErrInsecure means a key file exists but is readable by others or not
	// owned by the user, so it is refused.
	ErrInsecure = errors.New("key file is not private (want mode 0600, owned by you)")
)

// readTimeout bounds one secret-store call (a locked store can prompt).
const readTimeout = 5 * time.Second

// Source is one place a key may be. Lookup returns the trimmed key, an
// error wrapping ErrNotFound when the source has none, or another error when
// the source could not be read; it honours ctx. String names the source for
// errors and never includes the key.
type Source interface {
	Lookup(ctx context.Context) (string, error)
	String() string
}

// Seams for tests: no test touches a real secret store, environment key or
// key file outside its temp dir.
var (
	run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, bin, args...).Output() // #nosec G204 -- fixed binaries, args built here
	}
	lookPath  = exec.LookPath
	getenv    = os.Getenv
	ownedByMe = ownedByCurrentUser
)

// Chain returns a key function that asks sources in order and returns the
// first key found. It caches nothing: every call reads the sources, so a
// replaced key is picked up; a caller that keeps the key in memory owns
// that cache and its invalidation (decide.OpenRouter drops a rejected key).
// When no source has a key, the
// error wraps ErrNotFound if every source simply had none, or is the first
// source's read failure otherwise, so callers can tell "no key" from "could
// not read the key". Errors never contain the key.
func Chain(sources ...Source) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var firstErr error
		for _, s := range sources {
			key, err := s.Lookup(ctx)
			if err == nil && key != "" {
				return key, nil
			}
			if err != nil && !errors.Is(err, ErrNotFound) && firstErr == nil {
				firstErr = fmt.Errorf("key source %s: %w", s, err)
			}
		}
		if firstErr != nil {
			return "", firstErr
		}
		return "", fmt.Errorf("no key in %s: %w", names(sources), ErrNotFound)
	}
}

func names(sources []Source) string {
	if len(sources) == 0 {
		return "any source (none configured)"
	}
	n := make([]string, len(sources))
	for i, s := range sources {
		n[i] = s.String()
	}
	return strings.Join(n, ", ")
}

// keychainSource reads a macOS Keychain generic password with
// `/usr/bin/security find-generic-password -s <service> -w`.
type keychainSource struct{ service string }

// exitKeychainNotFound is security's exit status for a missing item.
const exitKeychainNotFound = 44

func (s keychainSource) String() string { return "keychain " + s.service }

func (s keychainSource) Lookup(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	b, err := run(ctx, "/usr/bin/security", "find-generic-password", "-s", s.service, "-w")
	return toolResult(ctx, s, b, err, exitKeychainNotFound)
}

// secretServiceSource reads a Secret Service item (GNOME Keyring, KWallet)
// with `secret-tool lookup service <service>`; it has no key when
// secret-tool is not installed.
type secretServiceSource struct{ service string }

// exitSecretToolNotFound is secret-tool's exit status for a missing item.
const exitSecretToolNotFound = 1

func (s secretServiceSource) String() string { return "secret service " + s.service }

func (s secretServiceSource) Lookup(ctx context.Context) (string, error) {
	bin, err := lookPath("secret-tool")
	if err != nil {
		return "", fmt.Errorf("%s: secret-tool not installed: %w", s, ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	b, err := run(ctx, bin, "lookup", "service", s.service)
	return toolResult(ctx, s, b, err, exitSecretToolNotFound)
}

// toolResult turns a secret tool's output into a key or an error that keeps
// only the exit status (the tool's output could be the key).
func toolResult(ctx context.Context, s Source, out []byte, err error, notFoundStatus int) (string, error) {
	if err != nil {
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return "", fmt.Errorf("%s: %w", s, ctx.Err())
		case errors.As(err, &ee) && ee.ExitCode() == notFoundStatus:
			return "", fmt.Errorf("%s: %w", s, ErrNotFound)
		case errors.As(err, &ee):
			return "", fmt.Errorf("%s: exit status %d", s, ee.ExitCode())
		default:
			return "", fmt.Errorf("%s: could not run the tool", s)
		}
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("%s is empty: %w", s, ErrNotFound)
	}
	return key, nil
}

// envSource reads an environment variable of this process.
type envSource struct{ name string }

// Env is the key in environment variable name, read inside the process (it
// is never passed on a command line).
func Env(name string) Source { return envSource{name: name} }

func (s envSource) String() string { return "env " + s.name }

func (s envSource) Lookup(context.Context) (string, error) {
	key := strings.TrimSpace(getenv(s.name))
	if key == "" {
		return "", fmt.Errorf("%s is unset: %w", s, ErrNotFound)
	}
	return key, nil
}

// fileSource reads a key file.
type fileSource struct{ path string }

// File is the key in the file at path ("~/" expands to the home folder). A
// missing file has no key; a file whose mode is not exactly 0600 or that the
// user does not own is refused with ErrInsecure.
func File(path string) Source { return fileSource{path: expandHome(path)} }

// DefaultKeyFile is ~/.config/aimux/keys/<name>.
func DefaultKeyFile(name string) string {
	return expandHome(filepath.Join("~", ".config", "aimux", "keys", name))
}

func (s fileSource) String() string { return "file " + s.path }

func (s fileSource) Lookup(context.Context) (string, error) {
	f, err := os.Open(s.path) // #nosec G304 -- the owner's configured key file
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s: %w", s, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("%s: cannot open: %w", s, errors.Unwrap(err))
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat() // stat the opened file, not the path, so a swap cannot slip in
	if err != nil {
		return "", fmt.Errorf("%s: cannot stat", s)
	}
	if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() || !ownedByMe(fi) {
		return "", fmt.Errorf("%s (mode %o): %w", s, fi.Mode().Perm(), ErrInsecure)
	}
	b := make([]byte, 4096)
	n, err := f.Read(b)
	if err != nil && n == 0 {
		return "", fmt.Errorf("%s is empty: %w", s, ErrNotFound)
	}
	key := strings.TrimSpace(string(b[:n]))
	if key == "" {
		return "", fmt.Errorf("%s is empty: %w", s, ErrNotFound)
	}
	return key, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
