package decide

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/llm"
)

// BackendConfig is one backend in a decider's chain: Kind "openrouter"
// (Decisions API) or "chat" (internal/llm), its model, for openrouter
// whether to require zero-data-retention providers (unset means true), and
// an optional Threshold for this backend's answers (unset means the
// decider's), since models report confidence on different scales.
type BackendConfig struct {
	Kind      string   `yaml:"kind"`
	Model     string   `yaml:"model"`
	ZDR       *bool    `yaml:"zdr,omitempty"`
	Threshold *float64 `yaml:"threshold,omitempty"`
}

func (b BackendConfig) zdr() bool { return b.ZDR == nil || *b.ZDR }

// KeyConfig says where the API key may be, tried in this order: the
// platform secret store item Service (macOS Keychain, Linux Secret
// Service), the 0600 File, then the environment variable Env (last, since a
// process started outside the owner's shell, such as one Chrome launches,
// does not see shell exports). Empty fields are skipped.
type KeyConfig struct {
	Service string `yaml:"service"`
	Env     string `yaml:"env"`
	File    string `yaml:"file"`
}

// DeciderConfig is one named decider in deciders.yaml: its backends, tried
// in order, the confidence threshold and total timeout, and where its API
// key is.
type DeciderConfig struct {
	Backends  []BackendConfig `yaml:"backends"`
	Threshold float64         `yaml:"threshold"`
	Timeout   time.Duration   `yaml:"timeout"`
	Key       KeyConfig       `yaml:"key"`
}

// Deps is what Build wires in: Key overrides the config's key sources when
// set (tests, or a caller with its own source), LLM is the model client for
// chat backends, Log the decision log, and Usage the usage recorder (nil
// means the Decider's default).
type Deps struct {
	Key   func(ctx context.Context) (string, error)
	LLM   *llm.Client
	Log   *Log
	Usage func(cost.Usage) error
}

// DefaultConfigPath is ~/.config/aimux/deciders.yaml ("" when there is no home).
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "aimux", "deciders.yaml")
}

// LoadConfig reads the named deciders from path, a YAML map of name to
// DeciderConfig, on top of builtins: the deciders a use case defines in
// code (internal/decide defines none). Unknown keys are errors (a typo must
// not silently keep a default). Every built-in is in the result; a file
// entry for a built-in overrides only the fields it sets (a list such as
// backends is replaced whole). Every decider the file names must end with a
// threshold above 0. A missing file gives the built-ins alone; an
// unreadable or malformed one is an error naming path and the decider.
// builtins itself is never changed.
func LoadConfig(path string, builtins map[string]DeciderConfig) (map[string]DeciderConfig, error) {
	cfgs := make(map[string]DeciderConfig, len(builtins))
	for name, c := range builtins {
		c.Backends = append([]BackendConfig(nil), c.Backends...)
		cfgs[name] = c
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the owner's config file
	if errors.Is(err, os.ErrNotExist) {
		return cfgs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read deciders config %s: %w", path, err)
	}
	var nodes map[string]yaml.Node
	if err := yaml.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("parse deciders config %s: %w", path, err)
	}
	for name, node := range nodes {
		if err := strictCheck(&node); err != nil {
			return nil, fmt.Errorf("parse deciders config %s, decider %q: %w", path, name, err)
		}
	}
	for name, node := range nodes {
		c := cfgs[name] // the built-in, or zero for a new name; the file's fields go on top
		if err := node.Decode(&c); err != nil {
			return nil, fmt.Errorf("parse deciders config %s, decider %q: %w", path, name, err)
		}
		if c.Threshold <= 0 {
			return nil, fmt.Errorf("deciders config %s, decider %q: set a threshold (0 < threshold <= 1); zero or less would decide every answer", path, name)
		}
		cfgs[name] = c
	}
	return cfgs, nil
}

// strictCheck decodes one decider's node with unknown fields rejected
// (yaml.Node.Decode cannot, so the node goes through a strict Decoder).
func strictCheck(node *yaml.Node) error {
	raw, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var c DeciderConfig
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Build makes the Decider name from cfg. It errors on no backends, an
// unknown kind, a missing model, a threshold not in (0, 1] (zero would
// decide everything), a
// timeout that is not positive, or a backend whose dependency is missing:
// a key source for openrouter (deps.Key, else cfg.Key), deps.LLM for chat.
func Build(name string, cfg DeciderConfig, deps Deps) (*Decider, error) {
	if cfg.Threshold <= 0 {
		return nil, fmt.Errorf("decider %q: set a threshold (0 < threshold <= 1); zero would decide every answer", name)
	}
	if cfg.Threshold < 0 || cfg.Threshold > 1 {
		return nil, fmt.Errorf("decider %q: threshold %v is outside 0..1", name, cfg.Threshold)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("decider %q: timeout must be positive", name)
	}
	if len(cfg.Backends) == 0 {
		return nil, fmt.Errorf("decider %q: no backends", name)
	}
	key := deps.Key
	if key == nil {
		key = keyChain(cfg.Key)
	}
	d := &Decider{Name: name, Threshold: cfg.Threshold, Timeout: cfg.Timeout, Log: deps.Log, Usage: deps.Usage}
	for _, bc := range cfg.Backends {
		b, err := buildBackend(bc, key, deps.LLM)
		if err != nil {
			return nil, fmt.Errorf("decider %q: %w", name, err)
		}
		d.Backends = append(d.Backends, b)
	}
	return d, nil
}

// keyChain is the key function for kc's sources, nil when it names none.
func keyChain(kc KeyConfig) func(ctx context.Context) (string, error) {
	var sources []keyring.Source
	if kc.Service != "" {
		sources = append(sources, keyring.System(kc.Service))
	}
	if kc.File != "" {
		sources = append(sources, keyring.File(kc.File))
	}
	if kc.Env != "" {
		sources = append(sources, keyring.Env(kc.Env))
	}
	if len(sources) == 0 {
		return nil
	}
	return keyring.Chain(sources...)
}

func buildBackend(bc BackendConfig, key func(ctx context.Context) (string, error), client *llm.Client) (Backend, error) {
	if bc.Model == "" {
		return nil, fmt.Errorf("backend %q has no model", bc.Kind)
	}
	if bc.Threshold != nil && (*bc.Threshold < 0 || *bc.Threshold > 1) {
		return nil, fmt.Errorf("backend %q threshold %v is outside 0..1", bc.Kind, *bc.Threshold)
	}
	var b Backend
	switch bc.Kind {
	case "openrouter":
		if key == nil {
			return nil, errors.New("backend openrouter needs a key source (set key: service, env or file)")
		}
		b = NewOpenRouter(key, bc.Model, bc.zdr())
	case "chat":
		if client == nil {
			return nil, errors.New("backend chat needs a model client")
		}
		b = NewChat(client, bc.Model)
	default:
		return nil, fmt.Errorf("unknown backend kind %q (want openrouter or chat)", bc.Kind)
	}
	if bc.Threshold != nil {
		b = WithThreshold(b, *bc.Threshold)
	}
	return b, nil
}
