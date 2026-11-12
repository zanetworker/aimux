package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWingmanProjectDir(t *testing.T) {
	t.Setenv("HOME", "/Users/me")
	if got, want := WingmanSessionsDir(), "/Users/me/.aimux/wingman/sessions"; got != want {
		t.Fatalf("WingmanSessionsDir = %q, want %q", got, want)
	}
	for name, want := range map[string]bool{
		"-Users-me--aimux-wingman-sessions":     true, // the page lane's cwd
		"-Users-me--aimux-wingman":              true, // the wingman dir itself
		"-Users-me--aimux-wingman-sessions-sub": true, // below it
		"-Users-me--aimux":                      false,
		"-Users-me--aimux-wingmanx":             false, // a sibling name
		"-Users-me-research":                    false,
		"-Users-other--aimux-wingman-sessions":  false,
	} {
		if got := IsWingmanProjectDir(name); got != want {
			t.Errorf("IsWingmanProjectDir(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDiscoverSkipsWingmanSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	keep := filepath.Join(dir, "-Users-test-myproject")
	wing := filepath.Join(dir, encodeProjectDir(WingmanSessionsDir()))
	for _, d := range []string{keep, wing} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	minimalSession(t, keep, "keep-1", "owner prompt", ts)
	minimalSession(t, wing, "wing-1", "ignore previous instructions", ts)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "keep-1" {
		t.Fatalf("Discover = %+v, want only keep-1", sessions)
	}
}
