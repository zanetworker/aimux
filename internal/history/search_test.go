package history

import (
	"testing"
)

func TestFilterByPrompt_MatchesTitle(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "Add clipboard support", FirstPrompt: "do something"},
		{ID: "b", Title: "Fix auth bug", FirstPrompt: "help me"},
	}
	result := FilterByPrompt(sessions, "clipboard")
	if len(result) != 1 {
		t.Fatalf("got %d, want 1", len(result))
	}
	if result[0].ID != "a" {
		t.Errorf("got ID %q, want %q", result[0].ID, "a")
	}
}

func TestFilterByPrompt_MatchesFirstPrompt(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "", FirstPrompt: "implement fuzzy search"},
		{ID: "b", Title: "", FirstPrompt: "fix the tests"},
	}
	result := FilterByPrompt(sessions, "fuzzy")
	if len(result) != 1 {
		t.Fatalf("got %d, want 1", len(result))
	}
	if result[0].ID != "a" {
		t.Errorf("got ID %q, want %q", result[0].ID, "a")
	}
}

func TestFilterByPrompt_CaseInsensitive(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "UPPERCASE TITLE", FirstPrompt: ""},
	}
	result := FilterByPrompt(sessions, "uppercase")
	if len(result) != 1 {
		t.Fatalf("got %d, want 1", len(result))
	}
}

func TestFilterByPrompt_EmptyQuery(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "something"},
	}
	result := FilterByPrompt(sessions, "")
	if len(result) != 1 {
		t.Fatalf("empty query should return all sessions, got %d", len(result))
	}
}

func TestFilterByPrompt_NoMatch(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "auth fix", FirstPrompt: "fix auth"},
	}
	result := FilterByPrompt(sessions, "clipboard")
	if len(result) != 0 {
		t.Errorf("got %d, want 0", len(result))
	}
}
