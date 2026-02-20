package discovery

import (
	"os"
	"testing"
)

func TestGetProcessCwdOwnPID(t *testing.T) {
	pid := os.Getpid()
	cwd, err := getProcessCwd(pid)
	if err != nil {
		t.Fatalf("getProcessCwd(%d): %v", pid, err)
	}

	expected, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd(): %v", err)
	}

	if cwd != expected {
		t.Errorf("getProcessCwd(%d) = %q, want %q", pid, cwd, expected)
	}
}

func TestGetProcessCwdInvalidPID(t *testing.T) {
	_, err := getProcessCwd(9999999)
	if err == nil {
		t.Error("expected error for invalid PID")
	}
}
