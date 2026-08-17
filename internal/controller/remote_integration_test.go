//go:build integration

package controller

import (
	"os/exec"
	"testing"
	"time"

	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/terminal"
)

func TestIntegration_RemoteLaunchSession(t *testing.T) {
	if _, err := exec.LookPath("openshell"); err != nil {
		t.Skip("openshell not in PATH")
	}

	out, err := exec.Command("openshell", "status").CombinedOutput()
	if err != nil {
		t.Skipf("gateway not reachable: %s", string(out))
	}

	engine, err := aimuxcompose.New(aimuxcompose.Options{
		Gateway:  "http://127.0.0.1:8090",
		Insecure: true,
	})
	if err != nil {
		t.Fatalf("aimuxcompose.New: %v", err)
	}

	result, err := RemoteLaunchSession(engine, "claude", "/tmp", RemoteSessionOpts{})
	if err != nil {
		t.Fatalf("RemoteLaunchSession: %v", err)
	}
	t.Logf("Sandbox: %s", result.SandboxName)
	t.Logf("OTELSessionID: %s", result.OTELSessionID)

	defer func() {
		exec.Command("openshell", "sandbox", "delete", result.SandboxName).Run() // #nosec G204
	}()

	if result.SandboxName == "" {
		t.Fatal("sandbox name is empty")
	}
	if result.OTELSessionID == "" {
		t.Fatal("OTEL session ID is empty")
	}

	// Verify we can connect a PTY backend to the sandbox.
	time.Sleep(2 * time.Second)
	backend, err := terminal.NewOpenShellExec(result.SandboxName, "", false, 80, 24)
	if err != nil {
		t.Fatalf("NewOpenShellExec: %v", err)
	}
	defer func() { _ = backend.Close() }()

	// Send a command and verify the backend is alive.
	if !backend.Alive() {
		t.Fatal("backend is not alive after connect")
	}
	_, _ = backend.Write([]byte("echo REMOTE_SESSION_OK\n"))
	time.Sleep(2 * time.Second)

	buf := make([]byte, 4096)
	n, _ := backend.Read(buf)
	output := string(buf[:n])
	t.Logf("Output:\n%s", output)

	if !containsStr(output, "REMOTE_SESSION_OK") {
		t.Error("expected REMOTE_SESSION_OK in output")
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
