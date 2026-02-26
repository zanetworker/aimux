package discovery

import (
	"os"
	"testing"
)

func TestGetProcessCwdOwnPID(t *testing.T) {
	pid := os.Getpid()
	cwd, err := GetProcessCwd(pid)
	if err != nil {
		t.Fatalf("GetProcessCwd(%d): %v", pid, err)
	}

	expected, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd(): %v", err)
	}

	if cwd != expected {
		t.Errorf("GetProcessCwd(%d) = %q, want %q", pid, cwd, expected)
	}
}

func TestGetProcessCwdInvalidPID(t *testing.T) {
	_, err := GetProcessCwd(9999999)
	if err == nil {
		t.Error("expected error for invalid PID")
	}
}
