package session_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/session"
)

func TestTransition_ValidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from session.SessionStatus
		to   session.SessionStatus
	}{
		{"created_to_running", session.StatusCreated, session.StatusRunning},
		{"running_to_idle", session.StatusRunning, session.StatusIdle},
		{"running_to_terminated", session.StatusRunning, session.StatusTerminated},
		{"running_to_error", session.StatusRunning, session.StatusError},
		{"idle_to_running", session.StatusIdle, session.StatusRunning},
		{"idle_to_terminated", session.StatusIdle, session.StatusTerminated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session.Session{
				ID:           "test-1",
				Status:       tt.from,
				LastActivity: time.Now().Add(-time.Hour),
			}
			if err := session.Transition(s, tt.to); err != nil {
				t.Fatalf("Transition(%s → %s) returned error: %v", tt.from, tt.to, err)
			}
			if s.Status != tt.to {
				t.Errorf("Status = %q, want %q", s.Status, tt.to)
			}
		})
	}
}

func TestTransition_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from session.SessionStatus
		to   session.SessionStatus
	}{
		{"terminated_to_running", session.StatusTerminated, session.StatusRunning},
		{"terminated_to_idle", session.StatusTerminated, session.StatusIdle},
		{"terminated_to_created", session.StatusTerminated, session.StatusCreated},
		{"terminated_to_error", session.StatusTerminated, session.StatusError},
		{"error_to_running", session.StatusError, session.StatusRunning},
		{"error_to_idle", session.StatusError, session.StatusIdle},
		{"error_to_created", session.StatusError, session.StatusCreated},
		{"error_to_terminated", session.StatusError, session.StatusTerminated},
		{"created_to_idle", session.StatusCreated, session.StatusIdle},
		{"created_to_terminated", session.StatusCreated, session.StatusTerminated},
		{"created_to_error", session.StatusCreated, session.StatusError},
		{"idle_to_idle", session.StatusIdle, session.StatusIdle},
		{"running_to_created", session.StatusRunning, session.StatusCreated},
		{"idle_to_created", session.StatusIdle, session.StatusCreated},
		{"running_to_running", session.StatusRunning, session.StatusRunning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session.Session{
				ID:     "test-1",
				Status: tt.from,
			}
			err := session.Transition(s, tt.to)
			if err == nil {
				t.Fatalf("Transition(%s → %s) should have returned error", tt.from, tt.to)
			}
			wantMsg := "invalid session transition: " + string(tt.from) + " → " + string(tt.to)
			if !strings.Contains(err.Error(), wantMsg) {
				t.Errorf("error = %q, want containing %q", err.Error(), wantMsg)
			}
			if s.Status != tt.from {
				t.Errorf("Status changed to %q on error, should remain %q", s.Status, tt.from)
			}
		})
	}
}

func TestTransition_UpdatesLastActivity(t *testing.T) {
	s := &session.Session{
		ID:           "test-1",
		Status:       session.StatusCreated,
		LastActivity: time.Now().Add(-time.Hour),
	}
	before := time.Now()
	if err := session.Transition(s, session.StatusRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	after := time.Now()

	if s.LastActivity.Before(before) || s.LastActivity.After(after) {
		t.Errorf("LastActivity = %v, want between %v and %v", s.LastActivity, before, after)
	}
}

func TestTransition_ConcurrentSafety(t *testing.T) {
	var wg sync.WaitGroup
	errors := make([]error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s := &session.Session{
				ID:           "test-concurrent",
				Status:       session.StatusCreated,
				LastActivity: time.Now(),
			}
			errors[n] = session.Transition(s, session.StatusRunning)
		}(i)
	}
	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
}

func TestManager_TransitionSession(t *testing.T) {
	m := tempSQLiteManager(t)
	s, err := m.CreateSession("claude", "local", "/tmp", "opus", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := m.TransitionSession(s.ID, session.StatusRunning); err != nil {
		t.Fatalf("TransitionSession(created→running): %v", err)
	}

	got, err := m.Store().Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != session.StatusRunning {
		t.Errorf("Status = %q, want running", got.Status)
	}
}

func TestManager_TransitionSession_Invalid(t *testing.T) {
	m := tempSQLiteManager(t)
	s, err := m.CreateSession("claude", "local", "/tmp", "opus", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	err = m.TransitionSession(s.ID, session.StatusIdle)
	if err == nil {
		t.Fatal("TransitionSession(created→idle) should have returned error")
	}

	got, _ := m.Store().Get(s.ID)
	if got.Status != session.StatusCreated {
		t.Errorf("Status changed to %q after failed transition, should remain created", got.Status)
	}
}

func TestManager_TransitionSession_NotFound(t *testing.T) {
	m := tempSQLiteManager(t)
	err := m.TransitionSession("nonexistent", session.StatusRunning)
	if err == nil {
		t.Fatal("TransitionSession for nonexistent session should error")
	}
}

func tempSQLiteManager(t *testing.T) *session.Manager {
	t.Helper()
	store := tempSQLiteStore(t)
	return session.NewManager(store)
}
