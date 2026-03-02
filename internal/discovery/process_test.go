package discovery

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
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
		want agent.SourceType
	}{
		{"CLI", "/usr/local/bin/claude --model opus", agent.SourceCLI},
		{"VSCode", "/Users/test/.vscode/extensions/anthropic.claude-code/node_modules/claude --model opus", agent.SourceVSCode},
		{"VSCode server", "/home/user/.vscode-server/extensions/anthropic.claude-code/bin/claude", agent.SourceVSCode},
		{"SDK", "python -m claude_agent_sdk run", agent.SourceSDK},
		{"plain CLI", "claude chat", agent.SourceCLI},
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
		{"bare claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 claude --dangerously-skip-permissions", true},
		{"vscode claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 /Users/test/.vscode/extensions/anthropic.claude-code-2.1.49-darwin-arm64/resources/native-binary/claude --output-format stream-json", true},
		{"not claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 /usr/local/bin/vim", false},
		{"grep claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 grep claude", false},
		{"aimux", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 aimux", false},
		{"tmux claude", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 tmux new -s claude-proj", false},
		{"chrome helper", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 /Applications/Claude.app/Contents/Helpers/chrome-native-host chrome-extension://abc", false},
		{"zsh subprocess", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 /bin/zsh -c -l source /Users/test/.claude/shell-snapshots/snapshot.sh", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isClaudeProcess(tt.line)
			if got != tt.want {
				t.Errorf("isClaudeProcess(%q) = %v, want %v", tt.line[:60], got, tt.want)
			}
		})
	}
}

func TestBuildInstance(t *testing.T) {
	proc := rawProcess{
		PID:      42,
		MemoryKB: 512000,
		Command:  "/usr/local/bin/claude --model opus --permission-mode plan --resume sess-123",
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
	if inst.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want %q", inst.PermissionMode, "plan")
	}
	if inst.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want %q", inst.SessionID, "sess-123")
	}
	if inst.Source != agent.SourceCLI {
		t.Errorf("Source = %v, want %v", inst.Source, agent.SourceCLI)
	}
}

func TestBuildInstanceBypass(t *testing.T) {
	proc := rawProcess{
		PID:      99,
		MemoryKB: 200000,
		Command:  "claude --dangerously-skip-permissions",
	}
	inst := buildInstance(proc)
	if inst.PermissionMode != "bypass" {
		t.Errorf("PermissionMode = %q, want %q", inst.PermissionMode, "bypass")
	}
}

func TestBuildInstanceDefaultPerm(t *testing.T) {
	proc := rawProcess{
		PID:      88,
		MemoryKB: 100000,
		Command:  "claude",
	}
	inst := buildInstance(proc)
	if inst.PermissionMode != "default" {
		t.Errorf("PermissionMode = %q, want %q", inst.PermissionMode, "default")
	}
}

// --- filterSubagents tests ---

// mockParentPID returns a function that maps PIDs to parent PIDs based on
// the provided mapping. Unknown PIDs return 0.
func mockParentPID(mapping map[int]int) func(int) int {
	return func(pid int) int {
		return mapping[pid]
	}
}

func TestFilterSubagentsRemovesChildProcesses(t *testing.T) {
	// PID 100 is the parent, PID 200 is a subagent of 100.
	original := getParentPID
	getParentPID = mockParentPID(map[int]int{
		100: 1,   // parent is init/launchd
		200: 100, // parent is PID 100 (a Claude process)
	})
	defer func() { getParentPID = original }()

	agents := []agent.Agent{
		{PID: 100, Model: "opus"},
		{PID: 200, Model: "opus"},
	}

	filtered := filterSubagents(agents)

	if len(filtered) != 1 {
		t.Fatalf("filterSubagents() returned %d agents, want 1", len(filtered))
	}
	if filtered[0].PID != 100 {
		t.Errorf("remaining PID = %d, want 100", filtered[0].PID)
	}
}

func TestFilterSubagentsKeepsIndependentProcesses(t *testing.T) {
	original := getParentPID
	getParentPID = mockParentPID(map[int]int{
		100: 1, // parent is init
		200: 1, // parent is init (independent)
		300: 1, // parent is init (independent)
	})
	defer func() { getParentPID = original }()

	agents := []agent.Agent{
		{PID: 100, Model: "opus"},
		{PID: 200, Model: "sonnet"},
		{PID: 300, Model: "haiku"},
	}

	filtered := filterSubagents(agents)

	if len(filtered) != 3 {
		t.Errorf("filterSubagents() returned %d agents, want 3", len(filtered))
	}
}

func TestFilterSubagentsEmptyList(t *testing.T) {
	filtered := filterSubagents(nil)
	if len(filtered) != 0 {
		t.Errorf("filterSubagents(nil) returned %d agents, want 0", len(filtered))
	}
}

func TestFilterSubagentsSingleAgent(t *testing.T) {
	agents := []agent.Agent{{PID: 42}}
	filtered := filterSubagents(agents)
	if len(filtered) != 1 {
		t.Errorf("filterSubagents() returned %d agents, want 1", len(filtered))
	}
}

func TestFilterSubagentsMultipleSubagents(t *testing.T) {
	// PID 100 is the parent, PIDs 200 and 300 are both subagents of 100.
	original := getParentPID
	getParentPID = mockParentPID(map[int]int{
		100: 1,
		200: 100,
		300: 100,
	})
	defer func() { getParentPID = original }()

	agents := []agent.Agent{
		{PID: 100},
		{PID: 200},
		{PID: 300},
	}

	filtered := filterSubagents(agents)

	if len(filtered) != 1 {
		t.Fatalf("filterSubagents() returned %d agents, want 1", len(filtered))
	}
	if filtered[0].PID != 100 {
		t.Errorf("remaining PID = %d, want 100", filtered[0].PID)
	}
}

func TestFilterSubagentsParentPIDNotInList(t *testing.T) {
	// PID 200 has parent PID 999, but 999 is not in the agent list.
	// So 200 should NOT be filtered out.
	original := getParentPID
	getParentPID = mockParentPID(map[int]int{
		100: 1,
		200: 999, // parent not in agent list
	})
	defer func() { getParentPID = original }()

	agents := []agent.Agent{
		{PID: 100},
		{PID: 200},
	}

	filtered := filterSubagents(agents)

	if len(filtered) != 2 {
		t.Errorf("filterSubagents() returned %d agents, want 2 (parent not in list)", len(filtered))
	}
}

func TestFilterSubagentsMultiLevelAncestor(t *testing.T) {
	// PID 100 is the parent claude. PID 300 is a subagent spawned via
	// claude (100) → node (200) → claude (300). The intermediate node
	// process (200) is NOT in the Claude PID set, but 300 should still
	// be filtered because its grandparent (100) is a Claude process.
	original := getParentPID
	getParentPID = mockParentPID(map[int]int{
		100: 1,   // parent is init
		200: 100, // node wrapper, child of claude
		300: 200, // subagent claude, child of node
	})
	defer func() { getParentPID = original }()

	agents := []agent.Agent{
		{PID: 100},
		{PID: 300},
	}

	filtered := filterSubagents(agents)

	if len(filtered) != 1 {
		t.Fatalf("filterSubagents() returned %d agents, want 1", len(filtered))
	}
	if filtered[0].PID != 100 {
		t.Errorf("remaining PID = %d, want 100", filtered[0].PID)
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
