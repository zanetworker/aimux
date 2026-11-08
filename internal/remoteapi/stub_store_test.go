package remoteapi

import (
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
)

// Claude's store is what remote serve registers.
var _ Store = (*provider.Claude)(nil)

// stubStore is a Store over injected functions: the sessions of
// Discover over ProjectsDir, files found under ProjectsDir, Tail or
// else the whole ParseTrace. name "" is Claude's provider name.
type stubStore struct {
	name        string
	ProjectsDir string
	Discover    func(opts history.DiscoverOpts, projectsDir string) ([]history.Session, error)
	ParseTrace  func(path string) ([]trace.Turn, error)
	Tail        func(path string, n int) ([]trace.Turn, error)
}

func (s *stubStore) Name() string {
	if s.name == "" {
		return "claude"
	}
	return s.name
}

func (s *stubStore) SessionFile(id string) string {
	return discovery.FindSessionFile(id, s.ProjectsDir)
}

func (s *stubStore) SessionCwd(path string) (string, error) { return history.SessionCwd(path) }

func (s *stubStore) Fingerprint(string) string { return "" }

func (s *stubStore) Sessions(opts history.DiscoverOpts) ([]history.Session, error) {
	if s.Discover == nil {
		return nil, nil
	}
	return s.Discover(opts, s.ProjectsDir)
}

func (s *stubStore) ParseTail(path string, n int) ([]trace.Turn, error) {
	if s.Tail != nil {
		return s.Tail(path, n)
	}
	return s.ParseTrace(path)
}

// stubProviders registers st (and live, nil for none) as the index's one
// provider.
func stubProviders(st *stubStore, live harness.LiveSessions) []Provider {
	return []Provider{{Store: st, Live: live}}
}

// stubOf is the stub store of an index built with stubProviders.
func stubOf(x *LocalIndex) *stubStore { return x.Providers[0].Store.(*stubStore) }
