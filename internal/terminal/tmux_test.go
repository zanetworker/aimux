package terminal

import (
	"os/exec"
	"testing"
)

func TestStartTmux_NilCmd(t *testing.T) {
	_, err := StartTmux(nil, 80, 24, "/bin/sh", "")
	if err == nil {
		t.Error("StartTmux(nil) should return error")
	}
}

func TestAttachTmux_NonexistentSession(t *testing.T) {
	_, err := AttachTmux("aimux-nonexistent-test-session", 80, 24)
	if err == nil {
		t.Error("AttachTmux(nonexistent) should return error")
	}
}

func TestStartTmux_CreateAndClose(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	cmd := exec.Command("sh", "-c", "echo hello; sleep 10")
	ts, err := StartTmux(cmd, 80, 24, "/bin/sh", "")
	if err != nil {
		t.Fatalf("StartTmux error: %v", err)
	}
	defer ts.Close()

	if !ts.Alive() {
		t.Error("session should be alive after creation")
	}

	if ts.SessionName() == "" {
		t.Error("SessionName() should not be empty")
	}

	// Close should kill the session since we created it
	ts.Close()

	if ts.Alive() {
		t.Error("session should not be alive after Close")
	}
}

func TestTmuxSession_Resize(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	cmd := exec.Command("sh", "-c", "sleep 10")
	ts, err := StartTmux(cmd, 80, 24, "/bin/sh", "")
	if err != nil {
		t.Fatalf("StartTmux error: %v", err)
	}
	defer ts.Close()

	// Resize should not error
	if err := ts.Resize(120, 40); err != nil {
		t.Errorf("Resize error: %v", err)
	}
}

func TestPollIntervalAdaptive(t *testing.T) {
	fast := pollInterval(true)
	slow := pollInterval(false)

	if fast >= slow {
		t.Errorf("active interval (%v) should be less than idle (%v)", fast, slow)
	}
	if fast.Milliseconds() > 150 {
		t.Errorf("active interval too slow: %v", fast)
	}
	if slow.Milliseconds() < 400 {
		t.Errorf("idle interval too fast: %v", slow)
	}
}
