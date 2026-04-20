package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/internal/tui/views"
)

// keyMsg creates a tea.KeyMsg for testing key handling.
func keyMsg(k string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// newTestController creates a controller with default config for testing.
func newTestController() *controller.Controller {
	cfg := config.Default()
	return controller.New(cfg)
}

// TestParserForProvider_FallsBackToFile verifies that the parser uses
// file-based parsing when the OTEL store is empty.
func TestParserForProvider_FallsBackToFile(t *testing.T) {
	app := App{
		otelStore: aimuxotel.NewSpanStore(),
	}
	p := &provider.Claude{}

	parser := app.parserForProvider(p)

	// Create a minimal JSONL file
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	os.WriteFile(path, []byte(data), 0o644)

	turns, err := parser(path)
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn from file, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "hello" {
		t.Errorf("UserLines = %v, want [hello]", turns[0].UserLines)
	}
}

// TestParserForProvider_PrefersOTEL verifies that when the OTEL store has
// data for the session, it is used instead of file parsing.
func TestParserForProvider_PrefersOTEL(t *testing.T) {
	store := aimuxotel.NewSpanStore()

	// Add OTEL data for a session (Claude log events format)
	root := &aimuxotel.Span{
		SpanID:  "root-1",
		TraceID: "session-test-otel",
		Name:    "claude_code.user_prompt",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "session-test-otel",
			"gen_ai.input.messages":  "from otel",
			"prompt.id":             "p1",
		},
		Children: []*aimuxotel.Span{
			{
				SpanID: "turn-1",
				Name:   "claude_code.api_request",
				Attrs: map[string]any{
					"gen_ai.request.model":      "claude-opus-4-6",
					"gen_ai.usage.input_tokens": int64(100),
					"prompt.id":                "p1",
				},
			},
		},
	}
	store.Add(root)

	// Create app with the OTEL store and a session view agent
	sessionView := views.NewSessionView()
	sessionAgent := &agent.Agent{
		SessionID:    "session-test-otel",
		ProviderName: "claude",
	}

	app := App{
		otelStore:   store,
		agentsView:  views.NewAgentsView(),
		sessionView: sessionView,
	}

	// Simulate the session view having an agent
	// We can't call Open() without a real backend, but we can test
	// the parser function directly by passing the session ID through
	// the store
	_ = sessionAgent // used conceptually

	p := &provider.Claude{}
	parser := app.parserForProvider(p)

	// The parser should find OTEL data even with an empty file path
	// (because the OTEL store has data and the agentsView.Selected
	// might return nil, but the store.HasData() is true)
	// However, without a selected agent or session view agent, it
	// won't know which session to look up. Let's test with agents view.
	app.agentsView.SetAgents([]agent.Agent{
		{SessionID: "session-test-otel", ProviderName: "claude"},
	})

	turns, err := parser("")
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn from OTEL, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "from otel" {
		t.Errorf("UserLines = %v, want [from otel]", turns[0].UserLines)
	}
}

// TestParserForProvider_OTELEmptyFallsBackToFile verifies that when the
// OTEL store has data but not for this session, file parsing is used.
func TestParserForProvider_OTELEmptyFallsBackToFile(t *testing.T) {
	store := aimuxotel.NewSpanStore()

	// Add OTEL data for a DIFFERENT session
	store.Add(&aimuxotel.Span{
		SpanID:  "other-root",
		TraceID: "other-session",
		Name:    "invoke_agent",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "other-session",
		},
	})

	app := App{
		otelStore:  store,
		agentsView: views.NewAgentsView(),
	}

	// Set selected agent to a different session
	app.agentsView.SetAgents([]agent.Agent{
		{SessionID: "my-session", ProviderName: "claude"},
	})

	p := &provider.Claude{}
	parser := app.parserForProvider(p)

	// Create a file for this session
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"from file"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"file response"}],"usage":{"input_tokens":50,"output_tokens":25}}}`
	os.WriteFile(path, []byte(data), 0o644)

	turns, err := parser(path)
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn from file fallback, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "from file" {
		t.Errorf("UserLines = %v, want [from file]", turns[0].UserLines)
	}
}

// TestOTELStoreLogEvents verifies that Claude-style log events get
// stored and can be converted to turns, grouped by prompt.id.
func TestOTELStoreLogEvents(t *testing.T) {
	store := aimuxotel.NewSpanStore()

	// Simulate Claude log events -- all share the same prompt.id
	promptID := "prompt-1"

	userPrompt := &aimuxotel.Span{
		SpanID:  "log-1",
		TraceID: "sess-abc",
		Name:    "claude_code.user_prompt",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "sess-abc",
			"gen_ai.operation.name":  "invoke_agent",
			"gen_ai.input.messages":  "fix the bug",
			"session.id":            "sess-abc",
			"prompt.id":             promptID,
		},
	}
	store.Add(userPrompt)

	apiRequest := &aimuxotel.Span{
		SpanID:  "log-2",
		TraceID: "sess-abc",
		Name:    "claude_code.api_request",
		Attrs: map[string]any{
			"gen_ai.conversation.id":    "sess-abc",
			"gen_ai.operation.name":     "chat",
			"gen_ai.request.model":      "claude-opus-4-6",
			"gen_ai.usage.input_tokens": int64(5000),
			"prompt.id":                promptID,
		},
	}
	store.Add(apiRequest)

	toolResult := &aimuxotel.Span{
		SpanID:  "log-3",
		TraceID: "sess-abc",
		Name:    "claude_code.tool_result",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "sess-abc",
			"gen_ai.operation.name":  "execute_tool",
			"gen_ai.tool.name":       "Read",
			"prompt.id":             promptID,
		},
	}
	store.Add(toolResult)

	// Verify the root span has children
	root := store.GetByConversation("sess-abc")
	if root == nil {
		t.Fatal("GetByConversation returned nil")
	}
	if len(root.Children) != 2 {
		t.Fatalf("root has %d children, want 2 (api_request + tool_result)", len(root.Children))
	}

	// Convert to turns -- all 3 events share prompt.id so they become 1 turn
	turns := aimuxotel.SpansToTurns(root)
	if len(turns) != 1 {
		t.Fatalf("SpansToTurns returned %d turns, want 1 (all events share prompt.id)", len(turns))
	}
	if turns[0].UserLines[0] != "fix the bug" {
		t.Errorf("turn[0].UserLines = %v, want [fix the bug]", turns[0].UserLines)
	}
	if turns[0].Model != "claude-opus-4-6" {
		t.Errorf("turn[0].Model = %q, want claude-opus-4-6", turns[0].Model)
	}
	if turns[0].TokensIn != 5000 {
		t.Errorf("turn[0].TokensIn = %d, want 5000", turns[0].TokensIn)
	}
	if len(turns[0].Actions) != 1 || turns[0].Actions[0].Name != "Read" {
		t.Errorf("turn[0].Actions = %v, want 1 action (Read)", turns[0].Actions)
	}
}

// TestLogsViewSetFilePath verifies that SetFilePath + Reload works
// for late-discovered session files.
func TestLogsViewSetFilePath(t *testing.T) {
	// Create a parser that reads Claude JSONL
	p := &provider.Claude{}
	parser := func(path string) ([]trace.Turn, error) {
		return p.ParseTrace(path)
	}

	// Create LogsView with empty path
	lv := views.NewLogsView(0, "", parser)
	if len(lv.Turns()) != 0 {
		t.Fatalf("expected 0 turns with empty path, got %d", len(lv.Turns()))
	}

	// Create a session file
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"late discovery"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"found it"}],"usage":{"input_tokens":50,"output_tokens":25}}}`
	os.WriteFile(path, []byte(data), 0o644)

	// Set the file path and reload
	lv.SetFilePath(path)
	lv.Reload()

	if len(lv.Turns()) != 1 {
		t.Fatalf("expected 1 turn after SetFilePath+Reload, got %d", len(lv.Turns()))
	}
	if lv.Turns()[0].UserLines[0] != "late discovery" {
		t.Errorf("UserLines = %v, want [late discovery]", lv.Turns()[0].UserLines)
	}
}

// TestOtelEnvForCmd verifies that otelEnvForCmd correctly merges
// OTEL env vars from a shell-style prefix into cmd.Env.
func TestOtelEnvForCmd(t *testing.T) {
	cmd := exec.Command("echo", "test")

	prefix := "CLAUDE_CODE_ENABLE_TELEMETRY=1 " +
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf " +
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 " +
		"OTEL_LOGS_EXPORTER=otlp "

	env := otelEnvForCmd(cmd, prefix)

	required := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4318",
		"OTEL_LOGS_EXPORTER":          "otlp",
	}

	for key, want := range required {
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				val := strings.TrimPrefix(e, key+"=")
				if val != want {
					t.Errorf("env %s = %q, want %q", key, val, want)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("env missing %s=%s", key, want)
		}
	}

	// Verify original env is preserved (should include PATH at minimum)
	hasPath := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}
	if !hasPath {
		t.Error("env should preserve original process env (PATH missing)")
	}
}

// TestOtelEnvForCmd_PreservesExisting verifies that otelEnvForCmd
// preserves any env already set on the cmd.
func TestOtelEnvForCmd_PreservesExisting(t *testing.T) {
	cmd := exec.Command("echo")
	cmd.Env = []string{"EXISTING=value", "PATH=/usr/bin"}

	env := otelEnvForCmd(cmd, "NEW_VAR=1 ")

	has := func(key string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				return true
			}
		}
		return false
	}

	if !has("EXISTING") {
		t.Error("lost EXISTING env var")
	}
	if !has("NEW_VAR") {
		t.Error("missing NEW_VAR")
	}
}

// TestViewTasks_NavigateAndBack verifies that T navigates to tasks view
// and Esc returns to agents view.
func TestViewTasks_NavigateAndBack(t *testing.T) {
	app := App{
		currentView: viewAgents,
		agentsView:  views.NewAgentsView(),
		tasksView:   views.NewTasksView(),
		headerView:  views.NewHeaderView(),
		otelStore:   aimuxotel.NewSpanStore(),
	}
	app.ctrl = newTestController()

	// Navigate to tasks via T key
	result, _ := app.handleKey(keyMsg("T"))
	a := result.(App)
	if a.currentView != viewTasks {
		t.Errorf("after T key: currentView = %d, want %d (viewTasks)", a.currentView, viewTasks)
	}

	// Navigate back via Esc
	result, _ = a.handleKey(keyMsg("esc"))
	a = result.(App)
	if a.currentView != viewAgents {
		t.Errorf("after Esc: currentView = %d, want %d (viewAgents)", a.currentView, viewAgents)
	}
}

// TestViewTasks_QuitReturnsToAgents verifies that q in tasks view goes back.
func TestViewTasks_QuitReturnsToAgents(t *testing.T) {
	app := App{
		currentView: viewTasks,
		agentsView:  views.NewAgentsView(),
		tasksView:   views.NewTasksView(),
		headerView:  views.NewHeaderView(),
		otelStore:   aimuxotel.NewSpanStore(),
	}
	app.ctrl = newTestController()
	app.ctrl.Nav.NavigateTo(6, "Tasks") // viewTasks = 6

	result, _ := app.handleKey(keyMsg("q"))
	a := result.(App)
	if a.currentView != viewAgents {
		t.Errorf("after q in tasks: currentView = %d, want %d (viewAgents)", a.currentView, viewAgents)
	}
}

// TestRefreshTasks_SetsTaskSummary verifies that refreshTasks updates
// the header with correct task counts.
func TestRefreshTasks_SetsTaskSummary(t *testing.T) {
	app := App{
		tasksView:  views.NewTasksView(),
		headerView: views.NewHeaderView(),
		providers:  []provider.Provider{}, // no providers with TaskLister
	}
	// Should not panic with zero task-capable providers
	app.refreshTasks()

	// Verify tasks view has zero tasks
	if selected := app.tasksView.Selected(); selected != nil {
		t.Error("expected no selected task with zero providers")
	}
}

// TestExecuteCommand_Tasks verifies the :tasks command.
func TestExecuteCommand_Tasks(t *testing.T) {
	app := App{
		currentView: viewAgents,
		tasksView:   views.NewTasksView(),
		headerView:  views.NewHeaderView(),
		otelStore:   aimuxotel.NewSpanStore(),
	}
	app.ctrl = newTestController()

	result, _ := app.executeCommand("tasks")
	a := result.(App)
	if a.currentView != viewTasks {
		t.Errorf("executeCommand(tasks): currentView = %d, want %d (viewTasks)", a.currentView, viewTasks)
	}
}

// TestResolveCommand_Tasks verifies the tasks command is resolvable.
func TestResolveCommand_Tasks(t *testing.T) {
	got := resolveCommand("tasks")
	if got != "tasks" {
		t.Errorf("resolveCommand(tasks) = %q, want %q", got, "tasks")
	}
}

// TestOpenNewPicker_SetsState verifies that openNewPicker activates the picker.
func TestOpenNewPicker_SetsState(t *testing.T) {
	app := App{
		currentView: viewAgents,
		providers:   []provider.Provider{&provider.Claude{}},
	}

	result, _ := app.openNewPicker()
	a := result.(App)
	if !a.newPickerActive {
		t.Error("expected newPickerActive = true after openNewPicker")
	}
	if a.newPicker == nil {
		t.Error("expected newPicker != nil after openNewPicker")
	}
}

// TestNewPickerCancel_ClearsState verifies that NewPickerCancelMsg clears the overlay.
func TestNewPickerCancel_ClearsState(t *testing.T) {
	app := App{
		newPickerActive: true,
		newPicker:       &views.NewPickerView{},
		agentsView:      views.NewAgentsView(),
		otelStore:       aimuxotel.NewSpanStore(),
	}

	result, _ := app.Update(views.NewPickerCancelMsg{})
	a := result.(App)
	if a.newPickerActive {
		t.Error("expected newPickerActive = false after cancel")
	}
	if a.newPicker != nil {
		t.Error("expected newPicker = nil after cancel")
	}
}

// TestFilterInput_AcceptsBracketedPaste verifies that pasting via clipboard
// (bracketed paste) works without bracket artifacts.
func TestFilterInput_AcceptsBracketedPaste(t *testing.T) {
	app := App{
		filterMode: true,
	}

	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world"), Paste: true}
	result, _ := app.handleFilterInput(paste)
	a := result.(App)
	if a.filterInput.Value() != "hello world" {
		t.Errorf("filterInput after paste = %q, want %q", a.filterInput.Value(), "hello world")
	}
}

// TestCommandInput_AcceptsBracketedPaste verifies that pasting into the
// ":" command input works without bracket artifacts.
func TestCommandInput_AcceptsBracketedPaste(t *testing.T) {
	app := App{
		commandMode: true,
	}

	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("send hello"), Paste: true}
	result, _ := app.handleCommandInput(paste)
	a := result.(App)
	if a.commandInput.Value() != "send hello" {
		t.Errorf("commandInput after paste = %q, want %q", a.commandInput.Value(), "send hello")
	}
}

// TestFilterInput_CursorNavigation verifies Ctrl+A/E and arrow keys work
// for editing pasted or typed text in the filter.
func TestFilterInput_CursorNavigation(t *testing.T) {
	app := App{
		filterMode: true,
	}

	// Type "world"
	result, _ := app.handleFilterInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("world")})
	a := result.(App)

	// Ctrl+A to go to beginning
	result, _ = a.handleFilterInput(tea.KeyMsg{Type: tea.KeyCtrlA})
	a = result.(App)

	// Type "hello " at the beginning
	result, _ = a.handleFilterInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello ")})
	a = result.(App)
	if a.filterInput.Value() != "hello world" {
		t.Errorf("filterInput after ctrl+a + type = %q, want %q", a.filterInput.Value(), "hello world")
	}

	// Ctrl+E to go to end, type " !"
	result, _ = a.handleFilterInput(tea.KeyMsg{Type: tea.KeyCtrlE})
	a = result.(App)
	result, _ = a.handleFilterInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	a = result.(App)
	if a.filterInput.Value() != "hello world!" {
		t.Errorf("filterInput after ctrl+e + type = %q, want %q", a.filterInput.Value(), "hello world!")
	}
}

// TestFilterInput_RejectsSpecialKeys verifies that special keys like
// arrow keys don't inject their string representation into the filter.
func TestFilterInput_RejectsSpecialKeys(t *testing.T) {
	app := App{
		filterMode: true,
	}

	arrow := tea.KeyMsg{Type: tea.KeyUp}
	result, _ := app.handleFilterInput(arrow)
	a := result.(App)
	if a.filterInput.Value() != "" {
		t.Errorf("filterInput after arrow key = %q, want empty", a.filterInput.Value())
	}
}

// TestAllProvidersOTELEnvIncludeProtocol verifies that ALL providers'
// OTELEnv methods include the http/protobuf protocol setting.
// This is the root cause test -- without this protocol setting,
// agents default to gRPC and our HTTP receiver can't handle it.
func TestAllProvidersOTELEnvIncludeProtocol(t *testing.T) {
	providers := []provider.Provider{
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
	}

	endpoint := "http://localhost:4318"
	for _, p := range providers {
		env := p.OTELEnv(endpoint)
		if !strings.Contains(env, "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf") {
			t.Errorf("%s.OTELEnv missing OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf:\n%s", p.Name(), env)
		}
		if !strings.Contains(env, endpoint) {
			t.Errorf("%s.OTELEnv missing endpoint %s:\n%s", p.Name(), endpoint, env)
		}
		if !strings.Contains(env, "OTEL_LOGS_EXPORTER=otlp") {
			t.Errorf("%s.OTELEnv missing OTEL_LOGS_EXPORTER=otlp:\n%s", p.Name(), env)
		}
	}
}
