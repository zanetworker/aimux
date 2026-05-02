package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the aimux configuration.
type Config struct {
	Providers       map[string]ProviderConfig `yaml:"providers"`
	RefreshInterval string                    `yaml:"refresh_interval"`
	DefaultRuntime  string                    `yaml:"default_runtime"`
	Shell           string                    `yaml:"shell"`       // login shell for spawning agents
	Export          ExportConfig              `yaml:"export"`      // OTEL export settings
	OTELReceiver    OTELReceiverConfig        `yaml:"otel"`        // OTEL receiver settings
	Sessions        SessionsConfig            `yaml:"sessions"`    // session history settings
	Notifications   NotificationsConfig       `yaml:"notifications"` // macOS notification settings
	Kubernetes      K8sProviderConfig         `yaml:"kubernetes"`  // Kubernetes provider settings
}

// K8sProviderConfig holds connection settings for the Kubernetes agent provider.
// Agents are discovered via Redis heartbeats rather than local process scanning.
//
// K8s is considered enabled when RedisURL is set (Enabled field is kept for
// backward compatibility but RedisURL is the real gate).
type K8sProviderConfig struct {
	Enabled      bool   `yaml:"enabled"`
	RedisURL     string `yaml:"redis_url"`      // e.g. "redis://:pass@localhost:6380"
	TeamID       string `yaml:"team_id"`        // Redis team key prefix, e.g. "my-team"
	Namespace    string `yaml:"namespace"`      // K8s namespace, e.g. "agents"
	Kubeconfig   string `yaml:"kubeconfig"`     // path to kubeconfig; empty = KUBECONFIG env or in-cluster
	OTELEndpoint string `yaml:"otel_endpoint"`  // e.g. "http://<elb>:4318" — OTel Collector for remote agent traces
}

// IsActive returns true when K8s is configured and usable.
// Requires both enabled flag and a Redis URL.
func (c K8sProviderConfig) IsActive() bool {
	return c.Enabled && c.RedisURL != ""
}

// SessionsConfig holds settings for the session history feature.
type SessionsConfig struct {
	AutoTitle  bool   `yaml:"auto_title"`  // generate titles via LLM on discovery
	TitleModel string `yaml:"title_model"` // "flash" (default), "haiku", "sonnet", "opus"
	APIKey     string `yaml:"api_key"`     // API key for title generation (overrides env vars)
}

// NotificationsConfig controls macOS notification behavior.
type NotificationsConfig struct {
	Enabled   bool `yaml:"enabled"`    // master switch (default: true)
	OnWaiting bool `yaml:"on_waiting"` // agent needs permission (default: true)
	OnError   bool `yaml:"on_error"`   // agent crashed (default: true)
	OnIdle    bool `yaml:"on_idle"`    // agent finished turn (default: false)
	OnDone    bool `yaml:"on_done"`    // agent finished (default: true)
	Sound     bool `yaml:"sound"`      // play macOS sound (default: false)
	Bell      bool `yaml:"bell"`       // terminal bell on attention events (default: true)
	Desktop   bool `yaml:"desktop"`    // macOS notification center (default: true)
}

// ExportConfig holds settings for exporting traces via OTLP/HTTP.
type ExportConfig struct {
	Endpoint string `yaml:"endpoint"` // OTLP/HTTP endpoint, e.g., "localhost:5001"
	Insecure bool   `yaml:"insecure"` // true for HTTP (no TLS), default true
	Headers  map[string]string `yaml:"headers,omitempty"` // extra HTTP headers for the export endpoint

	// Backend-specific settings
	MLflow MLflowConfig `yaml:"mlflow,omitempty"` // MLflow-specific config
}

// MLflowConfig holds MLflow-specific export settings.
type MLflowConfig struct {
	ExperimentID string `yaml:"experiment_id"` // MLflow experiment ID
}

// OTELReceiverConfig holds settings for the embedded OTLP/HTTP receiver.
type OTELReceiverConfig struct {
	Enabled bool `yaml:"enabled"` // false by default
	Port    int  `yaml:"port"`    // default 4318
}

// ProviderConfig controls a single provider's behaviour.
type ProviderConfig struct {
	Enabled bool   `yaml:"enabled"`
	Binary  string `yaml:"binary,omitempty"`
}

// Default returns the configuration used when no config file is present.
// All known providers are enabled. The Kubernetes provider is disabled by
// default because it requires a Redis URL and team ID to be useful.
func Default() Config {
	return Config{
		Providers: map[string]ProviderConfig{
			"claude": {Enabled: true},
			"codex":  {Enabled: true},
			"gemini": {Enabled: true},
		},
		RefreshInterval: "2s",
		DefaultRuntime:  "tmux",
		Sessions: SessionsConfig{
			TitleModel: "flash",
		},
		Notifications: NotificationsConfig{
			Enabled:   true,
			OnWaiting: true,
			OnError:   true,
			OnIdle:    false,
			OnDone:    true,
			Sound:     false,
			Bell:      true,
			Desktop:   true,
		},
		Kubernetes: K8sProviderConfig{
			Enabled:   false,
			Namespace: "agents",
		},
	}
}

// DefaultPath returns the default config file path:
// ~/.aimux/config.yaml
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "config.yaml")
}

// Load reads a YAML config file and merges it with the defaults.
// If the file does not exist, Default() is returned with no error.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	var fileCfg Config
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return cfg, err
	}

	// Merge: file values override defaults
	if fileCfg.RefreshInterval != "" {
		cfg.RefreshInterval = fileCfg.RefreshInterval
	}
	if fileCfg.DefaultRuntime != "" {
		cfg.DefaultRuntime = fileCfg.DefaultRuntime
	}
	if fileCfg.Shell != "" {
		cfg.Shell = fileCfg.Shell
	}
	if fileCfg.Export.Endpoint != "" {
		cfg.Export = fileCfg.Export
	}
	if fileCfg.OTELReceiver.Enabled {
		cfg.OTELReceiver = fileCfg.OTELReceiver
	}
	if fileCfg.Providers != nil {
		for name, pc := range fileCfg.Providers {
			cfg.Providers[name] = pc
		}
	}
	if fileCfg.Sessions.TitleModel != "" {
		cfg.Sessions.TitleModel = fileCfg.Sessions.TitleModel
	}
	if fileCfg.Sessions.AutoTitle {
		cfg.Sessions.AutoTitle = fileCfg.Sessions.AutoTitle
	}
	if fileCfg.Sessions.APIKey != "" {
		cfg.Sessions.APIKey = fileCfg.Sessions.APIKey
	}
	// If the notifications section is present at all, take its values wholesale.
	// Since YAML bool false and "not present" are indistinguishable, a user who
	// sets e.g. notifications: { enabled: false } will also get on_waiting: false
	// etc. This is acceptable — if the section is present, the user is explicitly
	// configuring it.
	if fileCfg.Notifications != (NotificationsConfig{}) {
		cfg.Notifications = fileCfg.Notifications
	}
	if fileCfg.Kubernetes.Enabled {
		cfg.Kubernetes = fileCfg.Kubernetes
	}

	return cfg, nil
}

// ResolveShell returns the shell to use for spawning agents in tmux sessions.
// Priority: config shell > $SHELL env var > /bin/sh (POSIX fallback).
func (c Config) ResolveShell() string {
	if c.Shell != "" {
		return c.Shell
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// ShellRCPrefix returns a shell command prefix that sources the user's RC file.
// This ensures shell functions and env vars are available when running commands
// via login shell (zsh -lc alone doesn't source .zshrc for non-interactive shells).
func ShellRCPrefix(shell string) string {
	base := filepath.Base(shell)
	switch {
	case strings.Contains(base, "zsh"):
		return "source ~/.zshrc 2>/dev/null; "
	case strings.Contains(base, "bash"):
		return "source ~/.bashrc 2>/dev/null; "
	case strings.Contains(base, "fish"):
		return "source ~/.config/fish/config.fish 2>/dev/null; "
	default:
		return ""
	}
}

// OTELReceiverPort returns the configured OTEL receiver port, defaulting to 4318.
func (c Config) OTELReceiverPort() int {
	if c.OTELReceiver.Port > 0 {
		return c.OTELReceiver.Port
	}
	return 4318
}

// OTELEndpoint returns the OTEL receiver endpoint URL, or "" if disabled.
func (c Config) OTELEndpoint() string {
	if !c.OTELReceiver.Enabled {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d", c.OTELReceiverPort())
}

// IsProviderEnabled returns true if the named provider is enabled in the config.
// Unknown providers (not in the map) are enabled by default.
func (c Config) IsProviderEnabled(name string) bool {
	pc, ok := c.Providers[name]
	if !ok {
		return true // unknown providers enabled by default
	}
	return pc.Enabled
}
