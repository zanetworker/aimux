package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProject_MergesOverGlobal(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	_ = os.MkdirAll(projDir, 0750)
	_ = os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte(`
shell: /bin/fish
auto_archive_after: "30m"
badges:
  - path: "go.mod"
    label: "go"
`), 0600)

	global := Default()
	merged, err := LoadProject(dir, global)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if merged.Shell != "/bin/fish" {
		t.Errorf("expected /bin/fish, got %s", merged.Shell)
	}
	if merged.AutoArchiveAfter != "30m" {
		t.Errorf("expected 30m, got %s", merged.AutoArchiveAfter)
	}
	if len(merged.Badges) != 1 {
		t.Errorf("expected 1 badge, got %d", len(merged.Badges))
	}
	if merged.Badges[0].Label != "go" {
		t.Errorf("expected badge label 'go', got %q", merged.Badges[0].Label)
	}
}

func TestLoadProject_NoProjectDir(t *testing.T) {
	global := Default()
	merged, err := LoadProject("/nonexistent", global)
	if err != nil {
		t.Fatalf("LoadProject should not error on missing dir: %v", err)
	}
	if merged.Shell != global.Shell {
		t.Error("missing project config should return global unchanged")
	}
	if merged.RefreshInterval != global.RefreshInterval {
		t.Error("missing project config should preserve global refresh interval")
	}
}

func TestLoadProject_PreservesGlobalProviders(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	_ = os.MkdirAll(projDir, 0750)
	_ = os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte(`
shell: /bin/bash
`), 0600)

	global := Default()
	merged, err := LoadProject(dir, global)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Shell != "/bin/bash" {
		t.Errorf("expected /bin/bash, got %s", merged.Shell)
	}
	if !merged.IsProviderEnabled("claude") {
		t.Error("project config should not wipe global providers")
	}
	if !merged.IsProviderEnabled("codex") {
		t.Error("project config should not wipe global codex provider")
	}
	if !merged.IsProviderEnabled("gemini") {
		t.Error("project config should not wipe global gemini provider")
	}
}

func TestLoadProject_OverridesProviders(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	_ = os.MkdirAll(projDir, 0750)
	_ = os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte(`
providers:
  codex:
    enabled: false
`), 0600)

	global := Default()
	merged, err := LoadProject(dir, global)
	if err != nil {
		t.Fatal(err)
	}
	if merged.IsProviderEnabled("codex") {
		t.Error("project config should disable codex")
	}
	if !merged.IsProviderEnabled("claude") {
		t.Error("claude should remain enabled from global")
	}
}

func TestLoadProject_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	_ = os.MkdirAll(projDir, 0750)
	_ = os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte("{{bad yaml"), 0600)

	global := Default()
	_, err := LoadProject(dir, global)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadProject_PreservesGlobalNotifications(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	_ = os.MkdirAll(projDir, 0750)
	_ = os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte(`
shell: /bin/zsh
`), 0600)

	global := Default()
	merged, err := LoadProject(dir, global)
	if err != nil {
		t.Fatal(err)
	}
	// Notifications should be preserved from global (project didn't override them)
	if !merged.Notifications.Enabled {
		t.Error("expected notifications to remain enabled from global")
	}
	if !merged.Notifications.OnWaiting {
		t.Error("expected on_waiting to remain from global")
	}
}

func TestMergeOver_RefreshInterval(t *testing.T) {
	base := Default()
	overlay := Config{RefreshInterval: "10s"}
	merged := mergeOver(base, overlay)
	if merged.RefreshInterval != "10s" {
		t.Errorf("expected 10s, got %s", merged.RefreshInterval)
	}
}

func TestMergeOver_EmptyOverlayNoChange(t *testing.T) {
	base := Default()
	overlay := Config{}
	merged := mergeOver(base, overlay)
	if merged.Shell != base.Shell {
		t.Errorf("empty overlay should not change shell")
	}
	if merged.RefreshInterval != base.RefreshInterval {
		t.Errorf("empty overlay should not change refresh interval")
	}
	if !merged.IsProviderEnabled("claude") {
		t.Error("empty overlay should not change providers")
	}
}
