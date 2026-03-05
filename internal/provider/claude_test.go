package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestClaudeName(t *testing.T) {
	c := &Claude{}
	if got := c.Name(); got != "claude" {
		t.Errorf("Claude.Name() = %q, want %q", got, "claude")
	}
}

func TestClaudeResumeCommandWithSessionID(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{
		SessionID:  "abc-123",
		WorkingDir: "/tmp/project",
	}
	cmd := c.ResumeCommand(a)
	if cmd == nil {
		t.Fatal("ResumeCommand returned nil, want non-nil")
	}

	args := cmd.Args
	// args[0] is the binary path, args[1:] are the flags
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "--resume" {
		t.Errorf("args[1] = %q, want %q", args[1], "--resume")
	}
	if args[2] != "abc-123" {
		t.Errorf("args[2] = %q, want %q", args[2], "abc-123")
	}
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestClaudeResumeCommandWithWorkingDirOnly(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{
		WorkingDir: "/tmp/project",
	}
	cmd := c.ResumeCommand(a)
	if cmd == nil {
		t.Fatal("ResumeCommand returned nil, want non-nil")
	}

	args := cmd.Args
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %d: %v", len(args), args)
	}
	if args[1] != "--continue" {
		t.Errorf("args[1] = %q, want %q", args[1], "--continue")
	}
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestClaudeResumeCommandWithNothing(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{}
	cmd := c.ResumeCommand(a)
	if cmd != nil {
		t.Errorf("ResumeCommand returned %v, want nil", cmd)
	}
}

func TestClaudeCanEmbed(t *testing.T) {
	c := &Claude{}
	if !c.CanEmbed() {
		t.Error("Claude.CanEmbed() = false, want true")
	}
}

func TestClaudeFindSessionFile_NoSessionNoDir(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{}
	if got := c.FindSessionFile(a); got != "" {
		t.Errorf("FindSessionFile(empty agent) = %q, want empty", got)
	}
}

func TestClaudeSpawnCommand_Default(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/myproject", "", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	// Default: no model, no mode flags — just the binary
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
	if base := filepath.Base(cmd.Args[0]); base != "claude" {
		t.Errorf("binary = %q, want %q", base, "claude")
	}
}

func TestClaudeSpawnCommand_DefaultModelSkipped(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/myproject", "default", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	// "default" model should not produce --model flag
	for _, a := range cmd.Args {
		if a == "--model" {
			t.Error("SpawnCommand with model='default' should not produce --model flag")
		}
	}
}

func TestClaudeSpawnCommand_WithModel(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/myproject", "opus", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "opus")
}

func TestClaudeSpawnCommand_Bypass(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/myproject", "", "bypass")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--dangerously-skip-permissions")
}

func TestClaudeSpawnCommand_Plan(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/myproject", "", "plan")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--permission-mode", "plan")
}

func TestClaudeSpawnCommand_ModelAndBypass(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/myproject", "sonnet", "bypass")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "sonnet")
	assertArgPresent(t, cmd.Args, "--dangerously-skip-permissions")
}

func TestClaudeSpawnArgs(t *testing.T) {
	c := &Claude{}
	sa := c.SpawnArgs()
	expectedModels := []string{"default", "opus", "sonnet", "haiku"}
	expectedModes := []string{"default", "plan", "acceptEdits", "bypass", "dontAsk"}

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

func TestClaudeRecentDirs_EmptyDir(t *testing.T) {
	// Override home to a temp dir to avoid reading real ~/.claude
	// Since RecentDirs reads os.UserHomeDir(), we test indirectly by
	// ensuring the code doesn't panic on missing dirs.
	c := &Claude{}
	// This will try to read the real home dir, which may or may not
	// have Claude data. Just verify it doesn't panic or error.
	_ = c.RecentDirs(5)
}

// assertArgPresent checks that a flag is present anywhere in the args slice.
func assertArgPresent(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("expected flag %q in args %v", flag, args)
}

// assertArgAbsent checks that a flag is NOT present in the args slice.
func assertArgAbsent(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			t.Errorf("unexpected flag %q in args %v", flag, args)
			return
		}
	}
}

// assertArgsContain checks that flag and its value appear consecutively in args.
func assertArgsContain(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Errorf("expected %q %q in args %v", flag, value, args)
}

func TestNewestFileModTime(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty directory — should return zero time
	got := newestFileModTime(tmpDir, "*.jsonl")
	if !got.IsZero() {
		t.Errorf("newestFileModTime on empty dir = %v, want zero", got)
	}

	// Create two files, check newest is returned
	f1 := filepath.Join(tmpDir, "old.jsonl")
	if err := os.WriteFile(f1, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make it older
	oldTime := got.Add(-1 * 24 * 60 * 60 * 1000000000) // doesn't matter, just needs to be older
	_ = os.Chtimes(f1, oldTime, oldTime)

	f2 := filepath.Join(tmpDir, "new.jsonl")
	if err := os.WriteFile(f2, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got = newestFileModTime(tmpDir, "*.jsonl")
	if got.IsZero() {
		t.Error("newestFileModTime should not be zero with files present")
	}

	// Verify it picks up the pattern filter
	got = newestFileModTime(tmpDir, "*.txt")
	if !got.IsZero() {
		t.Errorf("newestFileModTime with *.txt = %v, want zero (no .txt files)", got)
	}
}

// --- discoverRecentSessions tests ---

func TestDiscoverRecentSessions_EmptyDir(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	// No subdirectories at all
	agents := c.discoverRecentSessions(nil, projectsDir)
	if len(agents) != 0 {
		t.Errorf("discoverRecentSessions on empty dir returned %d agents, want 0", len(agents))
	}
}

func TestDiscoverRecentSessions_NoRecentFiles(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	// Create a subdir with an old .jsonl file (>24h)
	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(subdir, "session-old.jsonl")
	writeTestSession(t, sessionFile, "old-session-id", "claude-sonnet-4-5")

	// Backdate the file to 48 hours ago
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(sessionFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	agents := c.discoverRecentSessions(nil, projectsDir)
	if len(agents) != 0 {
		t.Errorf("discoverRecentSessions with only old files returned %d agents, want 0", len(agents))
	}
}

func TestDiscoverRecentSessions_FindsRecentSession(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	// Create a subdir with a recent .jsonl file
	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(subdir, "test-session-123.jsonl")
	writeTestSession(t, sessionFile, "test-session-123", "claude-sonnet-4-5")

	agents := c.discoverRecentSessions(nil, projectsDir)
	if len(agents) != 1 {
		t.Fatalf("discoverRecentSessions returned %d agents, want 1", len(agents))
	}

	a := agents[0]
	if a.PID != 0 {
		t.Errorf("agent PID = %d, want 0", a.PID)
	}
	if a.Status != agent.StatusIdle {
		t.Errorf("agent Status = %v, want StatusIdle", a.Status)
	}
	if a.ProviderName != "claude" {
		t.Errorf("agent ProviderName = %q, want %q", a.ProviderName, "claude")
	}
	if a.Source != agent.SourceCLI {
		t.Errorf("agent Source = %v, want SourceCLI", a.Source)
	}
	if a.SessionID != "test-session-123" {
		t.Errorf("agent SessionID = %q, want %q", a.SessionID, "test-session-123")
	}
	if a.SessionFile != sessionFile {
		t.Errorf("agent SessionFile = %q, want %q", a.SessionFile, sessionFile)
	}
	if a.Model != "claude-sonnet-4-5" {
		t.Errorf("agent Model = %q, want %q", a.Model, "claude-sonnet-4-5")
	}
	if a.Name != "myproject" {
		t.Errorf("agent Name = %q, want %q", a.Name, "myproject")
	}
	if a.WorkingDir != "/tmp/myproject" {
		t.Errorf("agent WorkingDir = %q, want %q", a.WorkingDir, "/tmp/myproject")
	}
}

func TestDiscoverRecentSessions_SkipsRunningBySessionID(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(subdir, "running-session.jsonl")
	writeTestSession(t, sessionFile, "running-session-id", "claude-sonnet-4-5")

	// Simulate a running agent with the same session ID
	running := []agent.Agent{
		{
			PID:       42,
			SessionID: "running-session-id",
		},
	}

	agents := c.discoverRecentSessions(running, projectsDir)
	if len(agents) != 0 {
		t.Errorf("discoverRecentSessions should skip session matching running ID, got %d agents", len(agents))
	}
}

func TestDiscoverRecentSessions_SkipsRunningByWorkingDir(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(subdir, "session-abc.jsonl")
	writeTestSession(t, sessionFile, "session-abc", "claude-sonnet-4-5")

	// Simulate a running agent in the same decoded working directory
	running := []agent.Agent{
		{
			PID:        42,
			WorkingDir: "/tmp/myproject",
		},
	}

	agents := c.discoverRecentSessions(running, projectsDir)
	if len(agents) != 0 {
		t.Errorf("discoverRecentSessions should skip session matching running WorkingDir, got %d agents", len(agents))
	}
}

func TestDiscoverRecentSessions_SkipsNonJSONL(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a non-JSONL file
	txtFile := filepath.Join(subdir, "notes.txt")
	if err := os.WriteFile(txtFile, []byte("not a session"), 0o644); err != nil {
		t.Fatal(err)
	}

	agents := c.discoverRecentSessions(nil, projectsDir)
	if len(agents) != 0 {
		t.Errorf("discoverRecentSessions should skip non-JSONL files, got %d agents", len(agents))
	}
}

func TestDiscoverRecentSessions_OnePerProjectDir(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestSession(t, filepath.Join(subdir, "session-1.jsonl"), "session-1", "claude-opus-4-6")
	writeTestSession(t, filepath.Join(subdir, "session-2.jsonl"), "session-2", "claude-sonnet-4-5")

	agents := c.discoverRecentSessions(nil, projectsDir)
	// Should return only 1 entry per project directory (the newest session)
	if len(agents) != 1 {
		t.Fatalf("discoverRecentSessions returned %d agents, want 1 (one per project dir)", len(agents))
	}

	if agents[0].WorkingDir != "/tmp/myproject" {
		t.Errorf("agent WorkingDir = %q, want %q", agents[0].WorkingDir, "/tmp/myproject")
	}
}

func TestDiscoverRecentSessions_NonexistentDir(t *testing.T) {
	c := &Claude{}
	agents := c.discoverRecentSessions(nil, "/nonexistent/path/that/does/not/exist")
	if len(agents) != 0 {
		t.Errorf("discoverRecentSessions on nonexistent dir returned %d agents, want 0", len(agents))
	}
}

func TestDiscoverRecentSessions_TokensAndCost(t *testing.T) {
	c := &Claude{}
	projectsDir := t.TempDir()

	subdir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(subdir, "session-cost.jsonl")
	writeTestSessionWithTokens(t, sessionFile, "session-cost", "claude-sonnet-4-5", 1000, 500)

	agents := c.discoverRecentSessions(nil, projectsDir)
	if len(agents) != 1 {
		t.Fatalf("discoverRecentSessions returned %d agents, want 1", len(agents))
	}

	a := agents[0]
	if a.TokensIn != 1000 {
		t.Errorf("agent TokensIn = %d, want 1000", a.TokensIn)
	}
	if a.TokensOut != 500 {
		t.Errorf("agent TokensOut = %d, want 500", a.TokensOut)
	}
	if a.EstCostUSD <= 0 {
		t.Errorf("agent EstCostUSD = %f, want > 0", a.EstCostUSD)
	}
}

// --- decodeDirKey tests ---

func TestDecodeDirKey_Standard(t *testing.T) {
	got := decodeDirKey("-Users-azaalouk-go-src-project")
	want := "/Users/azaalouk/go/src/project"
	if got != want {
		t.Errorf("decodeDirKey(%q) = %q, want %q", "-Users-azaalouk-go-src-project", got, want)
	}
}

func TestDecodeDirKey_NoLeadingHyphen(t *testing.T) {
	got := decodeDirKey("relative-path")
	if got != "" {
		t.Errorf("decodeDirKey(%q) = %q, want empty string", "relative-path", got)
	}
}

func TestDecodeDirKey_SingleComponent(t *testing.T) {
	got := decodeDirKey("-tmp")
	want := "/tmp"
	if got != want {
		t.Errorf("decodeDirKey(%q) = %q, want %q", "-tmp", got, want)
	}
}

func TestDecodeDirKey_Empty(t *testing.T) {
	got := decodeDirKey("")
	if got != "" {
		t.Errorf("decodeDirKey(%q) = %q, want empty string", "", got)
	}
}

// --- test helpers ---

// writeTestSession creates a minimal Claude JSONL session file for testing.
func writeTestSession(t *testing.T, path, sessionID, model string) {
	t.Helper()
	writeTestSessionWithTokens(t, path, sessionID, model, 0, 0)
}

// writeTestSessionWithTokens creates a Claude JSONL session file with token usage.
func writeTestSessionWithTokens(t *testing.T, path, sessionID, model string, tokensIn, tokensOut int64) {
	t.Helper()

	ts := time.Now().Add(-5 * time.Minute).Format(time.RFC3339Nano)

	// First line: session init with sessionId
	line1 := map[string]interface{}{
		"type":      "init",
		"sessionId": sessionID,
		"timestamp": ts,
	}

	// Second line: assistant message with model and usage
	line2 := map[string]interface{}{
		"type":      "assistant",
		"timestamp": ts,
		"message": map[string]interface{}{
			"model": model,
			"usage": map[string]interface{}{
				"input_tokens":  tokensIn,
				"output_tokens": tokensOut,
			},
		},
	}

	b1, err := json.Marshal(line1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := json.Marshal(line2)
	if err != nil {
		t.Fatal(err)
	}

	content := fmt.Sprintf("%s\n%s\n", string(b1), string(b2))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- ParseTrace tests ---

func TestClaudeParseTrace_BasicTurn(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"fix the bug"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"I'll fix that."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}],"usage":{"input_tokens":100,"output_tokens":50}}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Claude{}
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
	if turn.Actions[0].Name != "Read" {
		t.Errorf("Actions[0].Name = %q, want %q", turn.Actions[0].Name, "Read")
	}
	if turn.Actions[0].Snippet != "main.go" {
		t.Errorf("Actions[0].Snippet = %q, want %q", turn.Actions[0].Snippet, "main.go")
	}
	if len(turn.OutputLines) == 0 || turn.OutputLines[0] != "I'll fix that." {
		t.Errorf("OutputLines = %v, want [\"I'll fix that.\"]", turn.OutputLines)
	}
	if turn.TokensIn != 100 {
		t.Errorf("TokensIn = %d, want 100", turn.TokensIn)
	}
	if turn.TokensOut != 50 {
		t.Errorf("TokensOut = %d, want 50", turn.TokensOut)
	}
	if turn.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", turn.Model, "claude-opus-4-6")
	}
}

func TestClaudeParseTrace_MultipleTurns(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"first question"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"first answer"}],"usage":{"input_tokens":50,"output_tokens":25}}}
{"type":"user","timestamp":"2026-01-01T10:01:00Z","message":{"role":"user","content":"second question"}}
{"type":"assistant","timestamp":"2026-01-01T10:01:05Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"second answer"}],"usage":{"input_tokens":80,"output_tokens":40}}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Claude{}
	turns, err := c.ParseTrace(path)
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

func TestClaudeParseTrace_ToolResultError(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"run the build"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:01Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go build ./..."}}],"usage":{"input_tokens":50,"output_tokens":20}}}
{"type":"user","timestamp":"2026-01-01T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"compilation failed: undefined variable"}]}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Claude{}
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
		t.Error("expected action.Success = false for errored tool result")
	}
	if action.ErrorMsg == "" || action.ErrorMsg != "compilation failed: undefined variable" {
		t.Errorf("ErrorMsg = %q, want %q", action.ErrorMsg, "compilation failed: undefined variable")
	}
}

func TestClaudeParseTrace_MissingFile(t *testing.T) {
	c := &Claude{}
	_, err := c.ParseTrace("/nonexistent/path/session.jsonl")
	if err == nil {
		t.Error("ParseTrace on missing file should return error")
	}
}

func TestClaudeParseTrace_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Claude{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for empty file, got %d", len(turns))
	}
}

func TestClaudeParseTrace_CostCalculation(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000,"output_tokens":500}}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Claude{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0", turns[0].CostUSD)
	}
}

func TestClaudeSpawnCommand_AcceptEdits(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/p", "", "acceptEdits")
	assertArgsContain(t, cmd.Args, "--permission-mode", "acceptEdits")
}

func TestClaudeSpawnCommand_DontAsk(t *testing.T) {
	c := &Claude{}
	cmd := c.SpawnCommand("/tmp/p", "", "dontAsk")
	assertArgsContain(t, cmd.Args, "--permission-mode", "dontAsk")
}

func TestClaudeOTELEnv(t *testing.T) {
	c := &Claude{}
	env := c.OTELEnv("http://localhost:4318")

	required := []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:4318/v1/logs",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_METRICS_EXPORTER=otlp",
	}
	for _, r := range required {
		if !strings.Contains(env, r) {
			t.Errorf("OTELEnv missing %q, got:\n%s", r, env)
		}
	}

	// Verify we don't use "none" for any exporter (crashes some OTEL SDKs)
	if strings.Contains(env, "=none") {
		t.Errorf("OTELEnv should not set any exporter to 'none', got:\n%s", env)
	}
}

func TestClaudeParseTrace_PreservesIndentation(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")

	// Text with leading whitespace (code block content)
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"show code"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Here is code:\n` + "```" + `go\nfunc main() {\n    fmt.Println(\"hello\")\n}\n` + "```" + `"}],"usage":{"input_tokens":10,"output_tokens":20}}}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Claude{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}

	// Check that the indented line preserves its leading spaces
	found := false
	for _, line := range turns[0].OutputLines {
		if strings.HasPrefix(line, "    ") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected indented line with leading spaces, got: %v", turns[0].OutputLines)
	}
}

// Verify Claude implements the Provider interface at compile time.
var _ Provider = (*Claude)(nil)
