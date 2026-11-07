package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

var _ harness.SessionStore = (*Claude)(nil)

// projectsDir is ProjectsDir, or ~/.claude/projects.
func (c *Claude) projectsDir() string {
	if c.ProjectsDir != "" {
		return c.ProjectsDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "" // relative .claude/projects, as before the registry; never "" (a glob of "" reads the current dir)
	}
	return filepath.Join(home, ".claude", "projects")
}

// TranscriptRoot is the optional capability of a provider whose sessions
// are transcript files under one directory (the search index reads it).
type TranscriptRoot interface {
	TranscriptsDir() string
}

// LiveSource is the optional capability of a provider whose harness records
// which processes hold a session; ttl is the reader's cache time.
type LiveSource interface {
	LiveSessions(ttl time.Duration) harness.LiveSessions
}

// TailParser is the optional capability of parsing only the end of a
// transcript: at least the last n turns, numbered from the part read.
type TailParser interface {
	ParseTail(path string, n int) ([]trace.Turn, error)
}

var (
	_ TranscriptRoot = (*Claude)(nil)
	_ LiveSource     = (*Claude)(nil)
	_ TailParser     = (*Claude)(nil)
)

// TranscriptsDir is ProjectsDir, or ~/.claude/projects (the relative
// .claude/projects when the home directory is unknown).
func (c *Claude) TranscriptsDir() string { return c.projectsDir() }

// LiveSessions reads Claude Code's session registry under RegistryDir, or
// ~/.claude/sessions, caching a read for ttl (discovery.LiveCache). One
// reader per ttl, made on first use and kept: discovery reads the registry
// through the LiveTTL one (liveCache), so a refresh reads it and runs ps
// once.
func (c *Claude) LiveSessions(ttl time.Duration) harness.LiveSessions { return c.liveCache(ttl) }

func (c *Claude) liveCache(ttl time.Duration) *discovery.LiveCache {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	if lc, ok := c.lives[ttl]; ok {
		return lc
	}
	if c.lives == nil {
		c.lives = map[time.Duration]*discovery.LiveCache{}
	}
	lc := discovery.NewLiveCache(c.registryDir(), ttl)
	c.lives[ttl] = lc
	return lc
}

// registryDir is RegistryDir, or ~/.claude/sessions (the relative
// .claude/sessions when the home directory is unknown).
func (c *Claude) registryDir() string {
	if c.RegistryDir != "" {
		return c.RegistryDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "sessions")
}

// SessionFile is session id's transcript under the projects dir, or "".
func (c *Claude) SessionFile(id string) string {
	dir := c.projectsDir()
	if dir == "" {
		return ""
	}
	return discovery.FindSessionFile(id, dir)
}

// SessionCwd is the first working directory the transcript records
// (history.SessionCwd, the session list's rule).
func (c *Claude) SessionCwd(path string) (string, error) { return history.SessionCwd(path) }

// Fingerprint is the transcript's size and modification time, or "" when
// session id has none.
func (c *Claude) Fingerprint(id string) string {
	file := c.SessionFile(id)
	if file == "" {
		return ""
	}
	info, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// Sessions lists the transcripts under the projects dir (history.Discover);
// opts.Provider other than Claude's lists none.
func (c *Claude) Sessions(opts history.DiscoverOpts) ([]history.Session, error) {
	return history.Discover(opts, c.projectsDir())
}

// Reread is one session's record read again (history.Reread), so the
// session catalog can refresh one session without a full scan.
func (c *Claude) Reread(s history.Session) (history.Session, error) {
	return history.Reread(s)
}

// ParseTail returns at least the last n turns of the transcript at path,
// reading only the end of the file (ParseTraceTail).
func (c *Claude) ParseTail(path string, n int) ([]trace.Turn, error) {
	return c.ParseTraceTail(path, n)
}

// LiveOf is the harness.LiveSessions of every provider in ps that is a
// LiveSource, in order, each read with ttl: the state service's holders
// for the TUI and the dashboard.
func LiveOf(ps []Provider, ttl time.Duration) []harness.LiveSessions {
	var out []harness.LiveSessions
	for _, p := range ps {
		if ls, ok := p.(LiveSource); ok {
			out = append(out, ls.LiveSessions(ttl))
		}
	}
	return out
}
