// Package gitinfo is the one place aimux asks git about a folder: its
// top-level and common dir, its origin and its branch. folders and gitremote
// both read through it, so a folder costs one `rev-parse` however many of
// them ask.
package gitinfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const topTTL = 60 * time.Second

type topEntry struct {
	top, common string
	at          time.Time
}

var (
	mu     sync.Mutex
	tops   = map[string]topEntry{}
	topNow = time.Now
)

// run runs git in dir and returns its trimmed stdout.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed command; dir is a session folder passed as a -C argument
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Top returns the top-level and the common git dir of dir, both absolute.
// A success is remembered for a minute, so callers that ask about the same
// folder share one git call; failures are not remembered. gitremote caches its
// own result for a minute on top of this, so the root it reports can be up to
// ~120 s stale.
func Top(ctx context.Context, dir string) (top, common string, err error) {
	mu.Lock()
	if e, ok := tops[dir]; ok && topNow().Sub(e.at) < topTTL {
		mu.Unlock()
		return e.top, e.common, nil
	}
	mu.Unlock()
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return "", "", os.ErrNotExist
	}
	top, common = lines[0], lines[1]
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	mu.Lock()
	now := topNow()
	for d, e := range tops { // sweep expired entries
		if now.Sub(e.at) >= topTTL {
			delete(tops, d)
		}
	}
	tops[dir] = topEntry{top, common, now}
	mu.Unlock()
	return top, common, nil
}

// Origin returns dir's "origin" remote URL with insteadOf rewrites applied.
func Origin(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "remote", "get-url", "origin")
}

// Branch returns dir's current branch ("HEAD" when detached). A repository
// with no commit yet has no HEAD to resolve, so it falls back to the name
// the first commit will get.
func Branch(ctx context.Context, dir string) (string, error) {
	branch, err := run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		branch, err = run(ctx, dir, "symbolic-ref", "--short", "HEAD")
	}
	return branch, err
}
