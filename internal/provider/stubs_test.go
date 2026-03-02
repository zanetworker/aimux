package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

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

func TestGeminiName(t *testing.T) {
	g := &Gemini{}
	if got := g.Name(); got != "gemini" {
		t.Errorf("Gemini.Name() = %q, want %q", got, "gemini")
	}
}

func TestGeminiDiscover(t *testing.T) {
	g := &Gemini{}
	_, err := g.Discover()
	if err != nil {
		t.Errorf("Gemini.Discover() error = %v, want nil", err)
	}
	// Result depends on running processes — just verify no error
}

func TestGeminiResumeCommand(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{SessionID: "test", WorkingDir: "/tmp"})
	if cmd == nil {
		t.Fatal("Gemini.ResumeCommand() returned nil, want non-nil")
	}
	assertArgPresent(t, cmd.Args, "--resume")
	assertArgPresent(t, cmd.Args, "latest")
}

// --- Codex new methods ---

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

// --- Gemini tests ---

func TestGeminiCanEmbed(t *testing.T) {
	g := &Gemini{}
	if g.CanEmbed() {
		t.Error("Gemini.CanEmbed() = true, want false")
	}
}

func TestGeminiDiscover_NoError(t *testing.T) {
	g := &Gemini{}
	_, err := g.Discover()
	if err != nil {
		t.Errorf("Gemini.Discover() error = %v, want nil", err)
	}
}

func TestGeminiResumeCommand_WithWorkingDir(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{WorkingDir: "/tmp/project"})
	if cmd == nil {
		t.Fatal("ResumeCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--resume")
	assertArgPresent(t, cmd.Args, "latest")
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestGeminiResumeCommand_NoWorkingDir(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{})
	if cmd != nil {
		t.Errorf("ResumeCommand with no WorkingDir should return nil, got %v", cmd)
	}
}

func TestGeminiFindSessionFile_NoWorkingDir(t *testing.T) {
	g := &Gemini{}
	a := agent.Agent{SessionID: "test"}
	if got := g.FindSessionFile(a); got != "" {
		t.Errorf("Gemini.FindSessionFile(no WorkingDir) = %q, want empty", got)
	}
}

func TestGeminiRecentDirs_NoPanic(t *testing.T) {
	g := &Gemini{}
	_ = g.RecentDirs(5)
}

func TestGeminiSpawnCommand_Default(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	if base := filepath.Base(cmd.Args[0]); base != "gemini" {
		t.Errorf("binary = %q, want %q", base, "gemini")
	}
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
}

func TestGeminiSpawnCommand_DefaultModelSkipped(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "default", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgAbsent(t, cmd.Args, "--model")
}

func TestGeminiSpawnCommand_WithModel(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "gemini-2.5-pro", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gemini-2.5-pro")
}

func TestGeminiSpawnCommand_Yolo(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "", "yolo")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--yolo")
}

func TestGeminiSpawnCommand_Plan(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "", "plan")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--approval-mode", "plan")
}

func TestGeminiSpawnCommand_ModelAndYolo(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "gemini-3-pro", "yolo")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gemini-3-pro")
	assertArgPresent(t, cmd.Args, "--yolo")
}

func TestGeminiSpawnArgs(t *testing.T) {
	g := &Gemini{}
	sa := g.SpawnArgs()

	expectedModels := []string{"default", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-3-pro", "gemini-3.1-flash"}
	if len(sa.Models) != len(expectedModels) {
		t.Fatalf("SpawnArgs.Models = %v, want %v", sa.Models, expectedModels)
	}
	for i, m := range expectedModels {
		if sa.Models[i] != m {
			t.Errorf("SpawnArgs.Models[%d] = %q, want %q", i, sa.Models[i], m)
		}
	}

	expectedModes := []string{"default", "yolo", "auto_edit", "plan", "sandbox"}
	if len(sa.Modes) != len(expectedModes) {
		t.Fatalf("SpawnArgs.Modes = %v, want %v", sa.Modes, expectedModes)
	}
	for i, m := range expectedModes {
		if sa.Modes[i] != m {
			t.Errorf("SpawnArgs.Modes[%d] = %q, want %q", i, sa.Modes[i], m)
		}
	}
}

func TestGeminiExtractFlag(t *testing.T) {
	if got := geminiExtractFlag("gemini --model gemini-2.5-pro --yolo", "--model"); got != "gemini-2.5-pro" {
		t.Errorf("geminiExtractFlag(--model) = %q, want %q", got, "gemini-2.5-pro")
	}
	if got := geminiExtractFlag("gemini --yolo", "--model"); got != "" {
		t.Errorf("geminiExtractFlag(missing) = %q, want empty", got)
	}
}

func TestIsGeminiProcess(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 /opt/homebrew/bin/gemini", true},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 node /path/to/gemini --model x", true},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 grep gemini", false},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 aimux gemini", false},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 /usr/bin/python3 something", false},
	}
	for _, tt := range tests {
		if got := isGeminiProcess(tt.line); got != tt.want {
			t.Errorf("isGeminiProcess(%q) = %v, want %v", tt.line[:40], got, tt.want)
		}
	}
}

func TestNewestSessionJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty dir
	path, mod := newestSessionJSON(tmpDir)
	if path != "" {
		t.Errorf("newestSessionJSON(empty) = %q, want empty", path)
	}
	if !mod.IsZero() {
		t.Errorf("newestSessionJSON(empty) time should be zero")
	}

	// Create session files
	s1 := `{"sessionId":"aaa","startTime":"2026-02-28T09:00:00Z","lastUpdated":"2026-02-28T09:10:00Z","messages":[]}`
	s2 := `{"sessionId":"bbb","startTime":"2026-02-28T10:00:00Z","lastUpdated":"2026-02-28T10:30:00Z","messages":[]}`

	f1 := filepath.Join(tmpDir, "session-2026-02-28T09-00-aaa.json")
	f2 := filepath.Join(tmpDir, "session-2026-02-28T10-00-bbb.json")
	os.WriteFile(f1, []byte(s1), 0o644)
	os.WriteFile(f2, []byte(s2), 0o644)
	// Ensure f2 has a newer mod time (filesystem resolution may be 1s on Linux)
	past := time.Now().Add(-10 * time.Second)
	os.Chtimes(f1, past, past)
	// f2 keeps current mod time (newer)

	path, mod = newestSessionJSON(tmpDir)
	if path == "" {
		t.Fatal("newestSessionJSON should find a session")
	}
	if !strings.Contains(path, "bbb") {
		t.Errorf("expected newest session (bbb), got %q", filepath.Base(path))
	}
	if mod.IsZero() {
		t.Error("mod time should not be zero")
	}
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

// --- Gemini ParseTrace tests ---

func TestGeminiParseTrace_BasicTurn(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"hello gemini","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"model","message":"Hi there! How can I help?","timestamp":"2026-01-01T10:00:05Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
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
	if len(turn.UserLines) == 0 || turn.UserLines[0] != "hello gemini" {
		t.Errorf("UserLines = %v, want [\"hello gemini\"]", turn.UserLines)
	}
	if len(turn.OutputLines) == 0 || turn.OutputLines[0] != "Hi there! How can I help?" {
		t.Errorf("OutputLines = %v, want [\"Hi there! How can I help?\"]", turn.OutputLines)
	}
}

func TestGeminiParseTrace_MultipleTurns(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"first question","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"model","message":"first answer","timestamp":"2026-01-01T10:00:05Z"},
		{"sessionId":"s1","messageId":3,"type":"user","message":"second question","timestamp":"2026-01-01T10:01:00Z"},
		{"sessionId":"s1","messageId":4,"type":"model","message":"second answer","timestamp":"2026-01-01T10:01:05Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "first question" {
		t.Errorf("turn 1 UserLines[0] = %q, want %q", turns[0].UserLines[0], "first question")
	}
	if turns[1].UserLines[0] != "second question" {
		t.Errorf("turn 2 UserLines[0] = %q, want %q", turns[1].UserLines[0], "second question")
	}
}

func TestGeminiParseTrace_InfoMessage(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"what is the status?","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"info","message":"session started","timestamp":"2026-01-01T10:00:01Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if len(turns[0].OutputLines) == 0 {
		t.Fatal("expected info message in OutputLines")
	}
	if !strings.Contains(turns[0].OutputLines[0], "[info]") {
		t.Errorf("OutputLines[0] = %q, want to contain [info]", turns[0].OutputLines[0])
	}
}

func TestGeminiParseTrace_MissingFile(t *testing.T) {
	g := &Gemini{}
	_, err := g.ParseTrace("/nonexistent/path/logs.json")
	if err == nil {
		t.Error("ParseTrace on missing file should return error")
	}
}

func TestGeminiParseTrace_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace should not error on invalid JSON, got: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for invalid JSON, got %d", len(turns))
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

// --- Gemini new mode tests ---

func TestGeminiSpawnCommand_AutoEdit(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/p", "", "auto_edit")
	assertArgsContain(t, cmd.Args, "--approval-mode", "auto_edit")
}

func TestGeminiSpawnCommand_Sandbox(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/p", "", "sandbox")
	assertArgPresent(t, cmd.Args, "--sandbox")
}

func TestGeminiOTELEnv(t *testing.T) {
	g := &Gemini{}
	env := g.OTELEnv("http://localhost:4318")

	required := []string{
		"GEMINI_CLI_TELEMETRY_ENABLED=true",
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

// Suppress unused variable warnings for time import.
var _ = time.Now

// Verify stubs implement the Provider interface at compile time.
var _ Provider = (*Codex)(nil)
var _ Provider = (*Gemini)(nil)
