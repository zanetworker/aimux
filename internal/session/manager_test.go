package session_test

import (
	"testing"

	"github.com/zanetworker/aimux/internal/session"
)

func tempManager(t *testing.T) *session.Manager {
	t.Helper()
	store := tempStore(t)
	return session.NewManager(store)
}

func TestManager_CreateSession(t *testing.T) {
	m := tempManager(t)
	s, err := m.CreateSession("claude", "local", "/tmp/project", "opus", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if s.ID == "" {
		t.Error("ID should not be empty")
	}
	if s.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", s.Provider)
	}
	if s.Environment != "local" {
		t.Errorf("Environment = %q, want local", s.Environment)
	}
	if s.WorkingDir != "/tmp/project" {
		t.Errorf("WorkingDir = %q, want /tmp/project", s.WorkingDir)
	}
	if s.Model != "opus" {
		t.Errorf("Model = %q, want opus", s.Model)
	}
	if s.Status != session.StatusCreated {
		t.Errorf("Status = %q, want created", s.Status)
	}
	if s.StartTime.IsZero() {
		t.Error("StartTime should not be zero")
	}
}

func TestManager_CreateSession_WithSandbox(t *testing.T) {
	m := tempManager(t)
	s, err := m.CreateSession("claude", "sandbox", "/tmp", "", "ax-cl-1234")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if s.SandboxName != "ax-cl-1234" {
		t.Errorf("SandboxName = %q, want ax-cl-1234", s.SandboxName)
	}
}

func TestManager_CreateSession_Persists(t *testing.T) {
	m := tempManager(t)
	s, err := m.CreateSession("claude", "local", "/tmp", "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Verify it's in the store
	got, err := m.Store().Get(s.ID)
	if err != nil {
		t.Fatalf("Store.Get: %v", err)
	}
	if got.Provider != "claude" {
		t.Errorf("persisted Provider = %q, want claude", got.Provider)
	}
}
