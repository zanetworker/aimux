package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

// Verify Codex implements the Provider interface at compile time.
var _ Provider = (*Codex)(nil)

func TestCodexName(t *testing.T) {
	c := &Codex{}
	if got := c.Name(); got != "codex" {
		t.Errorf("Codex.Name() = %q, want %q", got, "codex")
	}
}

func TestCodexDiscover(t *testing.T) {
	c := &Codex{}
	_, err := c.Discover()
	if err != nil {
		t.Errorf("Codex.Discover() error = %v, want nil", err)
	}
	// Codex now does real discovery; result depends on running processes
}

func TestCodexDiscoverWithTmux(t *testing.T) {
	// Verify that Discover() works correctly with tmux session matching.
	// Even if tmux is not installed or has no sessions, Discover() should
	// not error — ListTmuxSessions() returns nil gracefully.
	c := &Codex{}
	agents, err := c.Discover()
	if err != nil {
		t.Fatalf("Codex.Discover() with tmux matching error = %v, want nil", err)
	}
	// If any agents were discovered, verify the TMuxSession field is a string
	// (may be empty if no matching tmux session exists).
	for _, a := range agents {
		_ = a.TMuxSession // access the field to ensure it's set without panic
	}
}

func TestCodexResumeCommand(t *testing.T) {
	c := &Codex{}
	cmd := c.ResumeCommand(agent.Agent{SessionID: "test-session", WorkingDir: "/tmp"})
	if cmd == nil {
		t.Skip("codex binary not found")
	}
	// Should produce: codex resume --no-alt-screen <session-id>
	args := cmd.Args
	if len(args) < 4 || args[1] != "resume" || args[2] != "--no-alt-screen" || args[3] != "test-session" {
		t.Errorf("Codex.ResumeCommand() args = %v, want [codex resume --no-alt-screen test-session]", args)
	}
}

func TestCodexCanEmbed(t *testing.T) {
	c := &Codex{}
	if c.CanEmbed() {
		t.Error("Codex.CanEmbed() = true, want false")
	}
}

func TestCodexFindSessionFile_NoWorkingDir(t *testing.T) {
	c := &Codex{}
	a := agent.Agent{SessionID: "some-id"}
	if got := c.FindSessionFile(a); got != "" {
		t.Errorf("FindSessionFile(no WorkingDir) = %q, want empty", got)
	}
}

func TestCodexSpawnCommand_Default(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/myproject", "", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	assertArgPresent(t, cmd.Args, "--no-alt-screen")
	assertArgsContain(t, cmd.Args, "--sandbox", "workspace-write")
	if base := filepath.Base(cmd.Args[0]); base != "codex" {
		t.Errorf("binary = %q, want %q", base, "codex")
	}
}

func TestCodexSpawnCommand_DefaultMode(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/myproject", "", "default")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--no-alt-screen")
	assertArgsContain(t, cmd.Args, "--sandbox", "workspace-write")
}

func TestCodexSpawnCommand_FullAuto(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/myproject", "", "full-auto")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--no-alt-screen")
	assertArgPresent(t, cmd.Args, "--full-auto")
	assertArgAbsent(t, cmd.Args, "--sandbox")
}

func TestCodexSpawnCommand_WithModel(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/myproject", "o3", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "o3")
}

func TestCodexSpawnCommand_DefaultModelSkipped(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/myproject", "default", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	for _, a := range cmd.Args {
		if a == "--model" {
			t.Error("SpawnCommand with model='default' should not produce --model flag")
		}
	}
}

func TestCodexSpawnArgs(t *testing.T) {
	c := &Codex{}
	sa := c.SpawnArgs()
	expectedModels := []string{"default", "o3", "o4-mini"}
	expectedModes := []string{"default", "full-auto", "full-access", "read-only"}

	if len(sa.Models) != len(expectedModels) {
		t.Fatalf("SpawnArgs.Models length = %d, want %d", len(sa.Models), len(expectedModels))
	}
	for i, m := range expectedModels {
		if sa.Models[i] != m {
			t.Errorf("SpawnArgs.Models[%d] = %q, want %q", i, sa.Models[i], m)
		}
	}

	if len(sa.Modes) != len(expectedModes) {
		t.Fatalf("SpawnArgs.Modes length = %d, want %d", len(sa.Modes), len(expectedModes))
	}
	for i, m := range expectedModes {
		if sa.Modes[i] != m {
			t.Errorf("SpawnArgs.Modes[%d] = %q, want %q", i, sa.Modes[i], m)
		}
	}
}

func TestCodexRecentDirs_NoHome(t *testing.T) {
	// Just verify it doesn't panic when called
	c := &Codex{}
	_ = c.RecentDirs(5)
}

// --- Helper tests ---

func TestExtractCodexCWD_Provider(t *testing.T) {
	tmpDir := t.TempDir()

	meta := map[string]string{
		"type": "session_meta",
		"cwd":  "/home/user/project",
	}
	metaJSON, _ := json.Marshal(meta)
	content := append(metaJSON, '\n')
	content = append(content, []byte(`{"type":"message","text":"hello"}`+"\n")...)

	path := filepath.Join(tmpDir, "session.jsonl")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got := extractCodexCWD(path)
	if got != "/home/user/project" {
		t.Errorf("extractCodexCWD = %q, want %q", got, "/home/user/project")
	}
}

func TestExtractCodexCWD_NoCWD(t *testing.T) {
	tmpDir := t.TempDir()

	path := filepath.Join(tmpDir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := extractCodexCWD(path)
	if got != "" {
		t.Errorf("extractCodexCWD = %q, want empty string", got)
	}
}

func TestExtractCodexCWD_MissingFile(t *testing.T) {
	got := extractCodexCWD("/nonexistent/path/session.jsonl")
	if got != "" {
		t.Errorf("extractCodexCWD on missing file = %q, want empty", got)
	}
}

func TestCodexParseSessionTokens(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	lines := []string{
		`{"timestamp":"2026-02-28T09:37:29.758Z","type":"session_meta","payload":{"id":"test-123","cwd":"/tmp/project"}}`,
		`{"timestamp":"2026-02-28T09:38:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":50000,"cached_input_tokens":3000,"output_tokens":1200,"total_tokens":51200}}}}`,
		`{"timestamp":"2026-02-28T09:39:00.000Z","model":"o3","type":"response"}`,
	}
	content := []byte(strings.Join(lines, "\n") + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Codex{}
	info := c.parseSession(path)

	if info.sessionID != "test-123" {
		t.Errorf("sessionID = %q, want %q", info.sessionID, "test-123")
	}
	if info.cwd != "/tmp/project" {
		t.Errorf("cwd = %q, want %q", info.cwd, "/tmp/project")
	}
	if info.tokensIn != 50000 {
		t.Errorf("tokensIn = %d, want %d", info.tokensIn, 50000)
	}
	if info.tokensOut != 1200 {
		t.Errorf("tokensOut = %d, want %d", info.tokensOut, 1200)
	}
	if info.cachedIn != 3000 {
		t.Errorf("cachedIn = %d, want %d", info.cachedIn, 3000)
	}
	if info.model != "o3" {
		t.Errorf("model = %q, want %q", info.model, "o3")
	}
}

func TestCodexParseSession_NoTokens(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	content := `{"timestamp":"2026-02-28T09:37:29.758Z","type":"session_meta","payload":{"id":"test-456","cwd":"/tmp"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Codex{}
	info := c.parseSession(path)

	if info.sessionID != "test-456" {
		t.Errorf("sessionID = %q, want %q", info.sessionID, "test-456")
	}
	if info.tokensIn != 0 {
		t.Errorf("tokensIn = %d, want 0", info.tokensIn)
	}
	if info.tokensOut != 0 {
		t.Errorf("tokensOut = %d, want 0", info.tokensOut)
	}
}

// --- Codex ParseTrace tests ---

func TestCodexParseTrace_BasicTurn(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"abc-123","cwd":"/tmp/test"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"fix the bug"}}
{"timestamp":"2026-01-01T10:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll look at it."}]}}
{"timestamp":"2026-01-01T10:00:03Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"cat main.go\"}"}}
{"timestamp":"2026-01-01T10:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"file contents here"}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Codex{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("ParseTrace returned %d turns, want 1", len(turns))
	}

	turn := turns[0]
	if turn.Number != 1 {
		t.Errorf("Number = %d, want 1", turn.Number)
	}
	if len(turn.UserLines) == 0 || turn.UserLines[0] != "fix the bug" {
		t.Errorf("UserLines = %v, want [\"fix the bug\"]", turn.UserLines)
	}
	if len(turn.Actions) != 1 {
		t.Fatalf("Actions has %d entries, want 1", len(turn.Actions))
	}
	if turn.Actions[0].Name != "Bash" {
		t.Errorf("Actions[0].Name = %q, want %q (mapped from exec_command)", turn.Actions[0].Name, "Bash")
	}
	if !strings.Contains(turn.Actions[0].Snippet, "cat main.go") {
		t.Errorf("Actions[0].Snippet = %q, should contain %q", turn.Actions[0].Snippet, "cat main.go")
	}
	if len(turn.OutputLines) == 0 || turn.OutputLines[0] != "I'll look at it." {
		t.Errorf("OutputLines = %v, want [\"I'll look at it.\"]", turn.OutputLines)
	}
}

func TestCodexParseTrace_FunctionCallError(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"test"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"run tests"}}
{"timestamp":"2026-01-01T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"go test\"}"}}
{"timestamp":"2026-01-01T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Process exited with code 1\nerror in test"}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Codex{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if len(turns[0].Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(turns[0].Actions))
	}
	action := turns[0].Actions[0]
	if action.Success {
		t.Error("expected action.Success = false for error output")
	}
	if action.ErrorMsg == "" {
		t.Error("expected non-empty ErrorMsg for error output")
	}
}

func TestCodexParseTrace_MissingFile(t *testing.T) {
	c := &Codex{}
	_, err := c.ParseTrace("/nonexistent/path/session.jsonl")
	if err == nil {
		t.Error("ParseTrace on missing file should return error")
	}
}

func TestCodexParseTrace_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Codex{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for empty file, got %d", len(turns))
	}
}

func TestCodexParseTrace_ToolNameMapping(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"test"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"read a file"}}
{"timestamp":"2026-01-01T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"read_file","call_id":"c1","arguments":"{\"file_path\":\"main.go\"}"}}
{"timestamp":"2026-01-01T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"package main"}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Codex{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 || len(turns[0].Actions) != 1 {
		t.Fatalf("expected 1 turn with 1 action")
	}
	if turns[0].Actions[0].Name != "Read" {
		t.Errorf("tool name mapping: got %q, want %q", turns[0].Actions[0].Name, "Read")
	}
}

// --- codexToolName tests (moved from views) ---

func TestCodexToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"exec_command", "Bash"},
		{"shell", "Bash"},
		{"read_file", "Read"},
		{"write_file", "Write"},
		{"apply_patch", "Edit"},
		{"edit_file", "Edit"},
		{"search_files", "Grep"},
		{"grep", "Grep"},
		{"list_directory", "Ls"},
		{"ls", "Ls"},
		{"unknown_short", "unknown_shor"}, // truncated to 12
		{"tiny", "tiny"},                  // short enough, no truncation
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := codexToolName(tt.input)
			if got != tt.want {
				t.Errorf("codexToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- Codex new mode tests ---

func TestCodexSpawnCommand_FullAccess(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/p", "", "full-access")
	assertArgsContain(t, cmd.Args, "--sandbox", "danger-full-access")
	assertArgsContain(t, cmd.Args, "--ask-for-approval", "never")
}

func TestCodexSpawnCommand_ReadOnly(t *testing.T) {
	c := &Codex{}
	cmd := c.SpawnCommand("/tmp/p", "", "read-only")
	assertArgsContain(t, cmd.Args, "--sandbox", "read-only")
}

func TestCodexParseTrace_PreservesIndentation(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "codex.jsonl")

	data := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"test"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"show code"}}
{"timestamp":"2026-01-01T10:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Here:\n` + "```" + `\nfunc main() {\n    fmt.Println(\"hi\")\n}\n` + "```" + `"}]}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	cx := &Codex{}
	turns, err := cx.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}

	found := false
	for _, line := range turns[0].OutputLines {
		if strings.HasPrefix(line, "    ") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected indented line, got: %v", turns[0].OutputLines)
	}
}

func TestCodexOTELEnv(t *testing.T) {
	c := &Codex{}
	env := c.OTELEnv("http://localhost:4318")

	required := []string{
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"OTEL_LOGS_EXPORTER=otlp",
	}
	for _, r := range required {
		if !strings.Contains(env, r) {
			t.Errorf("OTELEnv missing %q, got:\n%s", r, env)
		}
	}
}
