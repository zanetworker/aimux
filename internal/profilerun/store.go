package profilerun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Meta is what aimux records about a session started under a profile.
type Meta struct {
	Profile, Digest, Client, URL, Title string
	Created                             time.Time
	// Hidden: the owner removed it from the lists (Wingman's history
	// delete); FindPage never continues it.
	Hidden bool `json:",omitempty"`
	// Started: the first turn's claude showed the session in its stream, so
	// claude has created the session id and later turns resume it (sandbox
	// sessions).
	Started bool `json:",omitempty"`
}

// Store keeps session Meta in one JSON file (0600, written atomically), by
// default ~/.aimux/sessions/profiles.json. The file is read on every call,
// so two Stores on one path agree.
type Store struct {
	Path string

	mu sync.Mutex // serialises read-modify-write within this process
}

// DefaultStorePath is ~/.aimux/sessions/profiles.json.
func DefaultStorePath(home string) string {
	return filepath.Join(home, ".aimux", "sessions", "profiles.json")
}

// Put records m for session.
func (s *Store) Put(session string, m Meta) error {
	if session == "" {
		return errors.New("profile session: empty session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	all[session] = m
	return s.save(all)
}

// Get returns session's Meta; ok is false for a session started without a
// profile or when the file cannot be read.
func (s *Store) Get(session string) (Meta, bool) {
	m, ok, err := s.lookup(session)
	return m, ok && err == nil
}

// lookup is Get that tells an unreadable file apart from a missing session.
func (s *Store) lookup(session string) (Meta, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Meta{}, false, err
	}
	m, ok := all[session]
	return m, ok, nil
}

func (s *Store) load() (map[string]Meta, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Meta{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string]Meta{}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path, err)
	}
	return all, nil
}

// save writes all to a temp file (CreateTemp makes it 0600) in the same directory and renames it
// over Path, so a reader never sees a half-written file.
func (s *Store) save(all map[string]Meta) error {
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".profiles-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op after the rename
	if _, err := f.Write(b); err != nil {
		_ = f.Close() // the earlier error is the one to report
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close() // the earlier error is the one to report
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// FindPage returns the latest session client started on url under profile
// that is not hidden. Ties on Created go to the larger session id, so the
// answer is stable.
func (s *Store) FindPage(client, url, profile string) (session string, ok bool) {
	var best Meta
	for id, m := range s.List(client) {
		if m.URL != url || m.Profile != profile || m.Hidden {
			continue
		}
		if !ok || m.Created.After(best.Created) || (m.Created.Equal(best.Created) && id > session) {
			session, best, ok = id, m, true
		}
	}
	return session, ok
}

// List returns client's sessions (an empty map when there are none or the
// file cannot be read).
func (s *Store) List(client string) map[string]Meta {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	out := map[string]Meta{}
	if err != nil {
		return out
	}
	for id, m := range all {
		if m.Client == client {
			out[id] = m
		}
	}
	return out
}

// All returns every client's sessions by id; an error when the file cannot
// be read (unlike List, which reads that as none).
func (s *Store) All() (map[string]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// MarkStarted records that session's first turn started (Meta.Started),
// keeping the rest of its Meta. A session not in the store is an error.
func (s *Store) MarkStarted(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	m, ok := all[session]
	if !ok {
		return fmt.Errorf("profile session %s is not recorded", session)
	}
	if m.Started {
		return nil
	}
	m.Started = true
	all[session] = m
	return s.save(all)
}
