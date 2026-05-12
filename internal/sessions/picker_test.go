package sessions

import (
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

func TestFormatLine_BasicSession(t *testing.T) {
	s := history.Session{
		ID:         "abc-123-def",
		Project:    "/Users/me/myproject",
		LastActive: time.Now().Add(-2 * time.Hour),
		TurnCount:  15,
		CostUSD:    1.23,
		Title:      "Add fuzzy search",
	}
	line := FormatLine(s)
	if !strings.Contains(line, "abc-123-def") {
		t.Errorf("line missing ID: %q", line)
	}
	if !strings.Contains(line, "myproject") {
		t.Errorf("line missing project: %q", line)
	}
	if !strings.Contains(line, "Add fuzzy search") {
		t.Errorf("line missing title: %q", line)
	}
}

func TestFormatLine_FallsBackToFirstPrompt(t *testing.T) {
	s := history.Session{
		ID:          "xyz-789",
		Project:     "/Users/me/proj",
		LastActive:  time.Now(),
		TurnCount:   5,
		CostUSD:     0.50,
		Title:       "",
		FirstPrompt: "fix the auth bug",
	}
	line := FormatLine(s)
	if !strings.Contains(line, "fix the auth bug") {
		t.Errorf("line missing first prompt: %q", line)
	}
}

func TestParseSelectedLine_ExtractsID(t *testing.T) {
	line := "abc-123-def  myproject  2h ago   15T  $  1.23  Add fuzzy search"
	id := ParseSelectedID(line)
	if id != "abc-123-def" {
		t.Errorf("got %q, want %q", id, "abc-123-def")
	}
}

func TestParseSelectedLine_WithANSI(t *testing.T) {
	line := "\033[36mabc-123-def\033[0m  \033[32mmyproject\033[0m  2h ago   15T  $  1.23  title"
	id := ParseSelectedID(line)
	if id != "abc-123-def" {
		t.Errorf("got %q, want %q", id, "abc-123-def")
	}
}

func TestParseSelectedLine_FromFormatLine(t *testing.T) {
	s := history.Session{
		ID:        "test-uuid-123",
		Project:   "/Users/me/proj",
		LastActive: time.Now(),
		TurnCount: 10,
		CostUSD:   5.00,
		Title:     "some title",
	}
	line := FormatLine(s)
	id := ParseSelectedID(line)
	if id != "test-uuid-123" {
		t.Errorf("got %q, want %q", id, "test-uuid-123")
	}
}

func TestParseSelectedLine_EmptyLine(t *testing.T) {
	id := ParseSelectedID("")
	if id != "" {
		t.Errorf("got %q, want empty", id)
	}
}

func TestParseSelectedLine_WhitespaceOnly(t *testing.T) {
	id := ParseSelectedID("   ")
	if id != "" {
		t.Errorf("got %q, want empty", id)
	}
}

func TestFormatPreview_ShowsFirstPrompt(t *testing.T) {
	s := history.Session{
		ID:          "abc",
		Title:       "My title",
		FirstPrompt: "the actual first message typed",
	}
	preview := FormatPreview(s)
	if preview == "" {
		t.Fatal("expected non-empty preview when title and prompt differ")
	}
	if !strings.Contains(preview, "the actual first message typed") {
		t.Errorf("preview missing prompt: %q", preview)
	}
}

func TestFormatPreview_EmptyWhenNoPrompt(t *testing.T) {
	s := history.Session{ID: "abc", Title: "title", FirstPrompt: ""}
	if FormatPreview(s) != "" {
		t.Error("expected empty preview when no first prompt")
	}
}

func TestFormatPreview_EmptyWhenSameAsTitle(t *testing.T) {
	s := history.Session{ID: "abc", Title: "same text", FirstPrompt: "same text"}
	if FormatPreview(s) != "" {
		t.Error("expected empty preview when prompt equals title")
	}
}

func TestPickerModel_FilterReducesList(t *testing.T) {
	sessions := []history.Session{
		{ID: "aaa", Title: "Add clipboard"},
		{ID: "bbb", Title: "Fix auth"},
		{ID: "ccc", Title: "Add search"},
	}
	m := newPickerModel(sessions)
	m.filter = "add"
	filtered := m.filteredSessions()
	if len(filtered) != 2 {
		t.Fatalf("got %d filtered, want 2", len(filtered))
	}
}

func TestPickerModel_FilterCaseInsensitive(t *testing.T) {
	sessions := []history.Session{
		{ID: "aaa", Title: "UPPERCASE"},
	}
	m := newPickerModel(sessions)
	m.filter = "upper"
	filtered := m.filteredSessions()
	if len(filtered) != 1 {
		t.Fatalf("got %d filtered, want 1", len(filtered))
	}
}

func TestPickerModel_EmptyFilterShowsAll(t *testing.T) {
	sessions := []history.Session{
		{ID: "aaa", Title: "one"},
		{ID: "bbb", Title: "two"},
	}
	m := newPickerModel(sessions)
	m.filter = ""
	filtered := m.filteredSessions()
	if len(filtered) != 2 {
		t.Fatalf("got %d filtered, want 2", len(filtered))
	}
}
