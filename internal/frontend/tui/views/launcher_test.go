package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testProviderOpts() map[string]ProviderOptions {
	return map[string]ProviderOptions{
		"claude": {Models: []string{"default", "opus", "sonnet", "haiku"}, Modes: []string{"default", "plan", "acceptEdits", "bypass", "dontAsk"}},
		"codex":  {Models: []string{"default", "o3", "o4-mini"}, Modes: []string{"default", "full-auto", "full-access", "read-only"}},
	}
}

func sendKey(l *LauncherView, key string) tea.Cmd {
	return l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func sendEnter(l *LauncherView) tea.Cmd {
	return l.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func sendEsc(l *LauncherView) tea.Cmd {
	return l.Update(tea.KeyMsg{Type: tea.KeyEsc})
}

func sendTab(l *LauncherView) tea.Cmd {
	return l.Update(tea.KeyMsg{Type: tea.KeyTab})
}

func TestLauncherInitialState(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	if l.state != statePickProvider {
		t.Errorf("initial state = %d, want statePickProvider", l.state)
	}
	if len(l.providers) != 2 {
		t.Errorf("providers count = %d, want 2", len(l.providers))
	}
}

func TestLauncherProviderNavigation(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})

	sendKey(l, "j")
	if l.providerCursor != 1 {
		t.Errorf("after j, cursor = %d, want 1", l.providerCursor)
	}

	sendKey(l, "j")
	if l.providerCursor != 1 {
		t.Errorf("after j×2, cursor = %d, want 1", l.providerCursor)
	}

	// Can't go past last
	sendKey(l, "j")
	if l.providerCursor != 1 {
		t.Errorf("after j×3, cursor = %d, want 1 (clamped)", l.providerCursor)
	}

	sendKey(l, "k")
	if l.providerCursor != 0 {
		t.Errorf("after k, cursor = %d, want 0", l.providerCursor)
	}
}

func TestLauncherProviderToDirectory(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l) // pick first provider (claude)
	if l.state != statePickDirectory {
		t.Errorf("state = %d, want statePickDirectory", l.state)
	}
}

func TestLauncherRecentDirSelection(t *testing.T) {
	recent := []RecentDirEntry{
		{Path: "/tmp/project-a", Display: "project-a", Age: "2m ago"},
		{Path: "/tmp/project-b", Display: "project-b", Age: "1h ago"},
	}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l) // pick provider

	// Should be in recent mode by default
	if l.browseMode {
		t.Error("expected recent mode by default")
	}

	sendKey(l, "j") // move to project-b
	sendEnter(l)     // select

	if l.state != statePickOptions {
		t.Errorf("state = %d, want statePickOptions", l.state)
	}
}

func TestLauncherTabSwitchesMode(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l) // pick provider

	if l.browseMode {
		t.Error("expected recent mode initially")
	}

	sendTab(l)
	if !l.browseMode {
		t.Error("expected browse mode after Tab")
	}

	sendTab(l)
	if l.browseMode {
		t.Error("expected recent mode after second Tab")
	}
}

func TestLauncherFuzzyFilter(t *testing.T) {
	recent := []RecentDirEntry{
		{Path: "/tmp/aimux", Display: "aimux"},
		{Path: "/tmp/blog", Display: "blog"},
		{Path: "/tmp/remote-claude", Display: "remote-claude"},
	}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l) // pick provider

	sendKey(l, "b") // filter by "b"
	filtered := l.filteredRecent()
	if len(filtered) != 1 {
		t.Errorf("filtered count = %d, want 1 (blog)", len(filtered))
	}
	if filtered[0].Display != "blog" {
		t.Errorf("filtered[0] = %q, want blog", filtered[0].Display)
	}
}

func TestLauncherOptionsNavigation(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l) // provider
	sendEnter(l) // directory

	if l.state != statePickOptions {
		t.Fatalf("state = %d, want statePickOptions", l.state)
	}

	// Default field is model (0)
	if l.optionField != 0 {
		t.Errorf("optionField = %d, want 0", l.optionField)
	}

	// Navigate right to select a model
	sendKey(l, "l")
	if l.modelCursor != 1 {
		t.Errorf("modelCursor = %d, want 1", l.modelCursor)
	}

	// Navigate down to mode field
	sendKey(l, "j")
	if l.optionField != 1 {
		t.Errorf("optionField = %d, want 1", l.optionField)
	}
}

func TestLauncherEmitLaunch(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/myproject", Display: "myproject"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l) // provider: claude
	sendEnter(l) // directory: /tmp/myproject
	cmd := sendEnter(l) // launch with defaults

	if cmd == nil {
		t.Fatal("expected LaunchMsg command, got nil")
	}

	msg := cmd()
	launch, ok := msg.(LaunchMsg)
	if !ok {
		t.Fatalf("expected LaunchMsg, got %T", msg)
	}

	if launch.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", launch.Provider)
	}
	if launch.Dir != "/tmp/myproject" {
		t.Errorf("Dir = %q, want /tmp/myproject", launch.Dir)
	}
	if launch.Model != "" {
		t.Errorf("Model = %q, want empty (default)", launch.Model)
	}
	if launch.Runtime != "local" {
		t.Errorf("Runtime = %q, want local", launch.Runtime)
	}
	if launch.Execution != "local" {
		t.Errorf("Execution = %q, want local", launch.Execution)
	}
	if launch.Shell == "" {
		t.Error("Shell should not be empty")
	}
	if launch.SessionManager != "tmux" {
		t.Errorf("SessionManager = %q, want tmux", launch.SessionManager)
	}
}

func TestLauncherEscCancels(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})

	cmd := sendEsc(l)
	if cmd == nil {
		t.Fatal("expected LaunchCancelMsg, got nil")
	}

	msg := cmd()
	if _, ok := msg.(LaunchCancelMsg); !ok {
		t.Fatalf("expected LaunchCancelMsg, got %T", msg)
	}
}

func TestLauncherEscCancelsAtEachStep(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}

	// Cancel at provider step
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	cmd := sendEsc(l)
	msg := cmd()
	if _, ok := msg.(LaunchCancelMsg); !ok {
		t.Error("expected cancel at provider step")
	}

	// Cancel at directory step
	l = NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l)
	cmd = sendEsc(l)
	msg = cmd()
	if _, ok := msg.(LaunchCancelMsg); !ok {
		t.Error("expected cancel at directory step")
	}

	// Cancel at options step
	l = NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	sendEnter(l)
	sendEnter(l)
	cmd = sendEsc(l)
	msg = cmd()
	if _, ok := msg.(LaunchCancelMsg); !ok {
		t.Error("expected cancel at options step")
	}
}

func TestLauncherSelectCodex(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})

	sendKey(l, "j") // move to codex
	sendEnter(l)     // pick codex
	sendEnter(l)     // pick dir
	cmd := sendEnter(l) // launch

	msg := cmd().(LaunchMsg)
	if msg.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", msg.Provider)
	}
}

func TestLauncherViewRenders(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false, LauncherConfig{DefaultRuntime: "local"})
	l.SetSize(80, 40)
	view := l.View()
	if view == "" {
		t.Error("View() returned empty string")
	}
	if !containsStr(view, "Launch Agent") {
		t.Error("View should contain 'Launch Agent'")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestLauncherQuickDirs(t *testing.T) {
	quickDirs := []string{"/tmp/project-a", "/tmp/project-b"}
	recent := []RecentDirEntry{{Path: "/tmp/other", Display: "other"}}
	opts := testProviderOpts()
	lv := NewLauncherView(recent, opts, false, LauncherConfig{DefaultRuntime: "local"})
	lv.SetQuickDirs(quickDirs)
	lv.SetSize(80, 24)

	// Advance past provider
	sendEnter(lv)

	// Should be on Quick tab by default when quickDirs set
	if !lv.quickMode {
		t.Error("expected quickMode to be true when quick dirs are set")
	}

	// View should contain Quick tab
	view := lv.View()
	if !containsStr(view, "Quick") {
		t.Error("expected Quick tab in view")
	}
	if !containsStr(view, "project-a") {
		t.Error("expected project-a in view")
	}

	// Navigate within Quick dirs
	sendKey(lv, "j")
	if lv.quickCursor != 1 {
		t.Errorf("quickCursor = %d, want 1", lv.quickCursor)
	}

	// Select second quick dir
	sendEnter(lv)
	if lv.state != statePickOptions {
		t.Errorf("state = %d, want statePickOptions", lv.state)
	}

	// Verify selected directory is the second quick dir
	if lv.selectedDir() != "/tmp/project-b" {
		t.Errorf("selectedDir = %q, want /tmp/project-b", lv.selectedDir())
	}
}

func TestLauncherThreeTabCycling(t *testing.T) {
	quickDirs := []string{"/tmp/quick-project"}
	recent := []RecentDirEntry{{Path: "/tmp/recent-project", Display: "recent-project"}}
	opts := testProviderOpts()
	lv := NewLauncherView(recent, opts, false, LauncherConfig{DefaultRuntime: "local"})
	lv.SetQuickDirs(quickDirs)
	lv.SetSize(80, 24)

	// Advance past provider
	sendEnter(lv)

	// Should start on Quick tab
	if !lv.quickMode || lv.browseMode {
		t.Error("expected Quick mode initially")
	}

	// Tab to Recent
	sendTab(lv)
	if lv.quickMode || lv.browseMode {
		t.Error("expected Recent mode after first Tab")
	}

	// Tab to Browse
	sendTab(lv)
	if lv.quickMode || !lv.browseMode {
		t.Error("expected Browse mode after second Tab")
	}

	// Tab back to Quick
	sendTab(lv)
	if !lv.quickMode || lv.browseMode {
		t.Error("expected Quick mode after third Tab")
	}
}

func TestLauncherEnvironmentSelection_MultipleEnvs(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		Environments:   []string{"local", "sandbox", "cluster"},
	})

	if !l.hasMultipleEnvironments() {
		t.Fatal("expected hasMultipleEnvironments() to be true")
	}
	if len(l.environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(l.environments))
	}

	sendEnter(l) // provider
	sendEnter(l) // directory

	// Navigate to field 2 (environment)
	sendKey(l, "j") // field 1 (permissions)
	sendKey(l, "j") // field 2 (environment)
	if l.optionField != 2 {
		t.Fatalf("optionField = %d, want 2", l.optionField)
	}

	// Default cursor at 0 (local)
	if l.envCursor != 0 {
		t.Errorf("envCursor = %d, want 0", l.envCursor)
	}

	// Navigate right to sandbox
	sendKey(l, "l")
	if l.envCursor != 1 {
		t.Errorf("envCursor = %d, want 1 (sandbox)", l.envCursor)
	}

	// Launch
	cmd := sendEnter(l)
	if cmd == nil {
		t.Fatal("expected LaunchMsg, got nil")
	}
	msg := cmd().(LaunchMsg)
	if msg.Environment != "sandbox" {
		t.Errorf("Environment = %q, want sandbox", msg.Environment)
	}
}

func TestLauncherEnvironmentSelection_SingleEnv(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		Environments:   []string{"local"},
	})

	if l.hasMultipleEnvironments() {
		t.Fatal("expected hasMultipleEnvironments() to be false with single env")
	}

	sendEnter(l) // provider
	sendEnter(l) // directory

	// Navigate to field 2 (should be Runtime, not Environment)
	sendKey(l, "j") // field 1
	sendKey(l, "j") // field 2

	// Navigate right should change runtime, not env
	sendKey(l, "l")
	if l.runtimeCursor != 1 {
		t.Errorf("runtimeCursor = %d, want 1 (container)", l.runtimeCursor)
	}

	// Launch should have empty Environment (backward compat)
	sendKey(l, "h") // back to local runtime
	cmd := sendEnter(l)
	msg := cmd().(LaunchMsg)
	if msg.Environment != "" {
		t.Errorf("Environment = %q, want empty for single-env", msg.Environment)
	}
	if msg.Runtime != "local" {
		t.Errorf("Runtime = %q, want local", msg.Runtime)
	}
}

func TestLauncherEnvironmentSelection_NoEnvsConfigured(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
	})

	if l.hasMultipleEnvironments() {
		t.Fatal("expected hasMultipleEnvironments() to be false with no envs")
	}
	if len(l.environments) != 1 || l.environments[0] != "local" {
		t.Errorf("environments = %v, want [local]", l.environments)
	}
}

func TestLauncherEnvironmentViewRenders(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		Environments:   []string{"local", "sandbox"},
	})
	l.SetSize(80, 40)

	sendEnter(l) // provider
	sendEnter(l) // directory

	view := l.View()
	if !containsStr(view, "Environment:") {
		t.Error("expected 'Environment:' in options view with multiple envs")
	}
}

func TestLauncherNamedConfigs_ShowsConfiguredSection(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	configs := []AgentConfigEntry{
		{Name: "reviewer", Runtime: "claude", Model: "opus"},
		{Name: "writer", Runtime: "codex", Model: "o3"},
	}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		AgentConfigs:   configs,
	})
	l.SetSize(80, 40)

	view := l.View()
	if !containsStr(view, "Configured:") {
		t.Error("expected 'Configured:' section in provider view")
	}
	if !containsStr(view, "Quick Launch:") {
		t.Error("expected 'Quick Launch:' section in provider view")
	}
	if !containsStr(view, "reviewer") {
		t.Error("expected 'reviewer' config in view")
	}
}

func TestLauncherNamedConfigs_SelectConfigSetsProvider(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	configs := []AgentConfigEntry{
		{Name: "reviewer", Runtime: "claude", Model: "opus"},
	}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		AgentConfigs:   configs,
	})

	// Cursor starts at 0 (first config: "reviewer")
	sendEnter(l) // select "reviewer" config
	sendEnter(l) // select directory

	// Launch
	cmd := sendEnter(l)
	msg := cmd().(LaunchMsg)
	if msg.Provider != "claude" {
		t.Errorf("Provider = %q, want claude (from config runtime)", msg.Provider)
	}
	if msg.AgentConfig != "reviewer" {
		t.Errorf("AgentConfig = %q, want reviewer", msg.AgentConfig)
	}
}

func TestLauncherNamedConfigs_SelectRawProvider(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	configs := []AgentConfigEntry{
		{Name: "reviewer", Runtime: "claude", Model: "opus"},
	}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		AgentConfigs:   configs,
	})

	// Move past config to raw providers (1=claude, 2=codex)
	sendKey(l, "j") // cursor 1 = claude (raw)
	sendEnter(l)     // select claude raw
	sendEnter(l)     // select directory

	cmd := sendEnter(l)
	msg := cmd().(LaunchMsg)
	if msg.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", msg.Provider)
	}
	if msg.AgentConfig != "" {
		t.Errorf("AgentConfig = %q, want empty for raw provider", msg.AgentConfig)
	}
}

func TestLauncherNamedConfigs_NoConfigsShowsProvider(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
	})
	l.SetSize(80, 40)

	view := l.View()
	if !containsStr(view, "Provider:") {
		t.Error("expected 'Provider:' when no configs")
	}
	if containsStr(view, "Configured:") {
		t.Error("should not show 'Configured:' when no configs")
	}
}

func TestLauncherRuntimeViewRenders(t *testing.T) {
	recent := []RecentDirEntry{{Path: "/tmp/test", Display: "test"}}
	l := NewLauncherView(recent, testProviderOpts(), false, LauncherConfig{
		DefaultRuntime: "local",
		Environments:   []string{"local"},
	})
	l.SetSize(80, 40)

	sendEnter(l) // provider
	sendEnter(l) // directory

	view := l.View()
	if !containsStr(view, "Runtime:") {
		t.Error("expected 'Runtime:' in options view with single env")
	}
}
