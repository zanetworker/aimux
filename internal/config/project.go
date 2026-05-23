package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectConfigDir is the directory name for project-local config.
const ProjectConfigDir = ".aimux"

// LoadProject reads project-local config from dir/.aimux/config.yaml and
// merges it over the global config. Project values override global values.
// If no project config exists, the global config is returned unchanged.
func LoadProject(projectDir string, global Config) (Config, error) {
	path := filepath.Join(projectDir, ProjectConfigDir, "config.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- path from project dir
	if err != nil {
		if os.IsNotExist(err) {
			return global, nil
		}
		return global, err
	}

	var proj Config
	if err := yaml.Unmarshal(data, &proj); err != nil {
		return global, err
	}

	return mergeOver(global, proj), nil
}

// mergeOver applies non-zero values from overlay onto base.
// Only scalar fields and slice/map fields are merged; nested struct fields
// use simple "non-zero wins" semantics.
func mergeOver(base, overlay Config) Config {
	if overlay.Shell != "" {
		base.Shell = overlay.Shell
	}
	if overlay.RefreshInterval != "" {
		base.RefreshInterval = overlay.RefreshInterval
	}
	if overlay.DefaultRuntime != "" {
		base.DefaultRuntime = overlay.DefaultRuntime
	}
	if overlay.AutoArchiveAfter != "" {
		base.AutoArchiveAfter = overlay.AutoArchiveAfter
	}
	if len(overlay.Badges) > 0 {
		base.Badges = overlay.Badges
	}
	if len(overlay.QuickLaunch.Directories) > 0 {
		base.QuickLaunch = overlay.QuickLaunch
	}
	if overlay.Providers != nil {
		for name, pc := range overlay.Providers {
			base.Providers[name] = pc
		}
	}
	if len(overlay.Runtimes) > 0 {
		if base.Runtimes == nil {
			base.Runtimes = make(map[string]RuntimeConfig)
		}
		for name, rc := range overlay.Runtimes {
			base.Runtimes[name] = rc
		}
	}
	if overlay.Export.Endpoint != "" {
		base.Export = overlay.Export
	}
	if overlay.Notifications != (NotificationsConfig{}) {
		base.Notifications = overlay.Notifications
	}
	return base
}
