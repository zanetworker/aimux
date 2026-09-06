package session

import (
	"time"

	"github.com/google/uuid"
)

// Manager provides session lifecycle operations on top of a Store.
type Manager struct {
	store Store
}

// NewManager creates a Manager backed by the given Store.
func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

// Store returns the underlying Store for direct access when needed.
func (m *Manager) Store() Store {
	return m.store
}

// TransitionSession validates and applies a status transition for a session.
func (m *Manager) TransitionSession(id string, to SessionStatus) error {
	sess, err := m.store.Get(id)
	if err != nil {
		return err
	}
	if err := Transition(sess, to); err != nil {
		return err
	}
	return m.store.Update(sess)
}

// CreateSession creates a new session with a generated UUID and persists it.
func (m *Manager) CreateSession(provider, env, dir, model, sandboxName string) (*Session, error) {
	now := time.Now()
	s := &Session{
		ID:           uuid.NewString(),
		Provider:     provider,
		Environment:  env,
		WorkingDir:   dir,
		Model:        model,
		SandboxName:  sandboxName,
		Status:       StatusCreated,
		StartTime:    now,
		LastActivity: now,
	}
	if err := m.store.Create(s); err != nil {
		return nil, err
	}
	return s, nil
}
