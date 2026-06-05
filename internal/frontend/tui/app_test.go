package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
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
	_ = os.WriteFile(path, []byte(data), 0o600)

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
	_ = os.WriteFile(path, []byte(data), 0o600)

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
	_ = os.WriteFile(path, []byte(data), 0o600)

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

// TestNewCommand_OpensLauncher verifies that :new opens the Launcher directly.
func TestNewCommand_OpensLauncher(t *testing.T) {
	app := App{
		currentView: viewAgents,
		providers:   []provider.Provider{&provider.Claude{}},
		cfg:         config.Config{},
	}

	result, _ := app.openLauncher()
	a := result.(App)
	if !a.launcherActive {
		t.Error("expected launcherActive = true after openLauncher")
	}
	if a.launcherView == nil {
		t.Error("expected launcherView != nil after openLauncher")
	}
}

// TestLaunchCancel_ClearsState verifies that LaunchCancelMsg clears the launcher overlay.
func TestLaunchCancel_ClearsState(t *testing.T) {
	app := App{
		launcherActive: true,
		launcherView:   &views.LauncherView{},
		agentsView:     views.NewAgentsView(),
		otelStore:      aimuxotel.NewSpanStore(),
	}

	result, _ := app.Update(views.LaunchCancelMsg{})
	a := result.(App)
	if a.launcherActive {
		t.Error("expected launcherActive = false after LaunchCancelMsg")
	}
	if a.launcherView != nil {
		t.Error("expected launcherView = nil after LaunchCancelMsg")
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

// TestStartTraceTailer_SignalsChannel verifies that startTraceTailer
// creates a tailer that sends a signal on the channel when the file changes.
func TestStartTraceTailer_SignalsChannel(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	// Create the file so the tailer can stat it.
	_ = os.WriteFile(path, []byte(`{"type":"user"}"`+"\n"), 0o600)

	ch := make(chan struct{}, 1)
	tailer := startTraceTailer(path, ch)
	if tailer == nil {
		t.Fatal("startTraceTailer returned nil for valid file")
	}
	defer tailer.Stop()

	// Append a line to trigger the tailer.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304
	_, _ = f.WriteString(`{"type":"assistant"}` + "\n")
	_ = f.Close()

	// Wait for the channel signal (with timeout).
	select {
	case <-ch:
		// Success: tailer signaled the channel.
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tailer to signal channel")
	}
}

// TestStartTraceTailer_NonBlockingChannel verifies that the tailer
// doesn't block when the channel is already full.
func TestStartTraceTailer_NonBlockingChannel(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"user"}"`+"\n"), 0o600)

	ch := make(chan struct{}, 1)
	// Pre-fill the channel.
	ch <- struct{}{}

	tailer := startTraceTailer(path, ch)
	if tailer == nil {
		t.Fatal("startTraceTailer returned nil for valid file")
	}
	defer tailer.Stop()

	// Append to trigger the tailer. It should NOT block even though channel is full.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304
	_, _ = f.WriteString(`{"type":"assistant"}` + "\n")
	_ = f.Close()

	// The test passes if it doesn't deadlock/timeout.
	// Drain the pre-existing signal.
	<-ch
}

// TestStartTraceTailer_InvalidFile verifies that startTraceTailer
// returns nil for a non-existent file.
func TestStartTraceTailer_InvalidFile(t *testing.T) {
	ch := make(chan struct{}, 1)
	tailer := startTraceTailer("/nonexistent/path.jsonl", ch)
	if tailer != nil {
		tailer.Stop()
		t.Fatal("expected nil tailer for non-existent file")
	}
}

// TestStopActiveTailer_CleansUp verifies that stopActiveTailer stops the
// tailer and drains the channel.
func TestStopActiveTailer_CleansUp(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"user"}"`+"\n"), 0o600)

	app := &App{
		traceRefresh: make(chan struct{}, 1),
	}
	app.activeTailer = startTraceTailer(path, app.traceRefresh)
	if app.activeTailer == nil {
		t.Fatal("startTraceTailer returned nil")
	}

	// Put a signal in the channel to verify it gets drained.
	app.traceRefresh <- struct{}{}

	app.stopActiveTailer()

	if app.activeTailer != nil {
		t.Error("activeTailer should be nil after stop")
	}
	// Channel should be drained (non-blocking receive should fail).
	select {
	case <-app.traceRefresh:
		t.Error("channel should be drained after stop")
	default:
		// OK: channel is empty.
	}
}

// TestStopActiveTailer_NilTailer verifies that stopActiveTailer is safe
// to call when there is no active tailer.
func TestStopActiveTailer_NilTailer(t *testing.T) {
	app := &App{
		traceRefresh: make(chan struct{}, 1),
	}
	// Should not panic.
	app.stopActiveTailer()
}

// TestTraceRefreshMsg_ReloadsTrace verifies that traceRefreshMsg triggers
// a Reload on the split trace and re-arms the channel listener.
func TestTraceRefreshMsg_ReloadsTrace(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	_ = os.WriteFile(path, []byte(data), 0o600)

	p := &provider.Claude{}
	splitTrace := views.NewLogsView(0, path, p.ParseTrace)
	if len(splitTrace.Turns()) != 1 {
		t.Fatalf("expected 1 turn initially, got %d", len(splitTrace.Turns()))
	}

	// Append a second turn to the file.
	appendData := "\n" + `{"type":"user","timestamp":"2026-01-01T10:01:00Z","message":{"role":"user","content":"second"}}
{"type":"assistant","timestamp":"2026-01-01T10:01:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"response two"}],"usage":{"input_tokens":200,"output_tokens":100}}}`
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304
	_, _ = f.WriteString(appendData)
	_ = f.Close()

	app := App{
		splitMode:    true,
		splitTrace:   splitTrace,
		activeTailer: &trace.Tailer{}, // non-nil to indicate active
		traceRefresh: make(chan struct{}, 1),
		otelStore:    aimuxotel.NewSpanStore(),
		agentsView:   views.NewAgentsView(),
	}

	result, cmd := app.Update(traceRefreshMsg{})
	a := result.(App)

	// Verify the trace was reloaded with the new turn.
	if len(a.splitTrace.Turns()) != 2 {
		t.Errorf("expected 2 turns after reload, got %d", len(a.splitTrace.Turns()))
	}
	// Verify a cmd was returned to re-arm the listener.
	if cmd == nil {
		t.Error("expected non-nil cmd to re-arm channel listener")
	}
}

// TestTraceRefreshMsg_NoTailer verifies that traceRefreshMsg without an
// active tailer does not return a re-arm command.
func TestTraceRefreshMsg_NoTailer(t *testing.T) {
	app := App{
		splitMode:    true,
		splitTrace:   views.NewLogsView(0, "", nil),
		activeTailer: nil, // no tailer active
		traceRefresh: make(chan struct{}, 1),
		otelStore:    aimuxotel.NewSpanStore(),
		agentsView:   views.NewAgentsView(),
	}

	_, cmd := app.Update(traceRefreshMsg{})
	if cmd != nil {
		t.Error("expected nil cmd when no tailer is active")
	}
}

// TestProjectConfigMerge verifies that LoadProject correctly merges
// project-local badge rules over global config, which is the mechanism
// used by NewApp() and createWebServer() at startup.
func TestProjectConfigMerge(t *testing.T) {
	// Create a temp project directory with .aimux/config.yaml
	projDir := t.TempDir()
	aimuxDir := filepath.Join(projDir, ".aimux")
	_ = os.MkdirAll(aimuxDir, 0o750)
	_ = os.WriteFile(filepath.Join(aimuxDir, "config.yaml"), []byte(`
badges:
  - path: "package.json"
    json_path: "version"
    label: "ver"
    color: "#00ff00"
`), 0o600)

	global := config.Default()
	if len(global.Badges) != 0 {
		t.Fatal("default config should have no badges")
	}

	merged, err := config.LoadProject(projDir, global)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if len(merged.Badges) != 1 {
		t.Fatalf("expected 1 badge rule after merge, got %d", len(merged.Badges))
	}
	if merged.Badges[0].Label != "ver" {
		t.Errorf("badge label = %q, want %q", merged.Badges[0].Label, "ver")
	}
	if merged.Badges[0].JSONPath != "version" {
		t.Errorf("badge json_path = %q, want %q", merged.Badges[0].JSONPath, "version")
	}
	if merged.Badges[0].Color != "#00ff00" {
		t.Errorf("badge color = %q, want %q", merged.Badges[0].Color, "#00ff00")
	}
	// Global providers should be preserved
	if !merged.IsProviderEnabled("claude") {
		t.Error("project config should not wipe global claude provider")
	}
}

// TestProjectConfigMerge_NoDirReturnsGlobal verifies that LoadProject
// returns the global config unchanged when no .aimux/ directory exists.
func TestProjectConfigMerge_NoDirReturnsGlobal(t *testing.T) {
	global := config.Default()
	global.Shell = "/bin/zsh"

	merged, err := config.LoadProject("/nonexistent/path", global)
	if err != nil {
		t.Fatalf("LoadProject should not error: %v", err)
	}
	if merged.Shell != "/bin/zsh" {
		t.Errorf("shell = %q, want /bin/zsh", merged.Shell)
	}
}

// newTestAppForPending creates a minimal App with all views initialized so that
// Update(instancesMsg{...}) can run without nil pointer panics.
func newTestAppForPending(pending map[string]agent.Agent) App {
	app := App{
		pendingAgents:  pending,
		instances:      []agent.Agent{},
		hiddenAgents:   make(map[string]bool),
		prevStatuses:   make(map[int]agent.Status),
		doneTimestamps: make(map[int]time.Time),
		staleAgents:    make(map[int]bool),
		otelStore:      aimuxotel.NewSpanStore(),
		agentsView:     views.NewAgentsView(),
		headerView:     views.NewHeaderView(),
		costsView:      views.NewCostsView(),
		previewPane:    views.NewPreviewPane(),
		sessionsView:   views.NewSessionsView(),
		tasksView:      views.NewTasksView(),
		teamsView:      views.NewTeamsView(),
		healthView:     views.NewHealthView(),
		helpView:       views.NewHelpView(),
		cfg:            config.Default(),
		traceRefresh:   make(chan struct{}, 1),
	}
	app.ctrl = newTestController()
	return app
}

// TestPendingAgents_SurvivesDiscoveryTick verifies that pending agents
// are appended to instances when discovery returns no matching tmux session.
func TestPendingAgents_SurvivesDiscoveryTick(t *testing.T) {
	app := newTestAppForPending(map[string]agent.Agent{
		"aimux-claude-newproject": {
			Name:         "newproject",
			ProviderName: "claude",
			TMuxSession:  "aimux-claude-newproject",
			WorkingDir:   "/tmp/newproject",
			Status:       agent.StatusActive,
		},
	})

	// Simulate instancesMsg with a different discovered agent (no tmux session match).
	discovered := instancesMsg{
		{PID: 1, Name: "other", ProviderName: "claude", WorkingDir: "/tmp/other", TMuxSession: "aimux-claude-other"},
	}

	result, _ := app.Update(discovered)
	a := result.(App)

	// Pending agent should be appended since no discovered agent had TMuxSession "aimux-claude-newproject".
	found := false
	for _, inst := range a.instances {
		if inst.Name == "newproject" && inst.TMuxSession == "aimux-claude-newproject" {
			found = true
			break
		}
	}
	if !found {
		t.Error("pending agent 'newproject' should survive when no discovery match")
	}
	// Should still have 2 total: 1 discovered + 1 pending.
	if len(a.instances) != 2 {
		t.Errorf("expected 2 instances (1 discovered + 1 pending), got %d", len(a.instances))
	}
}

// TestPendingAgents_RemovedWhenDiscovered verifies that a pending agent is
// removed once discovery finds an agent with the same TMuxSession.
func TestPendingAgents_RemovedWhenDiscovered(t *testing.T) {
	app := newTestAppForPending(map[string]agent.Agent{
		"aimux-claude-myproject": {
			Name:         "myproject",
			ProviderName: "claude",
			TMuxSession:  "aimux-claude-myproject",
			WorkingDir:   "/tmp/myproject",
			Status:       agent.StatusActive,
		},
	})

	// Discovery finds an agent with the SAME TMuxSession.
	discovered := instancesMsg{
		{PID: 99, Name: "myproject", ProviderName: "claude", WorkingDir: "/tmp/myproject", TMuxSession: "aimux-claude-myproject"},
	}

	result, _ := app.Update(discovered)
	a := result.(App)

	// Pending should have been removed from the map.
	if len(a.pendingAgents) != 0 {
		t.Errorf("expected 0 pending agents after discovery match, got %d", len(a.pendingAgents))
	}
	// Only 1 instance (the discovered one).
	if len(a.instances) != 1 {
		t.Errorf("expected 1 instance (discovered only), got %d", len(a.instances))
	}
}

// TestPendingAgents_NotRemovedByWorkingDirMatch is a regression test verifying
// that pending agents are NOT removed when a discovered agent merely shares the
// same WorkingDir and provider. Only TMuxSession matching should trigger removal.
// Bug: previously WorkingDir+Provider matching would falsely remove pending agents
// when an old idle session in the same directory was discovered.
func TestPendingAgents_NotRemovedByWorkingDirMatch(t *testing.T) {
	app := newTestAppForPending(map[string]agent.Agent{
		"aimux-claude-myproject": {
			Name:         "myproject",
			ProviderName: "claude",
			TMuxSession:  "aimux-claude-myproject",
			WorkingDir:   "/tmp/myproject",
			Status:       agent.StatusActive,
		},
	})

	// Discovery finds an old session with same WorkingDir but DIFFERENT TMuxSession.
	discovered := instancesMsg{
		{PID: 50, Name: "myproject", ProviderName: "claude", WorkingDir: "/tmp/myproject", TMuxSession: "claude-myproject"},
	}

	result, _ := app.Update(discovered)
	a := result.(App)

	// Pending agent must NOT be removed (TMuxSession doesn't match).
	if len(a.pendingAgents) != 1 {
		t.Errorf("expected 1 pending agent (not removed by WorkingDir-only match), got %d", len(a.pendingAgents))
	}
	// Should have 2 instances: 1 discovered + 1 pending appended.
	if len(a.instances) != 2 {
		t.Errorf("expected 2 instances, got %d", len(a.instances))
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

func TestStarFromTrace(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")
	_ = os.WriteFile(sessionFile, []byte(`{"type":"user"}`), 0o600)

	app := App{
		agentsView: views.NewAgentsView(),
	}

	result, _ := app.starFromTrace(sessionFile)
	a := result.(App)
	if a.statusHint != "Session pinned ★" {
		t.Errorf("expected pinned hint, got %q", a.statusHint)
	}

	result, _ = a.starFromTrace(sessionFile)
	a = result.(App)
	if a.statusHint != "Session unpinned" {
		t.Errorf("expected unpinned hint, got %q", a.statusHint)
	}
}

func TestStarFromTrace_EmptyPath(t *testing.T) {
	app := App{}
	result, _ := app.starFromTrace("")
	a := result.(App)
	if a.statusHint != "No session file available" {
		t.Errorf("expected error hint, got %q", a.statusHint)
	}
}

func TestAgentForLogsView(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")
	_ = os.WriteFile(sessionFile, []byte(`{"type":"user"}`), 0o600)

	dummyParser := func(string) ([]trace.Turn, error) { return nil, nil }
	lv := views.NewLogsView(42, sessionFile, dummyParser)

	app := &App{
		logsView: lv,
		instances: []agent.Agent{
			{PID: 99, SessionFile: "/other/file.jsonl", SessionID: "other-id"},
			{PID: 42, SessionFile: sessionFile, SessionID: "matched-id", WorkingDir: "/project"},
		},
	}

	ag := app.agentForLogsView()
	if ag == nil {
		t.Fatal("expected agent, got nil")
	}
	if ag.SessionID != "matched-id" {
		t.Errorf("expected matched-id, got %q", ag.SessionID)
	}
}

func TestAgentForLogsView_NoMatch(t *testing.T) {
	dummyParser := func(string) ([]trace.Turn, error) { return nil, nil }
	lv := views.NewLogsView(42, "/nonexistent.jsonl", dummyParser)

	app := &App{
		logsView:  lv,
		instances: []agent.Agent{{PID: 99, SessionFile: "/other/file.jsonl"}},
	}

	ag := app.agentForLogsView()
	if ag != nil {
		t.Errorf("expected nil, got agent with PID %d", ag.PID)
	}
}

func TestAgentForLogsView_NilLogsView(t *testing.T) {
	app := &App{}
	ag := app.agentForLogsView()
	if ag != nil {
		t.Errorf("expected nil, got agent")
	}
}
