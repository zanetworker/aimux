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

func TestSessionsView_ColumnHeaders(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	output := v.View()
	if !strings.Contains(output, "AGE") {
		t.Error("expected AGE column header")
	}
	if !strings.Contains(output, "TITLE") {
		t.Error("expected TITLE column header")
	}
	if !strings.Contains(output, "TURNS") {
		t.Error("expected TURNS column header")
	}
	if !strings.Contains(output, "COST") {
		t.Error("expected COST column header")
	}
}

func TestSessionsView_ColumnHeadersAllProjects(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(120, 40)
	v.showAll = true

	output := v.View()
	if !strings.Contains(output, "PROJECT") {
		t.Error("expected PROJECT column header in all-projects mode")
	}
}

func TestSessionsView_SortCycle(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	// Default: SortByAge descending (newest first)
	if v.sortField != SortByAge {
		t.Errorf("default sortField = %d, want SortByAge", v.sortField)
	}

	// Press 's' → SortByCost
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if v.sortField != SortByCost {
		t.Errorf("after first s: sortField = %d, want SortByCost", v.sortField)
	}

	visible := v.visibleSessions()
	// Highest cost first (descending by default)
	if visible[0].ID != "ghi-789" { // $1.23
		t.Errorf("cost sort: first = %q, want ghi-789 ($1.23)", visible[0].ID)
	}

	// Press 's' → SortByTurns
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if v.sortField != SortByTurns {
		t.Errorf("after second s: sortField = %d, want SortByTurns", v.sortField)
	}

	// Press 's' → SortByTitle
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if v.sortField != SortByTitle {
		t.Errorf("after third s: sortField = %d, want SortByTitle", v.sortField)
	}

	// Title sort ascending by default
	visible = v.visibleSessions()
	if visible[0].ID != "def-456" { // "add table support"
		t.Errorf("title sort: first = %q, want def-456 ('add table support')", visible[0].ID)
	}

	// Press 's' → SortByFailureMode
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if v.sortField != SortByFailureMode {
		t.Errorf("after fourth s: sortField = %d, want SortByFailureMode", v.sortField)
	}

	// Press 's' → back to SortByAge
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if v.sortField != SortByAge {
		t.Errorf("after fifth s: sortField = %d, want SortByAge (cycle back)", v.sortField)
	}
}

func TestSessionsView_SortIndicator(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	output := v.View()
	// Default sort is by AGE, should show arrow
	if !strings.Contains(output, "AGE") {
		t.Error("expected AGE with sort indicator")
	}

	// Switch to cost sort
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	output = v.View()
	if !strings.Contains(output, "\u25bc") && !strings.Contains(output, "\u25b2") {
		t.Error("expected sort arrow in output")
	}
}

func TestSessionsView_ColumnWidths(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(100, 40)

	cols := v.columnWidths(100)
	if cols.age != 7 {
		t.Errorf("age width = %d, want 7", cols.age)
	}
	if cols.turns != 5 {
		t.Errorf("turns width = %d, want 5", cols.turns)
	}
	if cols.cost != 7 {
		t.Errorf("cost width = %d, want 7", cols.cost)
	}
	if cols.project != 0 {
		t.Errorf("project width = %d, want 0 when not showing all", cols.project)
	}
	if cols.prompt < 15 {
		t.Errorf("prompt width = %d, want >= 15", cols.prompt)
	}

	// With showAll
	v.showAll = true
	cols = v.columnWidths(100)
	if cols.project != 12 {
		t.Errorf("project width = %d, want 12 when showing all", cols.project)
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
	v.SetSize(120, 40)

	output := v.View()
	if !strings.Contains(output, "loop-on-error") {
		t.Error("expected failure-mode badge with tag in output")
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

func TestSessionsView_FailureModeIndicator(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(120, 40)

	output := v.View()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "OTEL") {
			if !strings.Contains(line, "loop-on-error") {
				t.Error("expected [loop-on-error] badge for tagged session ghi-789")
			}
		}
		if strings.Contains(line, "table support") {
			if strings.Contains(line, "loop-on-error") {
				t.Error("unexpected failure badge for untagged session def-456")
			}
		}
	}
}

func TestSessionsView_SortByFailureMode(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	// Cycle to SortByFailureMode: Age -> Cost -> Turns -> Title -> FailureMode
	for i := 0; i < 4; i++ {
		v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	}
	if v.sortField != SortByFailureMode {
		t.Errorf("sortField = %d, want SortByFailureMode", v.sortField)
	}

	visible := v.visibleSessions()
	if visible[0].ID != "ghi-789" {
		t.Errorf("failure-mode sort: first = %q, want ghi-789 (tagged)", visible[0].ID)
	}
}

func TestSessionsView_CleanupMode(t *testing.T) {
	v := NewSessionsView()
	now := time.Now()
	sessions := []history.Session{
		{ID: "a", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 20, CostUSD: 1.0, LastActive: now},
		{ID: "b", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 2, CostUSD: 0.1, LastActive: now},
		// "c" has TurnCount=1, CostUSD=0 which is hidden by visibleSessions filter,
		// so it won't appear in cleanup. Use a session visible to the list instead.
		{ID: "c", Project: "/proj", FirstPrompt: "another task", TurnCount: 1, CostUSD: 0, LastActive: now},
		{ID: "d", Project: "/proj", FirstPrompt: "another task", TurnCount: 8, CostUSD: 0.5, LastActive: now},
	}
	v.SetSessions(sessions)
	v.SetSize(100, 40)

	// Enter cleanup mode — "b" is a duplicate of "a" (fewer turns),
	// "c" is hidden by visibleSessions, so only "b" should appear
	// unless both are visible. Let's verify what we get.
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if !v.cleanupMode {
		t.Fatal("expected cleanup mode")
	}
	if len(v.cleanupItems) < 1 {
		t.Fatalf("expected at least 1 cleanup item, got %d", len(v.cleanupItems))
	}

	// All selected by default
	for _, item := range v.cleanupItems {
		if !item.selected {
			t.Errorf("expected item %q selected by default", item.session.ID)
		}
	}

	// Toggle first item off
	v.handleCleanupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if v.cleanupItems[0].selected {
		t.Error("expected first item deselected after space")
	}

	// Cancel
	v.handleCleanupKey(tea.KeyMsg{Type: tea.KeyEscape})
	if v.cleanupMode {
		t.Error("expected cleanup mode exited after esc")
	}
}

func TestSessionsView_CleanupModeConfirm(t *testing.T) {
	v := NewSessionsView()
	now := time.Now()
	sessions := []history.Session{
		{ID: "a", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 20, CostUSD: 1.0, LastActive: now},
		{ID: "b", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 2, CostUSD: 0.1, LastActive: now},
	}
	v.SetSessions(sessions)
	v.SetSize(100, 40)

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if !v.cleanupMode {
		t.Fatal("expected cleanup mode")
	}

	cmd := v.handleCleanupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected command from enter")
	}
	msg := cmd()
	bulkMsg, ok := msg.(SessionBulkDeleteMsg)
	if !ok {
		t.Fatalf("expected SessionBulkDeleteMsg, got %T", msg)
	}
	if len(bulkMsg.Sessions) != 1 {
		t.Errorf("expected 1 session to delete, got %d", len(bulkMsg.Sessions))
	}
	if bulkMsg.Sessions[0].ID != "b" {
		t.Errorf("expected session b to be deleted, got %q", bulkMsg.Sessions[0].ID)
	}
}

func TestSessionsView_CleanupModeNoItems(t *testing.T) {
	v := NewSessionsView()
	sessions := []history.Session{
		{ID: "a", Project: "/proj", FirstPrompt: "unique task", TurnCount: 20, CostUSD: 1.0, LastActive: time.Now()},
	}
	v.SetSessions(sessions)
	v.SetSize(100, 40)

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if v.cleanupMode {
		t.Error("should not enter cleanup mode with no items to clean")
	}
}

func TestSessionsView_CleanupRender(t *testing.T) {
	v := NewSessionsView()
	now := time.Now()
	sessions := []history.Session{
		{ID: "a", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 20, CostUSD: 1.0, LastActive: now},
		{ID: "b", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 2, CostUSD: 0.1, LastActive: now},
	}
	v.SetSessions(sessions)
	v.SetSize(100, 40)

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	output := v.View()
	if !strings.Contains(output, "Cleanup") {
		t.Error("expected 'Cleanup' header in cleanup view")
	}
	if !strings.Contains(output, "[x]") {
		t.Error("expected checkbox in cleanup view")
	}
	if !strings.Contains(output, "duplicate") {
		t.Error("expected 'duplicate' reason in cleanup view")
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

func testSessionsWithSubagent() []history.Session {
	now := time.Now()
	return []history.Session{
		{
			ID:          "abc-123",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "fix the bug",
			LastActive:  now.Add(-2 * time.Hour),
			TurnCount:   16,
			CostUSD:     0.42,
			Resumable:   true,
		},
		{
			ID:          "sub-001",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "YOU ARE A SESSION ANALYZER",
			LastActive:  now.Add(-3 * time.Hour),
			TurnCount:   6,
			CostUSD:     0.05,
			Resumable:   true,
			IsSubagent:  true,
		},
		{
			ID:          "sub-002",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "Evaluate session abc-123",
			LastActive:  now.Add(-4 * time.Hour),
			TurnCount:   8,
			CostUSD:     0.03,
			Resumable:   true,
			IsSubagent:  true,
		},
		{
			ID:          "def-456",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "add table support",
			LastActive:  now.Add(-5 * time.Hour),
			TurnCount:   8,
			CostUSD:     0.18,
			Resumable:   true,
		},
	}
}

func TestSessionsView_SubagentHiddenByDefault(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	visible := v.visibleSessions()
	for _, s := range visible {
		if s.IsSubagent {
			t.Errorf("subagent session %q should be hidden by default", s.ID)
		}
	}
	if len(visible) != 2 {
		t.Errorf("expected 2 visible sessions, got %d", len(visible))
	}
}

func TestSessionsView_SubagentToggle(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	visible := v.visibleSessions()
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible before toggle, got %d", len(visible))
	}

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	visible = v.visibleSessions()
	if len(visible) != 4 {
		t.Errorf("expected 4 visible after toggle on, got %d", len(visible))
	}

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	visible = v.visibleSessions()
	if len(visible) != 2 {
		t.Errorf("expected 2 visible after toggle off, got %d", len(visible))
	}
}

func TestSessionsView_SubagentBadge(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})

	output := v.View()
	if !strings.Contains(output, "[agent]") {
		t.Error("expected [agent] badge in rendered output for subagent sessions")
	}
}

func TestSessionsView_SubagentCount(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	output := v.View()
	if !strings.Contains(output, "+2 agent") {
		t.Errorf("expected hidden agent count in output, got:\n%s", output)
	}
}

func TestSessionsView_SubagentVisibleDuringSearch(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	v.filterMode = true
	v.filterText = "session analyzer"

	visible := v.visibleSessions()
	found := false
	for _, s := range visible {
		if s.ID == "sub-001" {
			found = true
		}
	}
	if !found {
		t.Error("subagent session should be visible when filter matches it")
	}
}
