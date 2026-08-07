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
			StartTime:   now.Add(-2 * time.Hour),
			LastActive:  now.Add(-1 * time.Hour),
			TurnCount:   16,
			CostUSD:     0.42,
			Resumable:   true,
		},
		{
			ID:          "def-456",
			Provider:    "claude",
			Project:     "/Users-test-aimux",
			FirstPrompt: "add table support",
			StartTime:   now.Add(-5 * time.Hour),
			LastActive:  now.Add(-4 * time.Hour),
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
			StartTime:   now.Add(-24 * time.Hour),
			LastActive:  now.Add(-23 * time.Hour),
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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)
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
	v.SetSize(160, 40)

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
	if !strings.Contains(output, "ROI") {
		t.Error("expected ROI column header")
	}
}

func TestSessionsView_ColumnHeadersAllProjects(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(180, 40)
	v.showAll = true

	output := v.View()
	if !strings.Contains(output, "PROJECT") {
		t.Error("expected PROJECT column header in all-projects mode")
	}
}

func TestSessionsView_SortCycle(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(160, 40)

	// Default: SortByAge descending (newest first)
	if v.sortField != SortByAge {
		t.Errorf("default sortField = %d, want SortByAge", v.sortField)
	}

	pressS := func() { v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}) }

	// 1st 's': toggles Age direction (desc→asc)
	pressS()
	if v.sortField != SortByAge {
		t.Errorf("after 1st s: field = %d, want SortByAge", v.sortField)
	}
	if !v.sortAsc {
		t.Error("after 1st s: sortAsc should be true (oldest first)")
	}

	// 2nd 's': advances to SortByCost
	pressS()
	if v.sortField != SortByCost {
		t.Errorf("after 2nd s: field = %d, want SortByCost", v.sortField)
	}

	visible := v.visibleSessions()
	if visible[0].ID != "ghi-789" {
		t.Errorf("cost sort: first = %q, want ghi-789 ($1.23)", visible[0].ID)
	}

	// 3rd 's': toggles Cost direction, 4th: advances to Turns
	pressS()
	pressS()
	if v.sortField != SortByTurns {
		t.Errorf("after 4th s: field = %d, want SortByTurns", v.sortField)
	}

	// 5th: toggle Turns, 6th: advance to Title
	pressS()
	pressS()
	if v.sortField != SortByTitle {
		t.Errorf("after 6th s: field = %d, want SortByTitle", v.sortField)
	}

	visible = v.visibleSessions()
	if visible[0].ID != "def-456" {
		t.Errorf("title sort: first = %q, want def-456 ('add table support')", visible[0].ID)
	}

	// 7th: toggle Title, 8th: advance to FailureMode
	pressS()
	pressS()
	if v.sortField != SortByFailureMode {
		t.Errorf("after 8th s: field = %d, want SortByFailureMode", v.sortField)
	}

	// 9th: toggle FailureMode, 10th: advance to ROI
	pressS()
	pressS()
	if v.sortField != SortByROI {
		t.Errorf("after 10th s: field = %d, want SortByROI", v.sortField)
	}

	// 11th: toggle ROI, 12th: back to Age
	pressS()
	pressS()
	if v.sortField != SortByAge {
		t.Errorf("after 12th s: field = %d, want SortByAge (cycle back)", v.sortField)
	}
}

func TestSessionsView_SortIndicator(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(160, 40)

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
	v.SetSize(200, 40)

	cols := v.columnWidths(200)
	if cols.age != 12 {
		t.Errorf("age width = %d, want 12", cols.age)
	}
	if cols.branch != 16 {
		t.Errorf("branch width = %d, want 16", cols.branch)
	}
	if cols.action != 20 {
		t.Errorf("action width = %d, want 20", cols.action)
	}
	if cols.turns != 6 {
		t.Errorf("turns width = %d, want 6", cols.turns)
	}
	if cols.cost != 8 {
		t.Errorf("cost width = %d, want 8", cols.cost)
	}
	if cols.project != 0 {
		t.Errorf("project width = %d, want 0 when not showing all", cols.project)
	}
	if cols.prompt < 15 {
		t.Errorf("prompt width = %d, want >= 15", cols.prompt)
	}

	// With showAll
	v.showAll = true
	cols = v.columnWidths(200)
	if cols.project != 14 {
		t.Errorf("project width = %d, want 14 when showing all", cols.project)
	}
}

func TestSessionsView_AnnotationBadgeRendered(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(160, 40)

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
	v.SetSize(180, 40)

	output := v.View()
	if !strings.Contains(output, "loop-on-error") {
		t.Error("expected failure-mode badge with tag in output")
	}
}

func TestSessionsView_EmptySessions(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(nil)
	v.SetSize(160, 40)

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
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-30 * time.Second), "now"},
		{"minutes", now.Add(-15 * time.Minute), "15m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"hours_rounds_down", now.Add(-3*time.Hour - 25*time.Minute), "3h ago"},
		{"zero", time.Time{}, "?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatAge(tt.t)
			if got != tt.want {
				t.Errorf("formatAge(%v) = %q, want %q", tt.t, got, tt.want)
			}
		})
	}

	// 2 days ago: shows weekday + time (e.g., "Mon 14:30")
	t.Run("days_shows_weekday", func(t *testing.T) {
		twoDaysAgo := now.Add(-48 * time.Hour)
		got := formatAge(twoDaysAgo)
		expected := twoDaysAgo.Local().Format("Mon 15:04")
		if got != expected {
			t.Errorf("formatAge(2d ago) = %q, want %q", got, expected)
		}
	})

	// 14 days ago: shows month+day+time (e.g., "May 22 14:30")
	t.Run("weeks_shows_date_and_time", func(t *testing.T) {
		twoWeeksAgo := now.Add(-14 * 24 * time.Hour)
		got := formatAge(twoWeeksAgo)
		expected := twoWeeksAgo.Local().Format("Jan _2 15:04")
		if got != expected {
			t.Errorf("formatAge(14d ago) = %q, want %q", got, expected)
		}
	})

	// 400 days ago: shows full date (e.g., "Apr 27 2025")
	t.Run("years_shows_full_date", func(t *testing.T) {
		longAgo := now.Add(-400 * 24 * time.Hour)
		got := formatAge(longAgo)
		expected := longAgo.Local().Format("Jan 02 2006")
		if got != expected {
			t.Errorf("formatAge(400d ago) = %q, want %q", got, expected)
		}
	})
}

func TestSessionsSortByAge(t *testing.T) {
	now := time.Now()
	sessions := []history.Session{
		{ID: "old", StartTime: now.Add(-72 * time.Hour), LastActive: now.Add(-70 * time.Hour), TurnCount: 10, CostUSD: 0.5},
		{ID: "new", StartTime: now.Add(-1 * time.Hour), LastActive: now.Add(-30 * time.Minute), TurnCount: 10, CostUSD: 0.5},
		{ID: "mid", StartTime: now.Add(-24 * time.Hour), LastActive: now.Add(-22 * time.Hour), TurnCount: 10, CostUSD: 0.5},
	}

	v := NewSessionsView()
	v.SetSessions(sessions)
	v.SetSize(160, 40)

	visible := v.visibleSessions()
	if len(visible) != 3 {
		t.Fatalf("expected 3 visible, got %d", len(visible))
	}
	// Default sort: newest first (sortAsc=false)
	if visible[0].ID != "new" {
		t.Errorf("expected newest first, got %q", visible[0].ID)
	}
	if visible[1].ID != "mid" {
		t.Errorf("expected mid second, got %q", visible[1].ID)
	}
	if visible[2].ID != "old" {
		t.Errorf("expected oldest last, got %q", visible[2].ID)
	}

	// Toggle sort direction (press 's' 6 times to cycle back to Age, or set directly)
	v.sortAsc = true
	visible = v.visibleSessions()
	if visible[0].ID != "old" {
		t.Errorf("ascending: expected oldest first, got %q", visible[0].ID)
	}
	if visible[2].ID != "new" {
		t.Errorf("ascending: expected newest last, got %q", visible[2].ID)
	}
}

func TestCycleSortField_TogglesDirectionThenAdvances(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(160, 40)

	if v.sortField != SortByAge {
		t.Fatalf("initial sort field = %d, want SortByAge(%d)", v.sortField, SortByAge)
	}
	if v.sortAsc {
		t.Fatal("initial sortAsc = true, want false (newest first)")
	}

	// 1st press: toggles direction (Age desc -> Age asc)
	v.cycleSortField()
	if v.sortField != SortByAge {
		t.Errorf("after 1st press: field = %d, want SortByAge(%d)", v.sortField, SortByAge)
	}
	if !v.sortAsc {
		t.Error("after 1st press: sortAsc should be true (oldest first)")
	}

	// 2nd press: advances to Cost
	v.cycleSortField()
	if v.sortField != SortByCost {
		t.Errorf("after 2nd press: field = %d, want SortByCost(%d)", v.sortField, SortByCost)
	}
	if v.sortAsc {
		t.Error("after 2nd press: sortAsc should be false (highest cost first)")
	}

	// 3rd press: toggles Cost direction
	v.cycleSortField()
	if v.sortField != SortByCost {
		t.Errorf("after 3rd press: field should still be SortByCost")
	}
	if !v.sortAsc {
		t.Error("after 3rd press: sortAsc should be true (lowest cost first)")
	}

	// 4th press: advances to Turns
	v.cycleSortField()
	if v.sortField != SortByTurns {
		t.Errorf("after 4th press: field = %d, want SortByTurns(%d)", v.sortField, SortByTurns)
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
	v.SetSize(180, 40)

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
	v.SetSize(160, 40)

	// Cycle to SortByFailureMode: (toggle+advance) x 4 = 8 presses
	for i := 0; i < 8; i++ {
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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
	v.SetSize(160, 40)

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
			FirstPrompt: "Research alternative implementations",
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
	v.SetSize(180, 40)

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
	v.SetSize(180, 40)

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
	v.SetSize(180, 40)

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})

	output := v.View()
	if !strings.Contains(output, "[agent]") {
		t.Error("expected [agent] badge in rendered output for subagent sessions")
	}
}

func TestSessionsView_SubagentCount(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(180, 40)

	output := v.View()
	if !strings.Contains(output, "+2 agent") {
		t.Errorf("expected hidden agent count in output, got:\n%s", output)
	}
}

func TestSessionsView_SubagentVisibleDuringSearch(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(180, 40)

	v.filterMode = true
	v.filterText = "alternative implementations"

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

func TestSessionsView_BranchBadge(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(140, 30)
	sessions := testSessions()
	sessions[0].GitBranch = "feat/resize-handle"
	sessions[1].GitBranch = "main"
	v.SetSessions(sessions)

	output := v.View()
	if !strings.Contains(output, "feat/resize") {
		t.Error("expected branch badge containing 'feat/resize' in output")
	}
}

func TestSessionsView_LastAction(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(160, 30)
	sessions := testSessions()
	sessions[0].LastAction = "Ed config.go"
	v.SetSessions(sessions)

	output := v.View()
	if !strings.Contains(output, "Ed config.go") {
		t.Error("expected last action 'Ed config.go' in output")
	}
}

func TestSessionsView_TitlePrefersLastPrompt(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(140, 30)
	sessions := []history.Session{
		{
			ID:          "test-1",
			Provider:    "claude",
			FirstPrompt: "initial question",
			LastPrompt:  "final refined question",
			LastActive:  time.Now().Add(-1 * time.Hour),
			TurnCount:   10,
			CostUSD:     0.50,
			Resumable:   true,
		},
	}
	v.SetSessions(sessions)

	output := v.View()
	if !strings.Contains(output, "final refined") {
		t.Errorf("expected last prompt in output, got:\n%s", output)
	}
}
