package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Compile-time check.
var _ Store = (*FileStore)(nil)

// FileStore implements Store backed by a JSON file.
type FileStore struct {
	mu   sync.RWMutex
	path string
	data map[string]*Session
}

// NewFileStore creates a file-backed session store.
// If the file exists, sessions are loaded from it.
func NewFileStore(path string) *FileStore {
	s := &FileStore{
		path: path,
		data: make(map[string]*Session),
	}
	s.load()
	return s
}

func (s *FileStore) Create(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.data[sess.ID]; exists {
		return fmt.Errorf("session %q already exists", sess.ID)
	}
	cp := *sess
	s.data[sess.ID] = &cp
	return s.saveLocked()
}

func (s *FileStore) Get(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	cp := *sess
	return &cp, nil
}

func (s *FileStore) List() ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Session, 0, len(s.data))
	for _, sess := range s.data {
		cp := *sess
		result = append(result, &cp)
	}
	return result, nil
}

func (s *FileStore) Update(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.data[sess.ID]; !exists {
		return fmt.Errorf("session %q not found", sess.ID)
	}
	cp := *sess
	s.data[sess.ID] = &cp
	return s.saveLocked()
}

func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.data[id]; !exists {
		return fmt.Errorf("session %q not found", id)
	}
	delete(s.data, id)
	return s.saveLocked()
}

func (s *FileStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var data map[string]*Session
	if json.Unmarshal(raw, &data) == nil && data != nil {
		s.data = data
	}
}

func (s *FileStore) saveLocked() error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return fmt.Errorf("marshal sessions: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	return os.WriteFile(s.path, raw, 0o600)
}
