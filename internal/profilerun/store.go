package profilerun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/runcheck"
)

// Meta is what aimux records about a session started under a profile.
type Meta struct {
	Profile, Digest, Client, URL, Title string
	Created                             time.Time
	// Hidden: the owner removed it from the lists (Wingman's history
	// delete); FindPage never continues it.
	Hidden bool `json:",omitempty"`
	// Lost: a run session recorded before a restart; the run engine keeps
	// its sessions in memory, so it cannot continue. It stays in history,
	// but FindPage never continues it.
	Lost bool `json:",omitempty"`
	// Started: the first turn's claude showed the session in its stream, so
	// claude has created the session id and later turns resume it (sandbox
	// sessions).
	Started bool `json:",omitempty"`
	// Instance is the browser profile that started it (Wingman's per-profile
	// id); empty for sessions from before instances, and for other clients.
	Instance string `json:",omitempty"`
	// WithPage: the session was started with page text (bound on its first
	// turn). FindPage continues it only for a request that also has page
	// text, so a question asked without the page never reaches that text.
	// False for sessions from before the field.
	WithPage bool `json:",omitempty"`
	// OriginOnly: the first prompt named only the URL's origin (Wingman,
	// a private page with "Use this page" off). Keyed like WithPage, so a
	// question asked one way never continues a session started the other.
	OriginOnly bool `json:",omitempty"`
	// Run is a research-with-work session's stage state (run engine);
	// nil for every other session and for files from before runs.
	Run *RunState `json:",omitempty"`
}

// Run stages, in order; a run ends in StageDone or StageFailed.
const (
	StagePlanning = "planning"
	StageChecking = "checking"
	StageReading  = "reading"
	StageWriting  = "writing"
	StageDone     = "done"
	StageFailed   = "failed"
)

// RunState is where a run session's latest turn got to: its stage, the
// child sessions each stage ran in, the queries the check accepted and why
// it rejected the others (reasons only, never the text), the sources of
// outside-written content the turn read (attempted and succeeded), and the
// links its answer may show (pages the reader fetched).
type RunState struct {
	Stage            string               `json:"stage"`
	Children         map[string]string    `json:"children,omitempty"`
	Accepted         []string             `json:"accepted,omitempty"`
	Rejected         []runcheck.Rejection `json:"rejected,omitempty"`
	OutsideAttempted []string             `json:"outsideAttempted,omitempty"`
	OutsideSucceeded []string             `json:"outsideSucceeded,omitempty"`
	Links            []string             `json:"links,omitempty"`
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

// FindPage returns the latest session client's instance started on url
// under profile, with page text exactly when withPage and an origin-only
// URL exactly when originOnly, that is not hidden. instance "" matches only
// sessions recorded without one (legacy), so an instance never continues
// another's session or a legacy one. Ties on Created go to the larger
// session id, so the answer is stable.
func (s *Store) FindPage(client, instance, url, profile string, withPage, originOnly bool) (session string, ok bool) {
	var best Meta
	for id, m := range s.List(client) {
		if m.Instance != instance || m.URL != url || m.Profile != profile || m.WithPage != withPage || m.OriginOnly != originOnly || m.Hidden || m.Lost {
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

// SetRun records session's run state, keeping the rest of its Meta. A
// session not in the store is an error.
func (s *Store) SetRun(session string, r RunState) error {
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
	m.Run = &r
	all[session] = m
	return s.save(all)
}
