package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the agentmux configuration.
type Config struct {
	Providers       map[string]ProviderConfig `yaml:"providers"`
	RefreshInterval string                    `yaml:"refresh_interval"`
	DefaultRuntime  string                    `yaml:"default_runtime"`
}

// ProviderConfig controls a single provider's behaviour.
type ProviderConfig struct {
	Enabled bool   `yaml:"enabled"`
	Binary  string `yaml:"binary,omitempty"`
}

// Default returns the configuration used when no config file is present.
// All known providers are enabled.
func Default() Config {
	return Config{
		Providers: map[string]ProviderConfig{
			"claude": {Enabled: true},
			"codex":  {Enabled: true},
			"gemini": {Enabled: true},
		},
		RefreshInterval: "2s",
		DefaultRuntime:  "tmux",
	}
}

// DefaultPath returns the default config file path:
// ~/.agentmux/config.yaml
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agentmux", "config.yaml")
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
	if fileCfg.Providers != nil {
		for name, pc := range fileCfg.Providers {
			cfg.Providers[name] = pc
		}
	}

	return cfg, nil
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
