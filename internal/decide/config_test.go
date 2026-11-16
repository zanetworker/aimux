package decide

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/llm"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deciders.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testBuiltins stands in for a use case's built-in deciders.
func testBuiltins() map[string]DeciderConfig {
	return map[string]DeciderConfig{
		"picker": {
			Backends:  []BackendConfig{{Kind: "openrouter", Model: "some/flash"}},
			Threshold: 0.25,
			Timeout:   2 * time.Second,
			Key:       KeyConfig{Service: "svc", Env: "PICKER_KEY", File: "~/.config/x/key"},
		},
	}
}

func TestConfig_BuiltinsWithoutFile(t *testing.T) {
	bi := testBuiltins()
	cfgs, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"), bi)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cfgs["picker"]
	if !ok || len(cfgs) != 1 {
		t.Fatalf("want only the passed built-in, got %v", cfgs)
	}
	if b := c.Backends[0]; b.Kind != "openrouter" || b.Model != "some/flash" || !b.zdr() || c.Threshold != 0.25 || c.Timeout != 2*time.Second || c.Key.Service != "svc" {
		t.Fatalf("built-in %+v", c)
	}
	none, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"), nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("no built-ins and no file: want none, got %v, %v", none, err)
	}
}

func TestConfig_DoesNotMutateBuiltins(t *testing.T) {
	bi := testBuiltins()
	if _, err := LoadConfig(writeConfig(t, "picker:\n  threshold: 0.4\n  backends:\n    - kind: chat\n      model: haiku\n"), bi); err != nil {
		t.Fatal(err)
	}
	if bi["picker"].Threshold != 0.25 || bi["picker"].Backends[0].Kind != "openrouter" {
		t.Fatalf("LoadConfig changed the caller's built-ins: %+v", bi["picker"])
	}
}

// decide is the reusable core: no use case's decider names belong in it.
func TestNoUseCaseNamesInDecide(t *testing.T) {
	banned := []string{"mode" + "-router", "auto" + "route"}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v %v", files, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f) // #nosec G304 -- this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range banned {
			if strings.Contains(string(data), b) {
				t.Errorf("%s names %q; use-case defaults belong to the use case package", f, b)
			}
		}
	}
}

func TestConfig_FileOverrides(t *testing.T) {
	path := writeConfig(t, `
picker:
  threshold: 0.3
  backends:
    - kind: openrouter
      model: cloudflare/clef-flash
    - kind: chat
      model: haiku
      threshold: 0.7
model-router:
  backends:
    - kind: openrouter
      model: some/model
  threshold: 0.5
  timeout: 1500ms
  key:
    env: OTHER_KEY
`)
	cfgs, err := LoadConfig(path, testBuiltins())
	if err != nil {
		t.Fatal(err)
	}
	mr := cfgs["picker"]
	if mr.Threshold != 0.3 || mr.Timeout != 2*time.Second || mr.Key.Service != "svc" {
		t.Fatalf("picker must keep defaults it does not override: %+v", mr)
	}
	if len(mr.Backends) != 2 || mr.Backends[1].Kind != "chat" || mr.Backends[1].Threshold == nil || *mr.Backends[1].Threshold != 0.7 {
		t.Fatalf("backends %+v", mr.Backends)
	}
	m := cfgs["model-router"]
	if m.Backends[0].Model != "some/model" || m.Timeout != 1500*time.Millisecond || !m.Backends[0].zdr() || m.Key.Env != "OTHER_KEY" {
		t.Fatalf("model-router %+v (zdr defaults on)", m)
	}
}

func TestConfig_UnknownFieldsRejected(t *testing.T) {
	for _, tc := range []struct{ body, key string }{
		{"picker:\n  treshold: 0.5\n", "treshold"},
		{"picker:\n  backends:\n    - kind: chat\n      mdl: haiku\n", "mdl"},
		{"model-router:\n  key:\n    servcie: x\n", "servcie"},
	} {
		path := writeConfig(t, tc.body)
		_, err := LoadConfig(path, testBuiltins())
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%q: want an error naming the file and %q, got %v", tc.body, tc.key, err)
		}
	}
}

func TestConfig_Malformed(t *testing.T) {
	path := writeConfig(t, "picker: [oops")
	if _, err := LoadConfig(path, testBuiltins()); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}

func TestConfig_NewDeciderNeedsThreshold(t *testing.T) {
	path := writeConfig(t, "model-router:\n  backends:\n    - kind: openrouter\n      model: m\n  timeout: 1s\n")
	_, err := LoadConfig(path, testBuiltins())
	if err == nil || !strings.Contains(err.Error(), "threshold") || !strings.Contains(err.Error(), "model-router") || !strings.Contains(err.Error(), path) {
		t.Fatalf("a new decider without a threshold would always decide; want an error, got %v", err)
	}
	cfg := DeciderConfig{Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Timeout: time.Second}
	if _, err := Build("model-router", cfg, Deps{Key: staticKey("k")}); err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("Build: want a threshold error, got %v", err)
	}
}

func TestBuild(t *testing.T) {
	cfgs, _ := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"), testBuiltins())
	l := &Log{Path: filepath.Join(t.TempDir(), "d.jsonl")}
	deps := Deps{Key: staticKey("k"), LLM: &llm.Client{}, Log: l}

	d, err := Build("picker", cfgs["picker"], deps)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "picker" || d.Threshold != 0.25 || d.Timeout != 2*time.Second || d.Log != l || len(d.Backends) != 1 {
		t.Fatalf("decider %+v", d)
	}
	or, ok := d.Backends[0].(*OpenRouter)
	if !ok || or.Model != "some/flash" || !or.ZDR {
		t.Fatalf("backend %+v", d.Backends[0])
	}

	base := BackendConfig{Kind: "openrouter", Model: "m"}
	bad := 1.5
	for name, cfg := range map[string]DeciderConfig{
		"no backends":       {Threshold: 0.5, Timeout: time.Second},
		"unknown kind":      {Backends: []BackendConfig{{Kind: "nope", Model: "m"}}, Threshold: 0.5, Timeout: time.Second},
		"no model":          {Backends: []BackendConfig{{Kind: "openrouter"}}, Threshold: 0.5, Timeout: time.Second},
		"threshold":         {Backends: []BackendConfig{base}, Threshold: 1.5, Timeout: time.Second},
		"backend threshold": {Backends: []BackendConfig{{Kind: "openrouter", Model: "m", Threshold: &bad}}, Threshold: 0.5, Timeout: time.Second},
		"timeout":           {Backends: []BackendConfig{base}, Threshold: 0.5},
	} {
		if _, err := Build("x", cfg, deps); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	chatOnly := DeciderConfig{Backends: []BackendConfig{{Kind: "chat", Model: "haiku"}}, Threshold: 0.5, Timeout: time.Second}
	if _, err := Build("x", chatOnly, Deps{}); err == nil {
		t.Error("chat without an llm client: want an error")
	}
	noKey := DeciderConfig{Backends: []BackendConfig{base}, Threshold: 0.5, Timeout: time.Second}
	if _, err := Build("x", noKey, Deps{}); err == nil {
		t.Error("openrouter with no key config and no deps.Key: want an error")
	}
}

func TestBuild_KeyFromConfig(t *testing.T) {
	cfg := DeciderConfig{Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.5, Timeout: time.Second,
		Key: KeyConfig{File: filepath.Join(t.TempDir(), "absent")}}
	d, err := Build("x", cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	or := d.Backends[0].(*OpenRouter)
	if _, err := or.Key(context.Background()); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("key chain should come from the config's sources: %v", err)
	}
}

// A Haiku chat fallback judged on its own 0.7 scale under a Clef 0.25 decider.
func TestBuild_HaikuFallbackThreshold(t *testing.T) {
	cfgs, err := LoadConfig(writeConfig(t, `
picker:
  backends:
    - kind: openrouter
      model: cloudflare/clef-flash
    - kind: chat
      model: haiku
      threshold: 0.7
`), testBuiltins())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLLM{reply: llm.Reply{Text: `{"choice":"web","confidence":0.5}`}}
	noKey := func(context.Context) (string, error) { return "", fmt.Errorf("x: %w", keyring.ErrNotFound) }
	d, err := Build("picker", cfgs["picker"], Deps{Key: noKey, LLM: &llm.Client{Primary: fake}, Usage: func(cost.Usage) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if a.Backend != "chat" || a.Choice != "web" || a.Threshold != 0.7 || a.Decided || a.Undecided != UndecidedLowConfidence {
		t.Fatalf("answer %+v", a)
	}
	fake.reply.Text = `{"choice":"web","confidence":0.75}`
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); !a.Decided {
		t.Fatalf("answer %+v", a)
	}
}

func TestConfig_ZeroThresholdRejectedForBuiltins(t *testing.T) {
	for _, body := range []string{"picker:\n  threshold: 0\n", "picker:\n  threshold: -0.1\n"} {
		path := writeConfig(t, body)
		_, err := LoadConfig(path, testBuiltins())
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "picker") {
			t.Errorf("%q: want an error naming the file and decider, got %v", body, err)
		}
	}
	cfg := testBuiltins()["picker"]
	cfg.Threshold = 0
	if _, err := Build("picker", cfg, Deps{Key: staticKey("k")}); err == nil || !strings.Contains(err.Error(), "picker") {
		t.Fatalf("Build must reject threshold 0 for a built-in too, got %v", err)
	}
}

// The file comes before the environment: a serve Chrome starts sees no
// shell exports, so the env variable is the last resort.
func TestBuild_KeyFileBeforeEnv(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICKER_TEST_KEY", "from-env")
	cfg := DeciderConfig{Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.5, Timeout: time.Second,
		Key: KeyConfig{Env: "PICKER_TEST_KEY", File: file}}
	d, err := Build("x", cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if k, err := d.Backends[0].(*OpenRouter).Key(context.Background()); err != nil || k != "from-file" {
		t.Fatalf("want the file's key first, got %q, %v", k, err)
	}
}
