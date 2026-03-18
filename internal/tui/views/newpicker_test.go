package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyMsg(k string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// healthyK8s returns a K8sHealth indicating all infrastructure is ready.
func healthyK8s() *K8sHealth {
	return &K8sHealth{
		Configured:  true,
		RedisOK:     true,
		ClusterOK:   true,
		Deployments: []string{"agent-claude-coder", "agent-claude-reviewer"},
	}
}

// defaultProviders returns the standard test provider support matrix.
func defaultProviders() []ProviderSupport {
	return []ProviderSupport{
		{Name: "claude", LocalSession: true, LocalK8s: true, RemoteSession: true, RemoteTask: true},
		{Name: "codex", LocalSession: true},
		{Name: "gemini", LocalSession: true, RemoteTask: true},
	}
}

// claudeGeminiProviders returns providers without codex.
func claudeGeminiProviders() []ProviderSupport {
	return []ProviderSupport{
		{Name: "claude", LocalSession: true, LocalK8s: true, RemoteSession: true, RemoteTask: true},
		{Name: "gemini", LocalSession: true, RemoteTask: true},
	}
}

func specialKeyMsg(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

// defaultTestConfig returns a config with the default provider support matrix.
func defaultTestConfig() NewPickerConfig {
	return NewPickerConfig{
		Providers: DefaultProviderSupport(),
	}
}

func TestNewPickerView_InitialState(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	if v.Level() != npLevelPicker {
		t.Errorf("expected level npLevelPicker, got %d", v.Level())
	}
	if v.pickerCursor != 0 {
		t.Errorf("expected pickerCursor 0, got %d", v.pickerCursor)
	}
}

func TestNewPickerView_SKeyMovesToSession(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("s"))
	if v.Level() != npLevelSession {
		t.Errorf("expected npLevelSession after S, got %d", v.Level())
	}
}

func TestNewPickerView_TKeyMovesToTask(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("t"))
	if v.Level() != npLevelTask {
		t.Errorf("expected npLevelTask after T, got %d", v.Level())
	}
}

func TestNewPickerView_EnterOnSessionMovesToSession(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	// cursor is 0 (Session) by default
	v.Update(specialKeyMsg(tea.KeyEnter))
	if v.Level() != npLevelSession {
		t.Errorf("expected npLevelSession after Enter on cursor 0, got %d", v.Level())
	}
}

func TestNewPickerView_EnterOnTaskMovesToTask(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("j")) // move to Task
	v.Update(specialKeyMsg(tea.KeyEnter))
	if v.Level() != npLevelTask {
		t.Errorf("expected npLevelTask after Enter on cursor 1, got %d", v.Level())
	}
}

func TestNewPickerView_EscFromSessionReturnsToPicker(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("s")) // go to session
	if v.Level() != npLevelSession {
		t.Fatalf("precondition: expected npLevelSession")
	}
	v.Update(specialKeyMsg(tea.KeyEsc))
	if v.Level() != npLevelPicker {
		t.Errorf("expected npLevelPicker after Esc from session, got %d", v.Level())
	}
}

func TestNewPickerView_EscFromTaskReturnsToPicker(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("t")) // go to task
	v.Update(specialKeyMsg(tea.KeyEsc))
	if v.Level() != npLevelPicker {
		t.Errorf("expected npLevelPicker after Esc from task, got %d", v.Level())
	}
}

func TestNewPickerView_EscFromPickerEmitsCancel(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	_, cmd := v.Update(specialKeyMsg(tea.KeyEsc))
	if cmd == nil {
		t.Fatal("expected a command from Esc at picker level")
	}
	msg := cmd()
	if _, ok := msg.(NewPickerCancelMsg); !ok {
		t.Errorf("expected NewPickerCancelMsg, got %T", msg)
	}
}

func TestNewPickerView_QClosesOverlay(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	_, cmd := v.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("expected a command from q")
	}
	msg := cmd()
	if _, ok := msg.(NewPickerCancelMsg); !ok {
		t.Errorf("expected NewPickerCancelMsg, got %T", msg)
	}
}

func TestNewPickerView_SessionTabCyclesWhereOptions(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s")) // go to session

	// field 0 = where, default cursor 0 = Local
	if v.sessionWhereCursor != 0 {
		t.Fatalf("expected sessionWhereCursor 0, got %d", v.sessionWhereCursor)
	}

	// right arrow to cycle
	v.Update(keyMsg("l"))
	if v.sessionWhereCursor != 1 {
		t.Errorf("expected sessionWhereCursor 1 after right, got %d", v.sessionWhereCursor)
	}

	v.Update(keyMsg("l"))
	if v.sessionWhereCursor != 2 {
		t.Errorf("expected sessionWhereCursor 2 after two rights, got %d", v.sessionWhereCursor)
	}

	// shouldn't go past the end
	v.Update(keyMsg("l"))
	if v.sessionWhereCursor != 2 {
		t.Errorf("expected sessionWhereCursor to stay at 2, got %d", v.sessionWhereCursor)
	}
}

func TestNewPickerView_SessionEnterEmitsNewSessionMsg(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.RecentDirs = []RecentDirEntry{
		{Path: "/home/user/project-a", Display: "project-a", Age: "2m"},
	}
	v := NewNewPickerView(cfg)
	v.Update(keyMsg("s")) // go to session

	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command from Enter in session launcher")
	}
	msg := cmd()
	sm, ok := msg.(NewSessionMsg)
	if !ok {
		t.Fatalf("expected NewSessionMsg, got %T", msg)
	}
	if sm.Where != "local" {
		t.Errorf("expected Where=local, got %s", sm.Where)
	}
	if sm.Provider != "claude" {
		t.Errorf("expected Provider=claude, got %s", sm.Provider)
	}
	// Dir is empty — directory picking is delegated to the old launcher
	if sm.Dir != "" {
		t.Errorf("expected empty Dir, got %s", sm.Dir)
	}
}

func TestNewPickerView_TaskEnterEmitsNewTaskMsg(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("t")) // go to task

	// Move to prompt field and type
	v.Update(specialKeyMsg(tea.KeyTab)) // field 1 (provider)
	v.Update(specialKeyMsg(tea.KeyTab)) // field 2 (prompt)
	v.Update(keyMsg("h"))
	v.Update(keyMsg("i"))

	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command from Enter in task launcher")
	}
	msg := cmd()
	tm, ok := msg.(NewTaskMsg)
	if !ok {
		t.Fatalf("expected NewTaskMsg, got %T", msg)
	}
	if tm.Where != "local" {
		t.Errorf("expected Where=local, got %s", tm.Where)
	}
	if tm.Provider != "claude" {
		t.Errorf("expected Provider=claude, got %s", tm.Provider)
	}
	if tm.Prompt != "hi" {
		t.Errorf("expected Prompt=hi, got %s", tm.Prompt)
	}
}

func TestNewPickerView_K8sOptionsHiddenWhenDisabled(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: false,
		Providers:  DefaultProviderSupport(),
	})

	if len(v.sessionWhereOptions) != 1 {
		t.Errorf("expected 1 session where option without K8s, got %d: %v", len(v.sessionWhereOptions), v.sessionWhereOptions)
	}
	if v.sessionWhereOptions[0] != "Local" {
		t.Errorf("expected only Local option, got %s", v.sessionWhereOptions[0])
	}

	if len(v.taskWhereOptions) != 1 {
		t.Errorf("expected 1 task where option without K8s, got %d: %v", len(v.taskWhereOptions), v.taskWhereOptions)
	}
	if v.taskWhereOptions[0] != "Local" {
		t.Errorf("expected only Local option, got %s", v.taskWhereOptions[0])
	}
}

func TestNewPickerView_K8sOptionsShownWhenEnabled(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})

	if len(v.sessionWhereOptions) != 3 {
		t.Errorf("expected 3 session where options with K8s, got %d: %v", len(v.sessionWhereOptions), v.sessionWhereOptions)
	}

	if len(v.taskWhereOptions) != 2 {
		t.Errorf("expected 2 task where options with K8s, got %d: %v", len(v.taskWhereOptions), v.taskWhereOptions)
	}
}

func TestNewPickerView_SessionRemoteWhereValue(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s"))

	// Move to Remote (pod) -- index 2
	v.Update(keyMsg("l"))
	v.Update(keyMsg("l"))

	// Claude is supported for remote, so this should emit
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command from Enter with claude+remote")
	}
	msg := cmd()
	sm := msg.(NewSessionMsg)
	if sm.Where != "remote" {
		t.Errorf("expected Where=remote, got %s", sm.Where)
	}
}

func TestNewPickerView_SessionLocalK8sWhereValue(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s"))

	// Move to Local+K8s -- index 1
	v.Update(keyMsg("l"))

	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command from Enter with claude+local-k8s")
	}
	msg := cmd()
	sm := msg.(NewSessionMsg)
	if sm.Where != "local-k8s" {
		t.Errorf("expected Where=local-k8s, got %s", sm.Where)
	}
}

func TestNewPickerView_TaskRemoteWhereValue(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("t"))

	// Move to Remote -- index 1
	v.Update(keyMsg("l"))

	// Need to type a prompt first
	v.Update(specialKeyMsg(tea.KeyTab)) // provider
	v.Update(specialKeyMsg(tea.KeyTab)) // prompt
	v.Update(keyMsg("g"))
	v.Update(keyMsg("o"))

	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command from Enter with claude+remote task")
	}
	msg := cmd()
	tm := msg.(NewTaskMsg)
	if tm.Where != "remote" {
		t.Errorf("expected Where=remote, got %s", tm.Where)
	}
}

func TestNewPickerView_TaskProvidersIncludeAll(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	// Task providers should include all providers (greying handled at render time)
	if len(v.taskProviders) != 3 {
		t.Errorf("expected 3 task providers, got %d: %v", len(v.taskProviders), v.taskProviders)
	}
}

func TestNewPickerView_ViewRendersWithoutPanic(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.SetSize(80, 24)

	// Render all three levels without panicking
	_ = v.View()

	v.Update(keyMsg("s"))
	_ = v.View()

	v.Update(specialKeyMsg(tea.KeyEsc))
	v.Update(keyMsg("t"))
	_ = v.View()
}

func TestNewPickerView_SessionEnterEmitsAndDelegatesToLauncher(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.SetSize(100, 30)
	v.Update(keyMsg("s"))

	// Enter on session should emit NewSessionMsg (dir picked by old launcher)
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command from Enter in session launcher")
	}
	msg := cmd()
	sm, ok := msg.(NewSessionMsg)
	if !ok {
		t.Fatalf("expected NewSessionMsg, got %T", msg)
	}
	if sm.Where != "local" {
		t.Errorf("expected Where=local, got %s", sm.Where)
	}
	if sm.Dir != "" {
		t.Errorf("expected empty Dir (delegated to launcher), got %s", sm.Dir)
	}
}

func TestNewPickerView_ProviderCycling(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("s"))

	// Move to provider field
	v.Update(specialKeyMsg(tea.KeyTab))

	// Cycle right
	v.Update(keyMsg("l"))
	if v.sessionProvCursor != 1 {
		t.Errorf("expected provCursor 1, got %d", v.sessionProvCursor)
	}

	v.Update(keyMsg("l"))
	if v.sessionProvCursor != 2 {
		t.Errorf("expected provCursor 2, got %d", v.sessionProvCursor)
	}

	// Left back
	v.Update(keyMsg("h"))
	if v.sessionProvCursor != 1 {
		t.Errorf("expected provCursor 1 after left, got %d", v.sessionProvCursor)
	}
}

func TestNewPickerView_DoubleEscFromLevel2Cancels(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("s")) // to session

	// First Esc goes back to picker
	v.Update(specialKeyMsg(tea.KeyEsc))
	if v.Level() != npLevelPicker {
		t.Fatalf("expected picker level after first Esc")
	}

	// Second Esc emits cancel
	_, cmd := v.Update(specialKeyMsg(tea.KeyEsc))
	if cmd == nil {
		t.Fatal("expected cancel command from second Esc")
	}
	msg := cmd()
	if _, ok := msg.(NewPickerCancelMsg); !ok {
		t.Errorf("expected NewPickerCancelMsg, got %T", msg)
	}
}

// --- New tests for UX improvements ---

func TestNewPickerView_GreyedOutSessionProvider_EnterIsNoop(t *testing.T) {
	// Codex on Local+K8s is unsupported
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s")) // go to session

	// Move where to Local+K8s
	v.Update(keyMsg("l")) // where = Local+K8s

	// Move to provider field, select codex (index 1)
	v.Update(specialKeyMsg(tea.KeyTab))
	v.Update(keyMsg("l")) // codex

	// Enter should be a no-op (no command emitted)
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("expected nil command for unsupported codex+Local+K8s, got non-nil")
	}

	// Should have a status message
	if v.StatusMsg() == "" {
		t.Errorf("expected status message for unsupported combination")
	}
	if !strings.Contains(v.StatusMsg(), "codex") {
		t.Errorf("expected status message to mention codex, got: %s", v.StatusMsg())
	}
}

func TestNewPickerView_GreyedOutSessionProvider_RemotePod(t *testing.T) {
	// Gemini on Remote (pod) is unsupported
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s"))

	// Move where to Remote (pod)
	v.Update(keyMsg("l"))
	v.Update(keyMsg("l"))

	// Move to provider, select gemini (index 2)
	v.Update(specialKeyMsg(tea.KeyTab))
	v.Update(keyMsg("l"))
	v.Update(keyMsg("l"))

	// Enter should be a no-op
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("expected nil command for unsupported gemini+Remote, got non-nil")
	}
	if !strings.Contains(v.StatusMsg(), "gemini") {
		t.Errorf("expected status to mention gemini, got: %s", v.StatusMsg())
	}
}

func TestNewPickerView_SupportedSessionProvider_Emits(t *testing.T) {
	// Claude on Remote (pod) IS supported
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s"))

	// Move where to Remote (pod)
	v.Update(keyMsg("l"))
	v.Update(keyMsg("l"))

	// Claude is already selected (index 0)
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected command for supported claude+Remote")
	}
	msg := cmd()
	sm, ok := msg.(NewSessionMsg)
	if !ok {
		t.Fatalf("expected NewSessionMsg, got %T", msg)
	}
	if sm.Provider != "claude" || sm.Where != "remote" {
		t.Errorf("expected claude+remote, got %s+%s", sm.Provider, sm.Where)
	}
}

func TestNewPickerView_GreyedOutTaskProvider_EnterIsNoop(t *testing.T) {
	// Codex on Remote task is unsupported
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("t"))

	// Move where to Remote
	v.Update(keyMsg("l"))

	// Move to provider, select codex (index 1)
	v.Update(specialKeyMsg(tea.KeyTab))
	v.Update(keyMsg("l"))

	// Type a prompt so that's not what blocks us
	v.Update(specialKeyMsg(tea.KeyTab))
	v.Update(keyMsg("x"))

	// Enter should be a no-op
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("expected nil command for unsupported codex+Remote task, got non-nil")
	}
	if !strings.Contains(v.StatusMsg(), "codex") {
		t.Errorf("expected status to mention codex, got: %s", v.StatusMsg())
	}
}

func TestNewPickerView_TaskEmptyPrompt_ShowsStatus(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("t"))

	// Don't type any prompt, just hit enter
	_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("expected nil command for empty prompt task, got non-nil")
	}
	if v.StatusMsg() != "Enter a prompt to launch" {
		t.Errorf("expected 'Enter a prompt to launch', got: %s", v.StatusMsg())
	}
}

func TestNewPickerView_StatusClearsOnNavigation(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.Update(keyMsg("t"))

	// Trigger status message
	v.Update(specialKeyMsg(tea.KeyEnter))
	if v.StatusMsg() == "" {
		t.Fatal("precondition: expected status message")
	}

	// Navigate and status should clear
	v.Update(keyMsg("j"))
	if v.StatusMsg() != "" {
		t.Errorf("expected status to clear on navigation, got: %s", v.StatusMsg())
	}
}

func TestNewPickerView_DescriptionChangesWithWhereCursor(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.SetSize(100, 30)
	v.Update(keyMsg("s"))

	// Local description
	view1 := v.View()
	if !strings.Contains(view1, "Claude Code on your laptop with local agents") {
		t.Errorf("expected Local description in view, got:\n%s", view1)
	}

	// Move to Local+K8s
	v.Update(keyMsg("l"))
	view2 := v.View()
	if !strings.Contains(view2, "tasks run on K8s pods") {
		t.Errorf("expected Local+K8s description in view, got:\n%s", view2)
	}

	// Move to Remote (pod)
	v.Update(keyMsg("l"))
	view3 := v.View()
	if !strings.Contains(view3, "Full Claude Code runs in a K8s pod") {
		t.Errorf("expected Remote description in view, got:\n%s", view3)
	}
}

func TestNewPickerView_TaskDescriptionChangesWithWhereCursor(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.SetSize(100, 30)
	v.Update(keyMsg("t"))

	view1 := v.View()
	if !strings.Contains(view1, "Run task on your machine") {
		t.Errorf("expected Local task description in view, got:\n%s", view1)
	}

	v.Update(keyMsg("l"))
	view2 := v.View()
	if !strings.Contains(view2, "Run task on a K8s pod") {
		t.Errorf("expected Remote task description in view, got:\n%s", view2)
	}
}

func TestNewPickerView_PickerShowsDescriptions(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.SetSize(80, 24)

	view := v.View()
	if !strings.Contains(view, "you chat with it live") {
		t.Errorf("expected session description in picker view")
	}
	if !strings.Contains(view, "agent works, reports results") {
		t.Errorf("expected task description in picker view")
	}
}

func TestNewPickerView_GreyedOutProviderShowsComingSoon(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.SetSize(100, 30)
	v.Update(keyMsg("s"))

	// Move to Local+K8s where codex is unsupported
	v.Update(keyMsg("l"))

	view := v.View()
	if !strings.Contains(view, "coming soon") {
		t.Errorf("expected 'coming soon' for unsupported providers in view, got:\n%s", view)
	}
}

func TestNewPickerView_ProviderSupportFiltering(t *testing.T) {
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers: []ProviderSupport{
			{Name: "claude", LocalSession: true, LocalK8s: true, RemoteSession: true, RemoteTask: true},
			{Name: "codex", LocalSession: true, LocalK8s: false, RemoteSession: false, RemoteTask: false},
			{Name: "gemini", LocalSession: true, LocalK8s: false, RemoteSession: false, RemoteTask: true},
		},
	})

	// All providers support Local session
	for _, p := range []string{"claude", "codex", "gemini"} {
		if !v.isSessionProviderSupported(p, "Local") {
			t.Errorf("expected %s to support Local session", p)
		}
	}

	// Only claude supports Local+K8s
	if !v.isSessionProviderSupported("claude", "Local+K8s") {
		t.Error("expected claude to support Local+K8s")
	}
	if v.isSessionProviderSupported("codex", "Local+K8s") {
		t.Error("expected codex NOT to support Local+K8s")
	}
	if v.isSessionProviderSupported("gemini", "Local+K8s") {
		t.Error("expected gemini NOT to support Local+K8s")
	}

	// Only claude supports Remote (pod) session
	if !v.isSessionProviderSupported("claude", "Remote (pod)") {
		t.Error("expected claude to support Remote (pod)")
	}
	if v.isSessionProviderSupported("codex", "Remote (pod)") {
		t.Error("expected codex NOT to support Remote (pod)")
	}

	// Remote task: claude and gemini
	if !v.isTaskProviderSupported("claude", "Remote") {
		t.Error("expected claude to support Remote task")
	}
	if !v.isTaskProviderSupported("gemini", "Remote") {
		t.Error("expected gemini to support Remote task")
	}
	if v.isTaskProviderSupported("codex", "Remote") {
		t.Error("expected codex NOT to support Remote task")
	}
}

func TestNewPickerView_HintLineContent(t *testing.T) {
	v := NewNewPickerView(defaultTestConfig())
	v.SetSize(80, 24)

	// Level 1 picker hints
	view := v.View()
	if !strings.Contains(view, "navigate") && !strings.Contains(view, "select") && !strings.Contains(view, "close") {
		t.Errorf("expected navigation hints in picker view")
	}

	// Session hints
	v.Update(keyMsg("s"))
	view = v.View()
	if !strings.Contains(view, "field") || !strings.Contains(view, "option") || !strings.Contains(view, "launch") {
		t.Errorf("expected session hints in session view")
	}

	// Task hints
	v.Update(specialKeyMsg(tea.KeyEsc))
	v.Update(keyMsg("t"))
	view = v.View()
	if !strings.Contains(view, "field") || !strings.Contains(view, "option") || !strings.Contains(view, "launch") {
		t.Errorf("expected task hints in task view")
	}
}

func TestNewPickerView_DefaultProviderSupport(t *testing.T) {
	defaults := DefaultProviderSupport()
	if len(defaults) != 3 {
		t.Fatalf("expected 3 default providers, got %d", len(defaults))
	}

	// Verify claude
	claude := defaults[0]
	if claude.Name != "claude" || !claude.LocalSession || !claude.LocalK8s || !claude.RemoteSession || !claude.RemoteTask {
		t.Errorf("claude should support all modes, got %+v", claude)
	}

	// Verify codex
	codex := defaults[1]
	if codex.Name != "codex" || !codex.LocalSession || codex.LocalK8s || codex.RemoteSession || codex.RemoteTask {
		t.Errorf("codex should only support LocalSession, got %+v", codex)
	}

	// Verify gemini
	gemini := defaults[2]
	if gemini.Name != "gemini" || !gemini.LocalSession || gemini.LocalK8s || gemini.RemoteSession || !gemini.RemoteTask {
		t.Errorf("gemini should support LocalSession and RemoteTask, got %+v", gemini)
	}
}

func TestNewPickerView_AllLocalSessionsSupported(t *testing.T) {
	// Even with K8s enabled, all providers work for Local sessions
	v := NewNewPickerView(NewPickerConfig{
		K8sEnabled: true, Health: healthyK8s(),
		Providers:  DefaultProviderSupport(),
	})
	v.Update(keyMsg("s"))

	// Try each provider with Local where
	for i, name := range v.sessionProviders {
		v.sessionProvCursor = i
		v.sessionWhereCursor = 0 // Local
		_, cmd := v.Update(specialKeyMsg(tea.KeyEnter))
		if cmd == nil {
			t.Errorf("expected %s+Local to emit, but got nil cmd", name)
		}
	}
}
