package history

import "testing"

func TestResolveModel(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		known    bool
	}{
		{"haiku", "claude-haiku-5-5", true},
		{"", "claude-haiku-5-5", true},
		{"sonnet", "claude-sonnet-5-5", true},
		{"opus", "claude-opus-5-5", true},
		{" Sonnet ", "claude-sonnet-5-5", true},
		{"claude-opus-5-5", "claude-opus-5-5", true},
		{"gpt-4", "claude-haiku-5-5", false},
		{"claude-haiku-4-5-20251001", "claude-haiku-5-5", false},
	} {
		got, known := ResolveModel(tc.in)
		if got != tc.want || known != tc.known {
			t.Errorf("ResolveModel(%q) = %q, %v; want %q, %v", tc.in, got, known, tc.want, tc.known)
		}
	}
}

func TestUselessTitle(t *testing.T) {
	for _, tc := range []struct {
		title   string
		useless bool
	}{
		{"", true},
		{"   ", true},
		{"(no prompt)", true},
		{"c513a7b6", true},
		{"Session c513a7b6", true},
		{"session c513a7b6-02d4-4c4f-9041-87df684d828c", true},
		{"c513a7b6-02d4-4c4f-9041-87df684d828c", true},
		{"continue c513a7b6", true},
		{"Continue session c513a7b6-02d4-4c4f-9041-87df684d828c", true},
		{"continue: c513a7b6", true},
		{"/clear", true},
		{"/superpowers:brainstorming", true},
		{" /review-pr ", true},
		// fine titles
		{"Fix login redirect loop", false},
		{"Session timeout handling", false},
		{"continue the refactor of the parser", false},
		{"/loop 5m check the deploy", false},
		{"facade", false},      // hex letters, no digit: a word
		{"Session abc", false}, // too short for an id
		{"continue", false},    // no id
		{"Deploy build 1a2b3c4d to staging", false},
	} {
		if got := UselessTitle(tc.title); got != tc.useless {
			t.Errorf("UselessTitle(%q) = %v, want %v", tc.title, got, tc.useless)
		}
	}
}

func TestShownTitle(t *testing.T) {
	s := Session{FirstPrompt: "first message"}
	if got := ShownTitle(s); got != "first message" {
		t.Errorf("first message: got %q", got)
	}
	s.AITitle = "Claude Code name"
	if got := ShownTitle(s); got != "Claude Code name" {
		t.Errorf("ai-title: got %q", got)
	}
	s.Title = "Meta title"
	if got := ShownTitle(s); got != "Meta title" {
		t.Errorf("meta title: got %q", got)
	}
}
