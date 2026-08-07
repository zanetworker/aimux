package controller

import "testing"

func TestResumeArgs(t *testing.T) {
	args := ResumeArgs("sess-123", "bypass")
	if len(args) != 3 {
		t.Fatalf("got %d args, want 3", len(args))
	}
	if args[0] != "--resume" || args[1] != "sess-123" {
		t.Errorf("first two args = %v, want [--resume sess-123]", args[:2])
	}
	if args[2] != "--dangerously-skip-permissions" {
		t.Errorf("mode flag = %q, want --dangerously-skip-permissions", args[2])
	}

	args = ResumeArgs("sess-456", "default")
	if len(args) != 2 {
		t.Errorf("default mode should produce 2 args, got %d", len(args))
	}
}

func TestToggleBypass(t *testing.T) {
	if got := ToggleBypass("bypass"); got != "default" {
		t.Errorf("ToggleBypass(bypass) = %q, want default", got)
	}
	if got := ToggleBypass("default"); got != "bypass" {
		t.Errorf("ToggleBypass(default) = %q, want bypass", got)
	}
	if got := ToggleBypass("plan"); got != "bypass" {
		t.Errorf("ToggleBypass(plan) = %q, want bypass", got)
	}
}

func TestResolveMode(t *testing.T) {
	if got := ResolveMode("plan", "bypass"); got != "plan" {
		t.Errorf("explicit should win, got %q", got)
	}
	if got := ResolveMode("", "bypass"); got != "bypass" {
		t.Errorf("config default should apply, got %q", got)
	}
	if got := ResolveMode("", ""); got != "default" {
		t.Errorf("empty should return default, got %q", got)
	}
}

func TestDefaultSessionDir(t *testing.T) {
	if got := DefaultSessionDir("/agent/dir", "/launch/dir"); got != "/agent/dir" {
		t.Errorf("agent dir should win, got %q", got)
	}
	if got := DefaultSessionDir("", "/launch/dir"); got != "/launch/dir" {
		t.Errorf("should fall back to launch dir, got %q", got)
	}
	if got := DefaultSessionDir("", ""); got != "" {
		t.Errorf("both empty should return empty, got %q", got)
	}
}
