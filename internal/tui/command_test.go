package tui

import (
	"testing"
)

func TestResolveCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"instances", "instances"},
		{"i", "instances"},
		{"logs", "logs"},
		{"l", "logs"},
		{"session", "session"},
		{"s", "session"},
		{"teams", "teams"},
		{"t", "teams"},
		{"costs", "costs"},
		{"c", "costs"},
		{"quit", "quit"},
		{"q", "quit"},
		{"help", "help"},
		{"?", "help"},
		{"new", "new"},
		{"n", "new"},
		{"kill", "kill"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		if got := resolveCommand(tt.input); got != tt.want {
			t.Errorf("resolveCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCommandCompletions(t *testing.T) {
	results := commandCompletions("co")
	found := false
	for _, r := range results {
		if r == "costs" {
			found = true
		}
	}
	if !found {
		t.Errorf("commandCompletions(\"co\") should include \"costs\", got %v", results)
	}
}

func TestCommandCompletionsEmpty(t *testing.T) {
	results := commandCompletions("zzz")
	if len(results) != 0 {
		t.Errorf("commandCompletions(\"zzz\") = %v, want empty", results)
	}
}
