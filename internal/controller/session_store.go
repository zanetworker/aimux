package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/zanetworker/aimux/internal/debuglog"
)

// LaunchMeta records what was stored at sandbox launch time.
type LaunchMeta struct {
	SessionID string `json:"session_id"`
	Provider  string `json:"provider"`
	Dir       string `json:"dir"`
}

type SessionStore struct {
	mu   sync.Mutex
	path string
	data map[string]LaunchMeta
}

func NewSessionStore(configDir string) *SessionStore {
	path := filepath.Join(configDir, "remote-sessions.json")
	s := &SessionStore{path: path, data: make(map[string]LaunchMeta)}
	s.load()
	return s
}

// Get returns the session ID for the given sandbox (backward-compat accessor).
func (s *SessionStore) Get(sandboxName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[sandboxName].SessionID
}

// Put stores only the session ID; provider and dir are left empty.
// Prefer PutMeta when all three values are available.
func (s *SessionStore) Put(sandboxName, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.data[sandboxName]
	existing.SessionID = sessionID
	s.data[sandboxName] = existing
	s.save()
}

// PutMeta stores the full launch metadata for a sandbox.
func (s *SessionStore) PutMeta(sandboxName string, meta LaunchMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[sandboxName] = meta
	s.save()
}

// GetMeta returns the full launch metadata for a sandbox.
func (s *SessionStore) GetMeta(sandboxName string) LaunchMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[sandboxName]
}

func (s *SessionStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	// Try new format {sandboxName: LaunchMeta} first.
	if json.Unmarshal(raw, &s.data) == nil {
		debuglog.Log("session-store: loaded %d entries from %s", len(s.data), s.path)
		return
	}
	// Migrate old format {sandboxName: "sessionID"} → new format.
	var old map[string]string
	if json.Unmarshal(raw, &old) == nil {
		for k, v := range old {
			s.data[k] = LaunchMeta{SessionID: v}
		}
		debuglog.Log("session-store: migrated %d legacy entries from %s", len(old), s.path)
		s.save()
	}
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
