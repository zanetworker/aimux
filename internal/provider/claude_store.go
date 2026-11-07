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

var (
	_ TranscriptRoot = (*Claude)(nil)
	_ LiveSource     = (*Claude)(nil)
)

// TranscriptsDir is ProjectsDir, or ~/.claude/projects (the relative
// .claude/projects when the home directory is unknown).
func (c *Claude) TranscriptsDir() string { return c.projectsDir() }

// LiveSessions reads Claude Code's session registry under RegistryDir, or
// ~/.claude/sessions, caching a read for ttl (discovery.LiveCache).
func (c *Claude) LiveSessions(ttl time.Duration) harness.LiveSessions {
	dir := c.RegistryDir
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".claude", "sessions")
		}
	}
	return discovery.NewLiveCache(dir, ttl)
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
