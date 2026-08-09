package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/zanetworker/aimux/internal/debuglog"
)

// remoteSessionStore persists the sandbox→UUID mapping to disk so it survives
// aimux restarts. Without this, sandboxes launched in a prior aimux run lose
// their pinned Claude session UUID, and the preview/trace pane can't find the
// OTEL conversation or session file.
type remoteSessionStore struct {
	mu   sync.Mutex
	path string
	data map[string]string // sandbox name → session UUID
}

func newRemoteSessionStore(configDir string) *remoteSessionStore {
	path := filepath.Join(configDir, "remote-sessions.json")
	s := &remoteSessionStore{path: path, data: make(map[string]string)}
	s.load()
	return s
}

func (s *remoteSessionStore) Get(sandboxName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[sandboxName]
}

func (s *remoteSessionStore) Put(sandboxName, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[sandboxName] = sessionID
	s.save()
}

func (s *remoteSessionStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, &s.data)
	debuglog.Log("remote-sessions: loaded %d entries from %s", len(s.data), s.path)
}

func (s *remoteSessionStore) save() {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		debuglog.Log("remote-sessions: save failed: %v", err)
	}
}

// aimuxConfigDir returns ~/.aimux, the standard config directory.
func aimuxConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".aimux")
	}
	return ".aimux"
}
