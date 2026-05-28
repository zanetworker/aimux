package clipboard

import (
	"testing"
)

func TestResumeCommand(t *testing.T) {
	tests := []struct {
		sessionID  string
		workingDir string
		want       string
	}{
		{"abc-123", "", "claude --resume 'abc-123'"},
		{"abc-123", "/home/user/project", "cd '/home/user/project' && claude --resume 'abc-123'"},
		{"session-with-dashes", "", "claude --resume 'session-with-dashes'"},
		{"sess-1", "/tmp/test", "cd '/tmp/test' && claude --resume 'sess-1'"},
	}
	for _, tt := range tests {
		got := ResumeCommand(tt.sessionID, tt.workingDir)
		if got != tt.want {
			t.Errorf("ResumeCommand(%q, %q) = %q, want %q", tt.sessionID, tt.workingDir, got, tt.want)
		}
	}
}

func TestResumeCommand_ShellQuoting(t *testing.T) {
	got := ResumeCommand("abc-123", "/path with spaces/project")
	want := "cd '/path with spaces/project' && claude --resume 'abc-123'"
	if got != want {
		t.Errorf("spaces: got %q, want %q", got, want)
	}

	got = ResumeCommand("abc-123", "/path/with'quote")
	want = "cd '/path/with'\"'\"'quote' && claude --resume 'abc-123'"
	if got != want {
		t.Errorf("quote: got %q, want %q", got, want)
	}
}

func TestResumeCommand_RejectsInvalidSessionID(t *testing.T) {
	tests := []string{
		"abc; touch /tmp/pwned",
		"$(whoami)",
		"abc`id`",
		"abc 123",
		"abc/def",
		"",
	}
	for _, id := range tests {
		got := ResumeCommand(id, "/tmp")
		if got != "" {
			t.Errorf("ResumeCommand(%q, ...) = %q, want empty for invalid ID", id, got)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"with'quote", "'with'\"'\"'quote'"},
		{"/normal/path", "'/normal/path'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.in)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
