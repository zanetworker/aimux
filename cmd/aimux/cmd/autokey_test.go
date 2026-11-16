package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/decide"
)

var routerKey = decide.KeyConfig{Service: "aimux-openrouter", Env: "OPENROUTER_API_KEY", File: "~/.config/aimux/keys/openrouter"}

func fileInfoOf(t *testing.T, mode os.FileMode) os.FileInfo {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func probe(keychain bool, env map[string]string, fi os.FileInfo) keyProbe {
	return keyProbe{
		Exists:    func(s string) bool { return keychain && s == "aimux-openrouter" },
		LookupEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
		Stat: func(string) (os.FileInfo, error) {
			if fi == nil {
				return nil, os.ErrNotExist
			}
			return fi, nil
		},
	}
}

func TestAutoKeyLine_Sources(t *testing.T) {
	ok := fileInfoOf(t, 0o600)
	loose := fileInfoOf(t, 0o644)
	envSet := map[string]string{"OPENROUTER_API_KEY": "sk-or-SECRET"}
	cases := []struct {
		name     string
		p        keyProbe
		contains string
	}{
		{"keychain wins", probe(true, envSet, ok), "Auto mode: OpenRouter key found (Keychain aimux-openrouter)"},
		{"file before env", probe(false, envSet, ok), "Auto mode: OpenRouter key found (file ~/.config/aimux/keys/openrouter)"},
		{"env only is not enough on macOS", probe(false, envSet, nil), "Auto mode: OpenRouter key found in your shell environment (OPENROUTER_API_KEY), but the Wingman serve started by Chrome does not inherit it; store it in the Keychain (macOS) or the 0600 file instead"},
		{"file", probe(false, nil, ok), "Auto mode: OpenRouter key found (file ~/.config/aimux/keys/openrouter)"},
		{"empty env ignored", probe(false, map[string]string{"OPENROUTER_API_KEY": " "}, nil), "Auto mode: no OpenRouter key; Auto will always ask. Add one with: security add-generic-password -U -a \"$USER\" -s aimux-openrouter -w"},
		{"loose file mode refused", probe(false, nil, loose), "no OpenRouter key"},
	}
	for _, c := range cases {
		got := autoKeyLine(routerKey, c.p, "darwin")
		if !strings.Contains(got, c.contains) {
			t.Errorf("%s: got %q, want it to contain %q", c.name, got, c.contains)
		}
		if strings.Contains(got, "SECRET") {
			t.Errorf("%s: output holds the secret: %q", c.name, got)
		}
	}
	if got := autoKeyLine(routerKey, probe(false, nil, loose), "darwin"); !strings.Contains(got, "mode 644, want 600") {
		t.Errorf("loose file should say why it is refused: %q", got)
	}
	// on macOS an env-only key is never reported as working for Auto
	if got := autoKeyLine(routerKey, probe(false, envSet, nil), "darwin"); strings.Contains(got, "(environment)") || !strings.Contains(got, "security add-generic-password") {
		t.Errorf("macOS env-only line must not claim the key works and must say how to store it: %q", got)
	}
}

func TestAutoKeyLine_LinuxEnvCaveat(t *testing.T) {
	got := autoKeyLine(routerKey, probe(false, map[string]string{"OPENROUTER_API_KEY": "sk"}, nil), "linux")
	if !strings.Contains(got, "OpenRouter key found in your shell environment") || !strings.Contains(got, "started from the desktop") || !strings.Contains(got, "0600 file") {
		t.Errorf("linux env line = %q", got)
	}
}

func TestAutoKeyLine_LinuxHintAndNilProbe(t *testing.T) {
	got := autoKeyLine(routerKey, probe(false, nil, nil), "linux")
	if strings.Contains(got, "security add-generic-password") || !strings.Contains(got, "OPENROUTER_API_KEY") || !strings.Contains(got, "0600") {
		t.Errorf("linux hint = %q", got)
	}
	if strings.Index(got, "0600") > strings.Index(got, "OPENROUTER_API_KEY") {
		t.Errorf("linux hint should put the file before the env variable: %q", got)
	}
	// A zero probe (no seams) finds nothing and does not panic.
	if got := autoKeyLine(routerKey, keyProbe{}, "darwin"); !strings.Contains(got, "no OpenRouter key") {
		t.Errorf("zero probe = %q", got)
	}
	// No configured sources at all.
	if got := autoKeyLine(decide.KeyConfig{}, probe(true, nil, nil), "darwin"); !strings.Contains(got, "no OpenRouter key") {
		t.Errorf("no sources = %q", got)
	}
}

func TestBrowserSetup_PrintsAutoKeyLine(t *testing.T) {
	created := false
	f := &fakeRun{answer: setupScript("tok", &created)}
	env := testEnv(t, f)
	env.AutoKey = probe(true, nil, nil)
	var out bytes.Buffer
	if err := browserSetup(context.Background(), env, filepath.Join(t.TempDir(), "sandbox"), false, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if n := strings.Count(out.String(), "Auto mode:"); n != 1 || !strings.Contains(out.String(), "Auto mode: OpenRouter key found (Keychain aimux-openrouter)") {
		t.Fatalf("want exactly one found line:\n%s", out.String())
	}
}

func TestBrowserSetup_AutoKeyHonorsDecidersConfig(t *testing.T) {
	created := false
	f := &fakeRun{answer: setupScript("tok", &created)}
	env := testEnv(t, f)
	cfg := filepath.Join(env.Home, ".config", "aimux")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	yml := "mode-router:\n  key:\n    env: MY_KEY\n"
	if err := os.WriteFile(filepath.Join(cfg, "deciders.yaml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	env.AutoKey = probe(false, map[string]string{"MY_KEY": "x"}, nil)
	var out bytes.Buffer
	if err := browserSetup(context.Background(), env, filepath.Join(t.TempDir(), "sandbox"), false, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "MY_KEY") || !strings.Contains(out.String(), "shell environment") {
		t.Fatalf("custom key config ignored:\n%s", out.String())
	}
}

// autoKeyStatus is the structured result autoKeyLine prints: Found is true
// only for a source the Chrome-started serve can read (store item or 0600
// file), Source names what was seen, Line is autoKeyLine's text.
func TestAutoKeyStatus_Structured(t *testing.T) {
	ok, loose := fileInfoOf(t, 0o600), fileInfoOf(t, 0o644)
	envSet := map[string]string{"OPENROUTER_API_KEY": "sk"}
	cases := []struct {
		name   string
		p      keyProbe
		goos   string
		found  bool
		source string
	}{
		{"keychain", probe(true, nil, nil), "darwin", true, "keychain"},
		{"secret service", probe(true, nil, nil), "linux", true, "secret-service"},
		{"file", probe(false, nil, ok), "darwin", true, "file"},
		{"env only", probe(false, envSet, nil), "darwin", false, "env"},
		{"env only linux", probe(false, envSet, nil), "linux", false, "env"},
		{"loose file", probe(false, nil, loose), "darwin", false, "none"},
		{"nothing", probe(false, nil, nil), "darwin", false, "none"},
	}
	for _, c := range cases {
		got := autoKeyStatus(routerKey, c.p, c.goos)
		if got.Found != c.found || got.Source != c.source || got.Line != autoKeyLine(routerKey, c.p, c.goos) {
			t.Errorf("%s: %+v (want found=%v source=%s)", c.name, got, c.found, c.source)
		}
	}
}
