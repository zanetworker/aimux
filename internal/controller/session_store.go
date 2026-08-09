package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/zanetworker/aimux/internal/debuglog"
)

type SessionStore struct {
	mu   sync.Mutex
	path string
	data map[string]string
}

func NewSessionStore(configDir string) *SessionStore {
	path := filepath.Join(configDir, "remote-sessions.json")
	s := &SessionStore{path: path, data: make(map[string]string)}
	s.load()
	return s
}

func (s *SessionStore) Get(sandboxName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[sandboxName]
}

func (s *SessionStore) Put(sandboxName, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[sandboxName] = sessionID
	s.save()
}

func (s *SessionStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, &s.data)
	debuglog.Log("session-store: loaded %d entries from %s", len(s.data), s.path)
}

func (s *SessionStore) save() {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		debuglog.Log("session-store: save failed: %v", err)
	}
}
