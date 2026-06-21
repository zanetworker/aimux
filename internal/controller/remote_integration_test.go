//go:build integration

package controller

import (
	"os/exec"
	"testing"
	"time"
)

func TestIntegration_RemoteLaunchSession(t *testing.T) {
	if _, err := exec.LookPath("openshell"); err != nil {
		t.Skip("openshell not in PATH")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH")
	}

	out, err := exec.Command("openshell", "status").CombinedOutput()
	if err != nil {
		t.Skipf("gateway not reachable: %s", string(out))
	}

	result, err := RemoteLaunchSession("claude", "/tmp", RemoteSessionOpts{
		Binary: "openshell",
	})
	if err != nil {
		t.Fatalf("RemoteLaunchSession: %v", err)
	}
	t.Logf("Sandbox: %s", result.SandboxName)
	t.Logf("Tmux: %s", result.TmuxSession)

	defer func() {
		exec.Command("tmux", "kill-session", "-t", result.TmuxSession).Run()
		exec.Command("openshell", "sandbox", "delete", result.SandboxName).Run()
	}()

	if result.SandboxName == "" {
		t.Fatal("sandbox name is empty")
	}
	if result.TmuxSession == "" {
		t.Fatal("tmux session name is empty")
	}

	// Verify tmux session exists
	time.Sleep(3 * time.Second)
	if err := exec.Command("tmux", "has-session", "-t", result.TmuxSession).Run(); err != nil {
		t.Fatalf("tmux session %s does not exist", result.TmuxSession)
	}

	// Send a command and verify output
	exec.Command("tmux", "send-keys", "-t", result.TmuxSession, "echo REMOTE_SESSION_OK && whoami", "Enter").Run()
	time.Sleep(3 * time.Second)

	pane, err := exec.Command("tmux", "capture-pane", "-t", result.TmuxSession, "-p").Output()
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	output := string(pane)
	t.Logf("Pane:\n%s", output)

	if !containsStr(output, "REMOTE_SESSION_OK") {
		t.Error("expected REMOTE_SESSION_OK in pane output")
	}
	if !containsStr(output, "sandbox") {
		t.Error("expected 'sandbox' user in pane output")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
