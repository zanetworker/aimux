package discovery

import (
	"testing"

	"github.com/zanetworker/claudetopus/internal/model"
)

func TestParseProcessLine(t *testing.T) {
	line := "user       12345   0.5  1.2  500000 102400 s001  S+   10:30AM   0:05.00 /usr/local/bin/claude --model opus --resume sess-abc"
	proc, err := parseProcessLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if proc.PID != 12345 {
		t.Errorf("PID = %d, want 12345", proc.PID)
	}
	if proc.MemoryKB != 102400 {
		t.Errorf("MemoryKB = %d, want 102400", proc.MemoryKB)
	}
	if proc.Command == "" {
		t.Error("Command should not be empty")
	}
	if !contains(proc.Command, "claude") {
		t.Errorf("Command should contain 'claude', got %q", proc.Command)
	}
}

func TestParseProcessLineTooFewFields(t *testing.T) {
	_, err := parseProcessLine("user 123 0.0")
	if err == nil {
		t.Error("expected error for line with too few fields")
	}
}

func TestParseProcessLineInvalidPID(t *testing.T) {
	line := "user       abc   0.5  1.2  500000 102400 s001  S+   10:30AM   0:05.00 /usr/local/bin/claude"
	_, err := parseProcessLine(line)
	if err == nil {
		t.Error("expected error for invalid PID")
	}
}

func TestClassifySource(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want model.SourceType
	}{
		{"CLI", "/usr/local/bin/claude --model opus", model.SourceCLI},
		{"VSCode", "/Users/test/.vscode/extensions/anthropic.claude-code/node_modules/claude --model opus", model.SourceVSCode},
		{"VSCode server", "/home/user/.vscode-server/extensions/anthropic.claude-code/bin/claude", model.SourceVSCode},
		{"SDK", "python -m claude_agent_sdk run", model.SourceSDK},
		{"plain CLI", "claude chat", model.SourceCLI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySource(tt.cmd)
			if got != tt.want {
				t.Errorf("classifySource(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestExtractFlag(t *testing.T) {
	tests := []struct {
		name string
		args string
		flag string
		want string
	}{
		{"model", "claude --model opus --resume abc", "--model", "opus"},
		{"permission-mode", "claude --permission-mode full", "--permission-mode", "full"},
		{"resume", "claude --resume sess-123", "--resume", "sess-123"},
		{"missing flag", "claude --model opus", "--resume", ""},
		{"flag at end", "claude --model", "--model", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFlag(tt.args, tt.flag)
			if got != tt.want {
				t.Errorf("extractFlag(%q, %q) = %q, want %q", tt.args, tt.flag, got, tt.want)
			}
		})
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"from --resume", "claude --resume sess-abc-123", "sess-abc-123"},
		{"from --session-id", "claude --session-id sess-xyz", "sess-xyz"},
		{"resume takes priority", "claude --resume sess-abc --session-id sess-xyz", "sess-abc"},
		{"neither", "claude --model opus", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSessionID(tt.args)
			if got != tt.want {
				t.Errorf("extractSessionID(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestIsClaudeProcess(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"claude binary", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 /usr/local/bin/claude --model opus", true},
		{"not claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 /usr/local/bin/vim", false},
		{"grep claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 grep claude", false},
		{"claudetopus", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 claudetopus", false},
		{"tmux claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 tmux new -s claude-proj", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isClaudeProcess(tt.line)
			if got != tt.want {
				t.Errorf("isClaudeProcess(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestBuildInstance(t *testing.T) {
	proc := rawProcess{
		PID:      42,
		MemoryKB: 512000,
		Command:  "/usr/local/bin/claude --model opus --permission-mode full --resume sess-123",
	}

	inst := buildInstance(proc)

	if inst.PID != 42 {
		t.Errorf("PID = %d, want 42", inst.PID)
	}
	if inst.MemoryMB != 500 {
		t.Errorf("MemoryMB = %d, want 500", inst.MemoryMB)
	}
	if inst.Model != "opus" {
		t.Errorf("Model = %q, want %q", inst.Model, "opus")
	}
	if inst.PermissionMode != "full" {
		t.Errorf("PermissionMode = %q, want %q", inst.PermissionMode, "full")
	}
	if inst.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want %q", inst.SessionID, "sess-123")
	}
	if inst.Source != model.SourceCLI {
		t.Errorf("Source = %v, want %v", inst.Source, model.SourceCLI)
	}
	if inst.Status != model.StatusUnknown {
		t.Errorf("Status = %v, want %v", inst.Status, model.StatusUnknown)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
