package plugin

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func ScanPlugins(dir string) ([]Plugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plugins dir %s: %w", dir, err)
	}

	var plugins []Plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(dir, e.Name(), "plugin.yaml")
		data, err := os.ReadFile(manifestPath) // #nosec G304 -- internal plugin directory
		if err != nil {
			continue
		}
		var p Plugin
		if err := yaml.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.Name == "" || p.Command == "" || len(p.Panels) == 0 {
			continue
		}
		if p.CacheSecs == 0 {
			p.CacheSecs = 30
		}
		plugins = append(plugins, p)
	}
	return plugins, nil
}

func DefaultPluginsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "plugins")
}
