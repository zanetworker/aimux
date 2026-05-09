package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanPlugins_ValidManifest(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	_ = os.MkdirAll(pluginDir, 0o750)

	manifest := `
name: my-plugin
tab: MyTab
command: "echo '{\"test\": {\"items\": []}}'"
cache_seconds: 10
panels:
  - id: test
    type: metric-row
    title: Test Panel
`
	_ = os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o600)

	plugins, err := ScanPlugins(dir)
	if err != nil {
		t.Fatalf("ScanPlugins error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Name != "my-plugin" {
		t.Errorf("expected name my-plugin, got %s", plugins[0].Name)
	}
	if plugins[0].Tab != "MyTab" {
		t.Errorf("expected tab MyTab, got %s", plugins[0].Tab)
	}
	if len(plugins[0].Panels) != 1 {
		t.Fatalf("expected 1 panel, got %d", len(plugins[0].Panels))
	}
	if plugins[0].Panels[0].Type != PanelMetricRow {
		t.Errorf("expected metric-row, got %s", plugins[0].Panels[0].Type)
	}
	if plugins[0].CacheSecs != 10 {
		t.Errorf("expected cache_seconds 10, got %d", plugins[0].CacheSecs)
	}
}

func TestScanPlugins_DefaultCacheSecs(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "nocache")
	_ = os.MkdirAll(pluginDir, 0o750)

	manifest := `
name: nocache
tab: Tab
command: echo '{}'
panels:
  - id: x
    type: list
    title: X
`
	_ = os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o600)

	plugins, _ := ScanPlugins(dir)
	if len(plugins) != 1 {
		t.Fatalf("expected 1, got %d", len(plugins))
	}
	if plugins[0].CacheSecs != 30 {
		t.Errorf("expected default 30, got %d", plugins[0].CacheSecs)
	}
}

func TestScanPlugins_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	plugins, err := ScanPlugins(dir)
	if err != nil {
		t.Fatalf("ScanPlugins error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}

func TestScanPlugins_MissingDir(t *testing.T) {
	plugins, err := ScanPlugins("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("should not error on missing dir: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0, got %d", len(plugins))
	}
}

func TestScanPlugins_SkipsInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "bad")
	_ = os.MkdirAll(pluginDir, 0o750)
	_ = os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("not: valid: yaml: {{"), 0o600)

	plugins, err := ScanPlugins(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0, got %d", len(plugins))
	}
}

func TestScanPlugins_SkipsMissingFields(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "incomplete")
	_ = os.MkdirAll(pluginDir, 0o750)
	_ = os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("name: incomplete\ntab: X\n"), 0o600)

	plugins, _ := ScanPlugins(dir)
	if len(plugins) != 0 {
		t.Errorf("expected 0 (missing command and panels), got %d", len(plugins))
	}
}
