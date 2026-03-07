package views

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/history"
)

func testSessions() []history.Session {
	now := time.Now()
	return []history.Session{
		{
			ID:          "abc-123",
			Provider:    "claude",
			Project:     "/Users-test-aimux",
			FirstPrompt: "fix markdown rendering",
			LastActive:  now.Add(-2 * time.Hour),
			TurnCount:   16,
			CostUSD:     0.42,
			Resumable:   true,
		},
		{
			ID:          "def-456",
			Provider:    "claude",
			Project:     "/Users-test-aimux",
			FirstPrompt: "add table support",
			LastActive:  now.Add(-5 * time.Hour),
			TurnCount:   8,
			CostUSD:     0.18,
			Resumable:   true,
			Annotation:  "achieved",
		},
		{
			ID:          "ghi-789",
			Provider:    "claude",
			Project:     "/Users-test-conductor",
			FirstPrompt: "OTEL export to MLflow",
			LastActive:  now.Add(-24 * time.Hour),
			TurnCount:   34,
			CostUSD:     1.23,
			Resumable:   true,
			Annotation:  "failed",
			Tags:        []string{"loop-on-error"},
		},
	}
}

func TestSessionsView_InitialState(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	if v.SelectedSession() == nil {
		t.Fatal("expected selected session")
	}
	if v.SelectedSession().ID != "abc-123" {
		t.Errorf("expected first session selected, got %q", v.SelectedSession().ID)
	}
}

func TestSessionsView_Navigation(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	// Move down
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.SelectedSession().ID != "def-456" {
		t.Errorf("after j: expected def-456, got %q", v.SelectedSession().ID)
	}

	// Move down again
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.SelectedSession().ID != "ghi-789" {
		t.Errorf("after j again: expected ghi-789, got %q", v.SelectedSession().ID)
	}

	// Move up
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if v.SelectedSession().ID != "def-456" {
		t.Errorf("after k: expected def-456, got %q", v.SelectedSession().ID)
	}

	// Jump to end
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if v.SelectedSession().ID != "ghi-789" {
		t.Errorf("after G: expected ghi-789, got %q", v.SelectedSession().ID)
	}

	// Jump to start
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if v.SelectedSession().ID != "abc-123" {
		t.Errorf("after g: expected abc-123, got %q", v.SelectedSession().ID)
	}
}

func TestSessionsView_AnnotationCycle(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	// First press: achieved
	cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if cmd == nil {
		t.Fatal("expected command from annotation")
	}
	msg := cmd()
	annotMsg, ok := msg.(SessionAnnotateMsg)
	if !ok {
		t.Fatalf("expected SessionAnnotateMsg, got %T", msg)
	}
	if annotMsg.Annotation != "achieved" {
		t.Errorf("first v: annotation = %q, want %q", annotMsg.Annotation, "achieved")
	}

	// Second press: partial
	cmd = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	msg = cmd()
	annotMsg = msg.(SessionAnnotateMsg)
	if annotMsg.Annotation != "partial" {
		t.Errorf("second v: annotation = %q, want %q", annotMsg.Annotation, "partial")
	}
}

func TestSessionsView_ResumeEmitsMessage(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected command from enter")
	}
	msg := cmd()
	resumeMsg, ok := msg.(SessionResumeMsg)
	if !ok {
		t.Fatalf("expected SessionResumeMsg, got %T", msg)
	}
	if resumeMsg.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", resumeMsg.SessionID, "abc-123")
	}
}

func TestSessionsView_FilterByPrompt(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	// Apply filter
	v.filterText = "OTEL"
	visible := v.visibleSessions()
	if len(visible) != 1 {
		t.Fatalf("expected 1 visible session with filter 'OTEL', got %d", len(visible))
	}
	if visible[0].ID != "ghi-789" {
		t.Errorf("filtered session = %q, want %q", visible[0].ID, "ghi-789")
	}
}

func TestSessionsView_FilterByTag(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	v.filterText = "loop"
	visible := v.visibleSessions()
	if len(visible) != 1 {
		t.Fatalf("expected 1 visible session with tag filter 'loop', got %d", len(visible))
	}
	if visible[0].ID != "ghi-789" {
		t.Errorf("filtered session = %q, want %q", visible[0].ID, "ghi-789")
	}
}

func TestSessionsView_FilterByAnnotation(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	v.filterText = "achieved"
	visible := v.visibleSessions()
	if len(visible) != 1 {
		t.Fatalf("expected 1 session with annotation filter, got %d", len(visible))
	}
	if visible[0].ID != "def-456" {
		t.Errorf("filtered session = %q, want %q", visible[0].ID, "def-456")
	}
}

func TestSessionsView_ToggleAllProjects(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	if v.ShowAll() {
		t.Error("expected showAll = false initially")
	}

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if !v.ShowAll() {
		t.Error("expected showAll = true after A")
	}

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if v.ShowAll() {
		t.Error("expected showAll = false after second A")
	}
}

func TestSessionsView_ViewRenders(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)
	v.SetCurrentDir("/Users/test/aimux")

	output := v.View()
	if !strings.Contains(output, "Sessions") {
		t.Error("expected 'Sessions' header in output")
	}
	if !strings.Contains(output, "fix markdown rendering") {
		t.Error("expected first prompt in output")
	}
	if !strings.Contains(output, "3 sessions") {
		t.Error("expected session count in output")
	}
}

func TestSessionsView_AnnotationBadgeRendered(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	output := v.View()
	if !strings.Contains(output, "ACHIEVED") {
		t.Error("expected ACHIEVED badge in output")
	}
	if !strings.Contains(output, "FAILED") {
		t.Error("expected FAILED badge in output")
	}
}

func TestSessionsView_TagsRendered(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	output := v.View()
	if !strings.Contains(output, "loop-on-error") {
		t.Error("expected tag in output")
	}
}

func TestSessionsView_EmptySessions(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(nil)
	v.SetSize(100, 40)

	output := v.View()
	if !strings.Contains(output, "No sessions found") {
		t.Error("expected 'No sessions found' message")
	}

	if v.SelectedSession() != nil {
		t.Error("expected nil selected session when empty")
	}
}

func TestSessionsView_HasActiveInput(t *testing.T) {
	v := NewSessionsView()
	if v.HasActiveInput() {
		t.Error("expected no active input initially")
	}

	v.filterMode = true
	if !v.HasActiveInput() {
		t.Error("expected active input in filter mode")
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"loop-on-error, wrong-file", []string{"loop-on-error", "wrong-file"}},
		{"single-tag", []string{"single-tag"}},
		{" , , empty, , ", []string{"empty"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := parseTags(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("parseTags(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "now"},
		{now.Add(-15 * time.Minute), "15m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-48 * time.Hour), "2d ago"},
		{time.Time{}, "?"},
	}
	for _, tt := range tests {
		got := formatAge(tt.t)
		if got != tt.want {
			t.Errorf("formatAge(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestShortProject(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users-test-aimux", "aimux"},                               // decoded path
		{"/Users-azaalouk-go-src-github-com-zanetworker-aimux", "aimux"}, // long encoded path
		{"", "(unknown)"},
	}
	for _, tt := range tests {
		got := shortProject(tt.input)
		if got != tt.want {
			t.Errorf("shortProject(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSessionMatchesFilter(t *testing.T) {
	s := history.Session{
		FirstPrompt: "fix the bug in main.go",
		Project:     "/Users/test/aimux",
		Annotation:  "failed",
		Tags:        []string{"loop-on-error"},
	}

	if !sessionMatchesFilter(s, "bug") {
		t.Error("expected match on prompt text")
	}
	if !sessionMatchesFilter(s, "aimux") {
		t.Error("expected match on project")
	}
	if !sessionMatchesFilter(s, "failed") {
		t.Error("expected match on annotation")
	}
	if !sessionMatchesFilter(s, "loop") {
		t.Error("expected match on tag")
	}
	if sessionMatchesFilter(s, "nonexistent") {
		t.Error("expected no match")
	}
}

func TestRenderSessionAnnotation(t *testing.T) {
	tests := []struct {
		label string
		want  string
	}{
		{"achieved", "ACHIEVED"},
		{"failed", "FAILED"},
		{"partial", "PARTIAL"},
		{"abandoned", "ABANDONED"},
	}
	for _, tt := range tests {
		got := renderSessionAnnotation(tt.label)
		if !strings.Contains(got, tt.want) {
			t.Errorf("renderSessionAnnotation(%q) = %q, expected to contain %q", tt.label, got, tt.want)
		}
	}
}
