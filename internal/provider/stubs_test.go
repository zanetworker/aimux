package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/agentmux/internal/agent"
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
	agents, err := g.Discover()
	if err != nil {
		t.Errorf("Gemini.Discover() error = %v, want nil", err)
	}
	if agents != nil {
		t.Errorf("Gemini.Discover() = %v, want nil", agents)
	}
}

func TestGeminiResumeCommand(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{SessionID: "test", WorkingDir: "/tmp"})
	if cmd != nil {
		t.Errorf("Gemini.ResumeCommand() = %v, want nil", cmd)
	}
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
	expectedModes := []string{"default", "full-auto"}

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

// --- Gemini new methods ---

func TestGeminiCanEmbed(t *testing.T) {
	g := &Gemini{}
	if g.CanEmbed() {
		t.Error("Gemini.CanEmbed() = true, want false")
	}
}

func TestGeminiFindSessionFile(t *testing.T) {
	g := &Gemini{}
	a := agent.Agent{SessionID: "test", WorkingDir: "/tmp"}
	if got := g.FindSessionFile(a); got != "" {
		t.Errorf("Gemini.FindSessionFile() = %q, want empty", got)
	}
}

func TestGeminiRecentDirs(t *testing.T) {
	g := &Gemini{}
	dirs := g.RecentDirs(10)
	if dirs != nil {
		t.Errorf("Gemini.RecentDirs() = %v, want nil", dirs)
	}
}

func TestGeminiSpawnCommand(t *testing.T) {
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
	// No flags for gemini
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
}

func TestGeminiSpawnCommand_IgnoresModelAndMode(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "some-model", "some-mode")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	// Gemini ignores model and mode — should still be just the binary
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
}

func TestGeminiSpawnArgs(t *testing.T) {
	g := &Gemini{}
	sa := g.SpawnArgs()

	if len(sa.Models) != 1 || sa.Models[0] != "default" {
		t.Errorf("SpawnArgs.Models = %v, want [default]", sa.Models)
	}
	if len(sa.Modes) != 1 || sa.Modes[0] != "default" {
		t.Errorf("SpawnArgs.Modes = %v, want [default]", sa.Modes)
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

// Suppress unused variable warnings for time import.
var _ = time.Now

// Verify stubs implement the Provider interface at compile time.
var _ Provider = (*Codex)(nil)
var _ Provider = (*Gemini)(nil)
