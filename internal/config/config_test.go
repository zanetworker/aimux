package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.RefreshInterval != "2s" {
		t.Errorf("RefreshInterval = %q, want %q", cfg.RefreshInterval, "2s")
	}
	if cfg.DefaultRuntime != "tmux" {
		t.Errorf("DefaultRuntime = %q, want %q", cfg.DefaultRuntime, "tmux")
	}

	for _, name := range []string{"claude", "codex", "gemini"} {
		pc, ok := cfg.Providers[name]
		if !ok {
			t.Errorf("provider %q missing from defaults", name)
			continue
		}
		if !pc.Enabled {
			t.Errorf("provider %q should be enabled by default", name)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	p := DefaultPath()
	if p == "" {
		t.Skip("cannot determine home directory")
	}
	if filepath.Base(p) != "config.yaml" {
		t.Errorf("DefaultPath() = %q, want filename config.yaml", p)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("Load(missing file) error = %v, want nil", err)
	}
	// Should return defaults
	if cfg.RefreshInterval != "2s" {
		t.Errorf("RefreshInterval = %q, want default %q", cfg.RefreshInterval, "2s")
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load('') error = %v, want nil", err)
	}
	if cfg.RefreshInterval != "2s" {
		t.Errorf("RefreshInterval = %q, want default %q", cfg.RefreshInterval, "2s")
	}
}

func TestLoad_OverridesDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yaml := `
refresh_interval: "5s"
default_runtime: "iterm"
providers:
  codex:
    enabled: false
  claude:
    enabled: true
    binary: /opt/bin/claude
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.RefreshInterval != "5s" {
		t.Errorf("RefreshInterval = %q, want %q", cfg.RefreshInterval, "5s")
	}
	if cfg.DefaultRuntime != "iterm" {
		t.Errorf("DefaultRuntime = %q, want %q", cfg.DefaultRuntime, "iterm")
	}

	// Codex disabled
	if cfg.Providers["codex"].Enabled {
		t.Error("codex should be disabled")
	}

	// Claude enabled with custom binary
	claude := cfg.Providers["claude"]
	if !claude.Enabled {
		t.Error("claude should be enabled")
	}
	if claude.Binary != "/opt/bin/claude" {
		t.Errorf("claude.Binary = %q, want %q", claude.Binary, "/opt/bin/claude")
	}

	// Gemini should still be enabled (from defaults, not overridden)
	if !cfg.Providers["gemini"].Enabled {
		t.Error("gemini should remain enabled from defaults")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	if err := os.WriteFile(cfgPath, []byte("{{invalid yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Error("Load(invalid yaml) should return error")
	}
}

func TestIsProviderEnabled(t *testing.T) {
	cfg := Default()

	// Known enabled providers
	if !cfg.IsProviderEnabled("claude") {
		t.Error("claude should be enabled")
	}

	// Disable codex
	cfg.Providers["codex"] = ProviderConfig{Enabled: false}
	if cfg.IsProviderEnabled("codex") {
		t.Error("codex should be disabled")
	}

	// Unknown provider defaults to enabled
	if !cfg.IsProviderEnabled("unknown-provider") {
		t.Error("unknown provider should default to enabled")
	}
}

func TestOTELEndpoint_Disabled(t *testing.T) {
	cfg := Default()
	if ep := cfg.OTELEndpoint(); ep != "" {
		t.Errorf("OTELEndpoint when disabled = %q, want empty", ep)
	}
}

func TestOTELEndpoint_Enabled(t *testing.T) {
	cfg := Default()
	cfg.OTELReceiver.Enabled = true
	cfg.OTELReceiver.Port = 4318

	ep := cfg.OTELEndpoint()
	if ep != "http://localhost:4318" {
		t.Errorf("OTELEndpoint = %q, want http://localhost:4318", ep)
	}
}

func TestOTELReceiverPort_Default(t *testing.T) {
	cfg := Default()
	if port := cfg.OTELReceiverPort(); port != 4318 {
		t.Errorf("OTELReceiverPort default = %d, want 4318", port)
	}
}

func TestOTELReceiverPort_Custom(t *testing.T) {
	cfg := Default()
	cfg.OTELReceiver.Port = 9999
	if port := cfg.OTELReceiverPort(); port != 9999 {
		t.Errorf("OTELReceiverPort custom = %d, want 9999", port)
	}
}

func TestNotificationsConfigDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.Notifications.Enabled {
		t.Error("Notifications.Enabled should default to true")
	}
	if !cfg.Notifications.OnWaiting {
		t.Error("Notifications.OnWaiting should default to true")
	}
	if !cfg.Notifications.OnError {
		t.Error("Notifications.OnError should default to true")
	}
	if cfg.Notifications.OnIdle {
		t.Error("Notifications.OnIdle should default to false")
	}
	if cfg.Notifications.Sound {
		t.Error("Notifications.Sound should default to false")
	}
}

func TestDefaultNotificationConfig(t *testing.T) {
	cfg := Default()
	if !cfg.Notifications.Bell {
		t.Error("expected Bell default true")
	}
	if !cfg.Notifications.Desktop {
		t.Error("expected Desktop default true")
	}
	if !cfg.Notifications.OnDone {
		t.Error("expected OnDone default true")
	}
}

func TestNotificationsConfigFromFile(t *testing.T) {
	yamlContent := `
notifications:
  enabled: true
  on_waiting: true
  on_error: true
  on_idle: true
  sound: true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	_ = os.WriteFile(path, []byte(yamlContent), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Notifications.OnIdle {
		t.Error("Notifications.OnIdle should be true from file")
	}
	if !cfg.Notifications.Sound {
		t.Error("Notifications.Sound should be true from file")
	}
}

func TestLoad_PartialProviders(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yaml := `
providers:
  codex:
    enabled: false
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	// Codex disabled
	if cfg.IsProviderEnabled("codex") {
		t.Error("codex should be disabled")
	}

	// Claude and gemini should remain from defaults
	if !cfg.IsProviderEnabled("claude") {
		t.Error("claude should still be enabled from defaults")
	}
	if !cfg.IsProviderEnabled("gemini") {
		t.Error("gemini should still be enabled from defaults")
	}
}

func TestLoadQuickLaunchDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yaml := `
quick_launch:
  directories:
    - /home/user/projects/foo
    - /home/user/projects/bar
    - /opt/workspace
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	want := []string{
		"/home/user/projects/foo",
		"/home/user/projects/bar",
		"/opt/workspace",
	}

	if len(cfg.QuickLaunch.Directories) != len(want) {
		t.Fatalf("QuickLaunch.Directories len = %d, want %d", len(cfg.QuickLaunch.Directories), len(want))
	}

	for i, dir := range cfg.QuickLaunch.Directories {
		if dir != want[i] {
			t.Errorf("QuickLaunch.Directories[%d] = %q, want %q", i, dir, want[i])
		}
	}
}

func TestLoadTasksConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yaml := `
tasks:
  backend: mcp
  mcp_endpoint: http://localhost:3000
  default_list: my-task-list-123
  prompt_template: "Custom task: {title}. Notes: {notes}. User: {user_prompt}. Go!"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Tasks.Backend != "mcp" {
		t.Errorf("Tasks.Backend = %q, want %q", cfg.Tasks.Backend, "mcp")
	}
	if cfg.Tasks.MCPEndpoint != "http://localhost:3000" {
		t.Errorf("Tasks.MCPEndpoint = %q, want %q", cfg.Tasks.MCPEndpoint, "http://localhost:3000")
	}
	if cfg.Tasks.DefaultList != "my-task-list-123" {
		t.Errorf("Tasks.DefaultList = %q, want %q", cfg.Tasks.DefaultList, "my-task-list-123")
	}
	wantTemplate := "Custom task: {title}. Notes: {notes}. User: {user_prompt}. Go!"
	if cfg.Tasks.PromptTemplate != wantTemplate {
		t.Errorf("Tasks.PromptTemplate = %q, want %q", cfg.Tasks.PromptTemplate, wantTemplate)
	}
}

func TestDefaultPromptTemplate(t *testing.T) {
	cfg := Default()

	if cfg.Tasks.Backend != "auto" {
		t.Errorf("Tasks.Backend default = %q, want %q", cfg.Tasks.Backend, "auto")
	}

	if cfg.Tasks.PromptTemplate == "" {
		t.Error("Tasks.PromptTemplate should have a non-empty default")
	}

	// Verify template contains expected placeholders
	wantTemplate := "Work on the following task: {title}\n\nDetails: {notes}\n\nAdditional instructions: {user_prompt}\n\nWhen done, summarize what you did."
	if cfg.Tasks.PromptTemplate != wantTemplate {
		t.Errorf("Tasks.PromptTemplate = %q, want %q", cfg.Tasks.PromptTemplate, wantTemplate)
	}
}
