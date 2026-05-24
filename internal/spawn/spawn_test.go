package spawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTmuxSessionName(t *testing.T) {
	tests := []struct {
		provider string
		dir      string
		want     string
	}{
		{"claude", "/Users/me/projects/blog-concept", "aimux-claude-blog-concept"},
		{"codex", "/tmp/my project", "aimux-codex-my-project"},
		{"gemini", "/home/user/app", "aimux-gemini-app"},
	}
	for _, tt := range tests {
		got := TmuxSessionName(tt.provider, tt.dir)
		if got != tt.want {
			t.Errorf("TmuxSessionName(%q, %q) = %q, want %q", tt.provider, tt.dir, got, tt.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"it's", "'it'\\''s'"},
		{"/path/to/dir", "'/path/to/dir'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLaunch_NilCmd(t *testing.T) {
	err := Launch(nil, "claude", "/tmp", "tmux", "/bin/sh", "")
	if err == nil {
		t.Error("Launch(nil) should return error")
	}
}

func TestLaunch_UnsupportedRuntime(t *testing.T) {
	cmd := exec.Command("echo", "test")
	err := Launch(cmd, "claude", "/tmp", "unsupported", "/bin/sh", "")
	if err == nil {
		t.Error("Launch with unsupported runtime should return error")
	}
}

func TestContainerName(t *testing.T) {
	tests := []struct {
		provider string
		dir      string
		want     string
	}{
		{"claude", "/Users/me/projects/blog", "aimux-claude-blog"},
		{"codex", "/tmp/my project", "aimux-codex-my-project"},
	}
	for _, tt := range tests {
		got := ContainerName(tt.provider, tt.dir)
		if got != tt.want {
			t.Errorf("ContainerName(%q, %q) = %q, want %q", tt.provider, tt.dir, got, tt.want)
		}
	}
}

func TestLaunchInContainer_NilCmd(t *testing.T) {
	err := LaunchInContainer(nil, "claude", "/tmp", "/bin/sh", "", ContainerOpts{})
	if err == nil {
		t.Error("LaunchInContainer(nil) should return error")
	}
}

func TestLaunchInContainer_EngineMissing(t *testing.T) {
	cmd := exec.Command("echo", "test")
	err := LaunchInContainer(cmd, "claude", "/tmp", "/bin/sh", "", ContainerOpts{Engine: "nonexistent-engine-xyz"})
	if err == nil {
		t.Error("expected error when engine binary not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %s", err.Error())
	}
}

func TestLaunch_DirectSessionManager(t *testing.T) {
	cmd := exec.Command("sleep", "0.1")
	err := Launch(cmd, "claude", t.TempDir(), "direct", "/bin/sh", "")
	if err != nil {
		t.Errorf("Launch with direct session manager should succeed, got: %v", err)
	}
}

func TestLaunchDirect_ProcessRuns(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")

	cmd := exec.Command("touch", marker) // #nosec G204
	err := Launch(cmd, "claude", dir, "direct", "/bin/sh", "")
	if err != nil {
		t.Fatalf("Launch direct: %v", err)
	}

	// Wait briefly for the background process to run.
	time.Sleep(200 * time.Millisecond)

	if _, err := os.Stat(marker); err != nil {
		t.Error("direct launch process did not run (marker file not created)")
	}
}

func TestParseEnvPrefix(t *testing.T) {
	env := parseEnvPrefix("FOO=bar BAZ=qux")
	if env["FOO"] != "bar" {
		t.Errorf("expected FOO=bar, got %q", env["FOO"])
	}
	if env["BAZ"] != "qux" {
		t.Errorf("expected BAZ=qux, got %q", env["BAZ"])
	}
}

func TestParseEnvPrefix_Empty(t *testing.T) {
	env := parseEnvPrefix("")
	if len(env) != 0 {
		t.Errorf("expected empty map, got %d entries", len(env))
	}
}
