package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/agentmux/internal/agent"
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

func TestClaudeParseConversation(t *testing.T) {
	c := &Claude{}
	segments, err := c.ParseConversation("/some/path")
	if err != nil {
		t.Errorf("ParseConversation returned error: %v", err)
	}
	if segments != nil {
		t.Errorf("ParseConversation returned %v, want nil", segments)
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
	expectedModes := []string{"default", "bypass", "plan"}

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

// Verify Claude implements the Provider interface at compile time.
var _ Provider = (*Claude)(nil)
