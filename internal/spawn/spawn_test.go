package spawn

import (
	"path/filepath"
	"testing"
)

func TestBuildCommand_Claude_Default(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "claude",
		Dir:      "/tmp/myproject",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil for claude provider")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	// Default: no model, no mode flags — just the binary name
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
	if base := filepath.Base(cmd.Args[0]); base != "claude" {
		t.Errorf("binary = %q, want %q", base, "claude")
	}
}

func TestBuildCommand_Claude_WithModel(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "claude",
		Dir:      "/tmp/myproject",
		Model:    "opus",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "opus")
}

func TestBuildCommand_Claude_Bypass(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "claude",
		Dir:      "/tmp/myproject",
		Mode:     "bypass",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--dangerously-skip-permissions")
}

func TestBuildCommand_Claude_Plan(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "claude",
		Dir:      "/tmp/myproject",
		Mode:     "plan",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--permission-mode", "plan")
}

func TestBuildCommand_Codex_Default(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "codex",
		Dir:      "/tmp/myproject",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil for codex provider")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	assertArgPresent(t, cmd.Args, "--no-alt-screen")
	// Default mode adds --sandbox workspace-write
	assertArgsContain(t, cmd.Args, "--sandbox", "workspace-write")
}

func TestBuildCommand_Codex_FullAuto(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "codex",
		Dir:      "/tmp/myproject",
		Mode:     "full-auto",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--full-auto")
	assertArgAbsent(t, cmd.Args, "--sandbox")
}

func TestBuildCommand_Codex_WithModel(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "codex",
		Dir:      "/tmp/myproject",
		Model:    "o3",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "o3")
}

func TestBuildCommand_Gemini(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "gemini",
		Dir:      "/tmp/myproject",
	})
	if cmd == nil {
		t.Fatal("BuildCommand returned nil for gemini provider")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	if base := filepath.Base(cmd.Args[0]); base != "gemini" {
		t.Errorf("binary = %q, want %q", base, "gemini")
	}
	// No flags for gemini
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
}

func TestBuildCommand_UnknownProvider(t *testing.T) {
	cmd := BuildCommand(LaunchConfig{
		Provider: "unknown",
		Dir:      "/tmp/myproject",
	})
	if cmd != nil {
		t.Errorf("BuildCommand for unknown provider returned %v, want nil", cmd)
	}
}

func TestTmuxSessionName(t *testing.T) {
	tests := []struct {
		provider string
		dir      string
		want     string
	}{
		{"claude", "/Users/me/projects/blog-concept", "agentmux-claude-blog-concept"},
		{"codex", "/home/dev/my-app", "agentmux-codex-my-app"},
		{"gemini", "/tmp/test", "agentmux-gemini-test"},
		{"claude", "/Users/me/go/src/github.com/zanetworker/agentmux", "agentmux-claude-agentmux"},
	}
	for _, tt := range tests {
		got := TmuxSessionName(tt.provider, tt.dir)
		if got != tt.want {
			t.Errorf("TmuxSessionName(%q, %q) = %q, want %q", tt.provider, tt.dir, got, tt.want)
		}
	}
}

func TestTmuxSessionName_WithSpaces(t *testing.T) {
	tests := []struct {
		provider string
		dir      string
		want     string
	}{
		{"claude", "/tmp/my project", "agentmux-claude-my-project"},
		{"codex", "/home/dev/cool stuff here", "agentmux-codex-cool-stuff-here"},
	}
	for _, tt := range tests {
		got := TmuxSessionName(tt.provider, tt.dir)
		if got != tt.want {
			t.Errorf("TmuxSessionName(%q, %q) = %q, want %q", tt.provider, tt.dir, got, tt.want)
		}
	}
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
