package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zanetworker/aimux/internal/decide"
	"github.com/zanetworker/aimux/internal/keyring"
)

// keyProbe checks whether a key source would answer, by existence only: it
// never reads a secret. A nil field finds nothing.
type keyProbe struct {
	Exists    func(service string) bool              // secret store item exists (no secret read)
	LookupEnv func(name string) (string, bool)       // process environment
	Stat      func(path string) (os.FileInfo, error) // key file; "~/" is the caller's to expand
}

func defaultKeyProbe(home string) keyProbe {
	return keyProbe{
		Exists:    keyring.Exists,
		LookupEnv: os.LookupEnv,
		Stat: func(p string) (os.FileInfo, error) {
			if strings.HasPrefix(p, "~/") {
				p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
			}
			return os.Stat(p)
		},
	}
}

// autoKeyLine says which configured key source would answer for Auto mode,
// in the order the key chain asks (secret store, file, environment), or how
// to add a key. The key itself is never read or printed. An environment
// variable is seen only by a serve started from the owner's shell, not by
// the one Wingman's relay starts from Chrome, so on macOS it never counts
// as found, and on Linux it comes with that caveat.
func autoKeyLine(kc decide.KeyConfig, p keyProbe, goos string) string {
	return autoKeyStatus(kc, p, goos).Line
}

// keyStatus is autoKeyLine's result in parts. Found is true only for a
// source the serve Chrome starts can read (a secret store item or a 0600
// file); Source is keychain, secret-service, file, env (seen in the shell
// only) or none.
type keyStatus struct {
	Found  bool
	Source string
	Line   string
}

func autoKeyStatus(kc decide.KeyConfig, p keyProbe, goos string) keyStatus {
	if kc.Service != "" && p.Exists != nil && p.Exists(kc.Service) {
		store, source := "Keychain", "keychain"
		if goos != "darwin" {
			store, source = "Secret Service", "secret-service"
		}
		return keyStatus{true, source, fmt.Sprintf("Auto mode: OpenRouter key found (%s %s)", store, kc.Service)}
	}
	var refused string
	if kc.File != "" && p.Stat != nil {
		if fi, err := p.Stat(kc.File); err == nil {
			if fi.Mode().IsRegular() && fi.Mode().Perm() == 0o600 {
				return keyStatus{true, "file", fmt.Sprintf("Auto mode: OpenRouter key found (file %s)", kc.File)}
			}
			refused = fmt.Sprintf(" The file %s is mode %o, want 600 (chmod 600 it).", kc.File, fi.Mode().Perm())
		}
	}
	file := kc.File
	if file == "" {
		file = "~/.config/aimux/keys/openrouter"
	}
	add := `security add-generic-password -U -a "$USER" -s aimux-openrouter -w`
	if goos != "darwin" {
		add = fmt.Sprintf("put the key in a file with mode 0600 at %s, or set OPENROUTER_API_KEY", file)
	}
	if kc.Env != "" && p.LookupEnv != nil {
		if v, ok := p.LookupEnv(kc.Env); ok && strings.TrimSpace(v) != "" {
			if goos == "darwin" {
				return keyStatus{false, "env", fmt.Sprintf("Auto mode: OpenRouter key found in your shell environment (%s), but the Wingman serve started by Chrome does not inherit it; store it in the Keychain (macOS) or the 0600 file instead: %s%s", kc.Env, add, refused)}
			}
			return keyStatus{false, "env", fmt.Sprintf("Auto mode: OpenRouter key found in your shell environment (%s). A Wingman serve started from the desktop (Chrome launched from a menu) may not inherit it; for that, store it in the Secret Service or the 0600 file %s instead.%s", kc.Env, file, refused)}
		}
	}
	return keyStatus{false, "none", "Auto mode: no OpenRouter key; Auto will always ask. Add one with: " + add + refused}
}
