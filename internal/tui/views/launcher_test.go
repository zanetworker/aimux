package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testProviderOpts() map[string]ProviderOptions {
	return map[string]ProviderOptions{
		"claude": {Models: []string{"default", "opus", "sonnet", "haiku"}, Modes: []string{"default", "plan", "acceptEdits", "bypass", "dontAsk"}},
		"codex":  {Models: []string{"default", "o3", "o4-mini"}, Modes: []string{"default", "full-auto", "full-access", "read-only"}},
		"gemini": {Models: []string{"default", "gemini-2.5-pro"}, Modes: []string{"default", "yolo", "plan"}},
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
	l := NewLauncherView(nil, testProviderOpts(), false)
	if l.state != statePickProvider {
		t.Errorf("initial state = %d, want statePickProvider", l.state)
	}
	if len(l.providers) != 3 {
		t.Errorf("providers count = %d, want 3", len(l.providers))
	}
}

func TestLauncherProviderNavigation(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false)

	sendKey(l, "j")
	if l.providerCursor != 1 {
		t.Errorf("after j, cursor = %d, want 1", l.providerCursor)
	}

	sendKey(l, "j")
	if l.providerCursor != 2 {
		t.Errorf("after j×2, cursor = %d, want 2", l.providerCursor)
	}

	// Can't go past last
	sendKey(l, "j")
	if l.providerCursor != 2 {
		t.Errorf("after j×3, cursor = %d, want 2 (clamped)", l.providerCursor)
	}

	sendKey(l, "k")
	if l.providerCursor != 1 {
		t.Errorf("after k, cursor = %d, want 1", l.providerCursor)
	}
}

func TestLauncherProviderToDirectory(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false)
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
	l := NewLauncherView(recent, testProviderOpts(), false)
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
	l := NewLauncherView(nil, testProviderOpts(), false)
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
	l := NewLauncherView(recent, testProviderOpts(), false)
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
	l := NewLauncherView(recent, testProviderOpts(), false)
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
	l := NewLauncherView(recent, testProviderOpts(), false)
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
	if launch.Runtime != "tmux" {
		t.Errorf("Runtime = %q, want tmux", launch.Runtime)
	}
}

func TestLauncherEscCancels(t *testing.T) {
	l := NewLauncherView(nil, testProviderOpts(), false)

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
	l := NewLauncherView(recent, testProviderOpts(), false)
	cmd := sendEsc(l)
	msg := cmd()
	if _, ok := msg.(LaunchCancelMsg); !ok {
		t.Error("expected cancel at provider step")
	}

	// Cancel at directory step
	l = NewLauncherView(recent, testProviderOpts(), false)
	sendEnter(l)
	cmd = sendEsc(l)
	msg = cmd()
	if _, ok := msg.(LaunchCancelMsg); !ok {
		t.Error("expected cancel at directory step")
	}

	// Cancel at options step
	l = NewLauncherView(recent, testProviderOpts(), false)
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
	l := NewLauncherView(recent, testProviderOpts(), false)

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
	l := NewLauncherView(nil, testProviderOpts(), false)
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
