package views

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/session"
)

func testEvents() []session.SessionEvent {
	base := time.Date(2026, 9, 7, 14, 32, 0, 0, time.UTC)
	return []session.SessionEvent{
		{ID: 1, Type: "text", Timestamp: base, Data: `{"text":"I'll analyze the code..."}`},
		{ID: 2, Type: "tool_call", Timestamp: base.Add(2 * time.Second), Data: `{"id":"tc_1","name":"Read","input":{"file_path":"main.go"}}`},
		{ID: 3, Type: "tool_result", Timestamp: base.Add(3 * time.Second), Data: `{"id":"tc_1","output":"package main\nfunc main() {}\n","is_error":false}`},
		{ID: 4, Type: "tool_call", Timestamp: base.Add(5 * time.Second), Data: `{"id":"tc_2","name":"Bash","input":{"command":"go test ./..."}}`},
		{ID: 5, Type: "tool_result", Timestamp: base.Add(7 * time.Second), Data: `{"id":"tc_2","output":"PASS\nok  myapp 0.3s","is_error":false}`},
		{ID: 6, Type: "text", Timestamp: base.Add(8 * time.Second), Data: `{"text":"All tests pass. The code looks correct."}`},
	}
}

func TestToolStreamView_EmptyEvents(t *testing.T) {
	v := NewToolStreamView(nil, 80, 24)
	output := v.View()
	if !strings.Contains(output, "No events yet") {
		t.Errorf("empty view should show 'No events yet', got:\n%s", output)
	}
	if !strings.Contains(output, "0 events") {
		t.Errorf("empty view should show '0 events', got:\n%s", output)
	}
}

func TestToolStreamView_MixedEvents(t *testing.T) {
	events := testEvents()
	v := NewToolStreamView(events, 120, 40)
	output := v.View()

	if !strings.Contains(output, "6 events") {
		t.Errorf("should show '6 events' count, got:\n%s", output)
	}
	if !strings.Contains(output, "14:32:00") {
		t.Errorf("should show timestamp 14:32:00, got:\n%s", output)
	}
	if !strings.Contains(output, "Read") {
		t.Errorf("should show tool name 'Read', got:\n%s", output)
	}
	if !strings.Contains(output, "Bash") {
		t.Errorf("should show tool name 'Bash', got:\n%s", output)
	}
	if !strings.Contains(output, "main.go") {
		t.Errorf("should show file path 'main.go' in Read summary, got:\n%s", output)
	}
	if !strings.Contains(output, "go test") {
		t.Errorf("should show command 'go test' in Bash summary, got:\n%s", output)
	}
}

func TestToolStreamView_CursorNavigation(t *testing.T) {
	events := testEvents()
	v := NewToolStreamView(events, 80, 24)

	if v.cursor != 0 {
		t.Fatalf("initial cursor should be 0, got %d", v.cursor)
	}

	// Move down
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.cursor != 1 {
		t.Errorf("after j, cursor should be 1, got %d", v.cursor)
	}

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.cursor != 2 {
		t.Errorf("after second j, cursor should be 2, got %d", v.cursor)
	}

	// Move up
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if v.cursor != 1 {
		t.Errorf("after k, cursor should be 1, got %d", v.cursor)
	}

	// Home
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if v.cursor != 5 {
		t.Errorf("after G, cursor should be 5 (last), got %d", v.cursor)
	}

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if v.cursor != 0 {
		t.Errorf("after g, cursor should be 0, got %d", v.cursor)
	}

	// Can't go past boundaries
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if v.cursor != 0 {
		t.Errorf("at top, k should keep cursor at 0, got %d", v.cursor)
	}
}

func TestToolStreamView_ExpandCollapse(t *testing.T) {
	events := testEvents()
	v := NewToolStreamView(events, 120, 40)

	// Move to a tool_call event (index 1 = Read)
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.cursor != 1 {
		t.Fatalf("cursor should be 1, got %d", v.cursor)
	}

	if v.expanded[1] {
		t.Error("event 1 should not be expanded initially")
	}

	// Expand
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !v.expanded[1] {
		t.Error("event 1 should be expanded after Enter")
	}

	output := v.View()
	if !strings.Contains(output, "file_path") {
		t.Errorf("expanded tool_call should show input JSON with 'file_path', got:\n%s", output)
	}

	// Collapse
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v.expanded[1] {
		t.Error("event 1 should be collapsed after second Enter")
	}
}

func TestToolStreamView_ToolResultSubline(t *testing.T) {
	events := testEvents()
	v := NewToolStreamView(events, 120, 40)

	output := v.View()
	// tool_call at index 1 (Read main.go) should show a result sub-line with output
	if !strings.Contains(output, "└") {
		t.Errorf("tool_call should show result sub-line with └, got:\n%s", output)
	}
}

func TestToolStreamView_ErrorEvent(t *testing.T) {
	events := []session.SessionEvent{
		{ID: 1, Type: "error", Timestamp: time.Now(), Data: "Command failed: exit status 1"},
	}
	v := NewToolStreamView(events, 80, 24)
	output := v.View()
	if !strings.Contains(output, "Command failed") {
		t.Errorf("error event should show error text, got:\n%s", output)
	}
}

func TestToolStreamView_StatusChangeEvent(t *testing.T) {
	events := []session.SessionEvent{
		{ID: 1, Type: "status_change", Timestamp: time.Now(), Data: "done"},
	}
	v := NewToolStreamView(events, 80, 24)
	output := v.View()
	if !strings.Contains(output, "done") {
		t.Errorf("status_change event should show 'done', got:\n%s", output)
	}
}

func TestToolStreamView_ScrollPastVisible(t *testing.T) {
	// Create many events that exceed visible area
	base := time.Now()
	var events []session.SessionEvent
	for i := 0; i < 50; i++ {
		events = append(events, session.SessionEvent{
			ID:        i + 1,
			Type:      "text",
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Data:      `{"text":"Event line"}`,
		})
	}
	v := NewToolStreamView(events, 80, 10)

	// Move cursor to the bottom
	for i := 0; i < 49; i++ {
		v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	}
	if v.cursor != 49 {
		t.Errorf("cursor should be 49 after 49 j presses, got %d", v.cursor)
	}

	output := v.View()
	if output == "" {
		t.Error("view should not be empty even when scrolled")
	}
}

func TestToolStreamView_BackMessage(t *testing.T) {
	v := NewToolStreamView(testEvents(), 80, 24)

	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should produce a command")
	}
	msg := cmd()
	if _, ok := msg.(ToolStreamBackMsg); !ok {
		t.Errorf("q should produce ToolStreamBackMsg, got %T", msg)
	}
}

func TestToolStreamView_SetEvents(t *testing.T) {
	v := NewToolStreamView(nil, 80, 24)
	if len(v.events) != 0 {
		t.Fatal("initial events should be empty")
	}

	events := testEvents()
	v.SetEvents(events)
	if len(v.events) != 6 {
		t.Errorf("after SetEvents, should have 6 events, got %d", len(v.events))
	}

	// Cursor clamping
	v.cursor = 100
	v.SetEvents(events[:2])
	if v.cursor != 1 {
		t.Errorf("cursor should be clamped to 1, got %d", v.cursor)
	}
}

func TestToolStreamView_PageNavigation(t *testing.T) {
	base := time.Now()
	var events []session.SessionEvent
	for i := 0; i < 30; i++ {
		events = append(events, session.SessionEvent{
			ID:        i + 1,
			Type:      "text",
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Data:      `{"text":"Line"}`,
		})
	}
	v := NewToolStreamView(events, 80, 14) // listHeight = 10

	v.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if v.cursor < 5 {
		t.Errorf("PgDown should move cursor significantly, got %d", v.cursor)
	}

	v.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if v.cursor != 0 {
		t.Errorf("PgUp from near-top should go to 0, got %d", v.cursor)
	}
}

func TestToolStreamView_ToolCallSummary(t *testing.T) {
	tests := []struct {
		name     string
		data     toolCallData
		contains string
	}{
		{
			name: "Bash command",
			data: toolCallData{Name: "Bash", Input: []byte(`{"command":"echo hello"}`)},
			contains: "echo hello",
		},
		{
			name: "Read file",
			data: toolCallData{Name: "Read", Input: []byte(`{"file_path":"/tmp/test.go"}`)},
			contains: "/tmp/test.go",
		},
		{
			name: "Edit file",
			data: toolCallData{Name: "Edit", Input: []byte(`{"file_path":"handler.go"}`)},
			contains: "handler.go",
		},
		{
			name: "Generic tool",
			data: toolCallData{Name: "CustomTool", Input: []byte(`{"url":"https://example.com"}`)},
			contains: "url=",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toolCallSummary(tt.data, 80)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("summary should contain %q, got %q", tt.contains, result)
			}
		})
	}
}
