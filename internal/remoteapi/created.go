package remoteapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CreatedSession is a session started through the remote API.
type CreatedSession struct {
	Cwd     string    `json:"cwd"`
	Prompt  string    `json:"prompt"` // its first message, the title until the agent names it
	Created time.Time `json:"created"`
}

// CreatedStore remembers the sessions aimux started, in an owner-only JSON
// file. The agent SDK marks them automated, which would hide them, and the
// agent writes a session's file only on its first prompt.
type CreatedStore struct {
	Path string

	mu     sync.Mutex
	loaded bool
	byID   map[string]CreatedSession
}

func (s *CreatedStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byID = map[string]CreatedSession{}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return // missing (nothing created yet) or unreadable: start empty
	}
	_ = json.Unmarshal(data, &s.byID) // a corrupt file only loses the list
	if s.byID == nil {
		s.byID = map[string]CreatedSession{} // the file held null
	}
}

// Get returns a created session, if this store has it.
func (s *CreatedStore) Get(id string) (CreatedSession, bool) {
	if s == nil {
		return CreatedSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	c, ok := s.byID[id]
	return c, ok
}

// All returns every created session by id.
func (s *CreatedStore) All() map[string]CreatedSession {
	out := map[string]CreatedSession{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	for id, c := range s.byID {
		out[id] = c
	}
	return out
}

// Add records a new session and saves the store.
func (s *CreatedStore) Add(id, cwd, prompt string) error {
	if s == nil {
		return errors.New("no store for created sessions")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.byID[id] = CreatedSession{Cwd: cwd, Prompt: shorten(prompt, 200), Created: time.Now().UTC()}
	data, err := json.MarshalIndent(s.byID, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("save created sessions: %w", err)
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("save created sessions: %w", err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("save created sessions: %w", err)
	}
	return nil
}
