// Package folders gives every session path one folder identity: the git
// repository, the linked worktree and the subfolder it runs in. Clients print
// Folder.Label and never derive names themselves.
package folders

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	perTick     = 4
	defaultTick = 250 * time.Millisecond
	gitTimeout  = 5 * time.Second
)

// Folder is the identity of a session's working directory.
type Folder struct {
	Repo     string `json:"repo"`               // shown name of the top folder
	RepoPath string `json:"repoPath"`           // its full path, the grouping key
	Worktree string `json:"worktree,omitempty"` // linked worktree folder name; absent for the main checkout
	Sub      string `json:"sub,omitempty"`      // path below the worktree top, "/"-separated
	Label    string `json:"label"`              // "aimux / clients · ios"
}

// cached is what survives a restart for a path that git resolved: enough to
// rebuild the Folder after the worktree itself is deleted.
type cached struct {
	Top      string `json:"top"`      // git top-level of the path
	RepoPath string `json:"repoPath"` // main checkout of the repository
}

// Resolver maps session paths to Folders. Folder never runs git; Run does.
type Resolver struct {
	Home      string
	CachePath string
	// Git returns the top-level and the common git dir (both absolute) of dir.
	Git    func(dir string) (top, commonDir string, err error)
	Exists func(string) bool
	// Tick is how often Run resolves a batch; zero means the default.
	Tick time.Duration

	mu     sync.Mutex
	known  map[string]cached   // git-resolved paths
	nonGit map[string]struct{} // paths git said no to, or that are gone
	queued map[string]struct{}
	queue  []string
	repos  map[string]struct{} // every RepoPath seen, for name collisions
	dirty  bool
}

// NewResolver builds a Resolver for the given home directory and cache file,
// loading the cache when it exists and parses.
func NewResolver(home, cachePath string) *Resolver {
	r := &Resolver{
		Home:      filepath.Clean(home),
		CachePath: cachePath,
		Git:       gitTop,
		Exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		known:  map[string]cached{},
		nonGit: map[string]struct{}{},
		queued: map[string]struct{}{},
		repos:  map[string]struct{}{},
	}
	if b, err := os.ReadFile(cachePath); err == nil { //nolint:gosec // cache path is chosen by the caller
		var m map[string]cached
		if json.Unmarshal(b, &m) == nil {
			for p, c := range m {
				r.known[p] = c
				r.repos[c.RepoPath] = struct{}{}
			}
		}
	}
	return r
}

// Folder returns the identity of path without running git. A path not yet
// resolved gets the non-git rule and is queued for Run.
func (r *Resolver) Folder(path string) Folder {
	if path == "" {
		return Folder{}
	}
	path = filepath.Clean(path)

	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.known[path]
	if !ok {
		if _, done := r.nonGit[path]; !done {
			if _, q := r.queued[path]; !q {
				r.queued[path] = struct{}{}
				r.queue = append(r.queue, path)
			}
		}
	}
	return r.build(path, c, ok)
}

// build assembles the Folder; r.mu must be held.
func (r *Resolver) build(path string, c cached, git bool) Folder {
	var f Folder
	switch {
	case git:
		f.RepoPath = c.RepoPath
		if c.Top != c.RepoPath {
			f.Worktree = filepath.Base(c.Top)
		}
		if rel, err := filepath.Rel(c.Top, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			f.Sub = filepath.ToSlash(rel)
		}
	case path == r.Home:
		f.RepoPath = path
	default:
		f.RepoPath = filepath.Dir(path)
		f.Worktree = filepath.Base(path)
	}
	f.Repo = r.repoName(f.RepoPath)

	f.Label = f.Repo
	if f.Worktree != "" {
		f.Label += " / " + f.Worktree
	}
	if f.Sub != "" {
		f.Label += " · " + f.Sub
	}
	return f
}

// repoName is the folder name of repoPath, with its parent's name prefixed
// only when a different known RepoPath shows the same name. r.mu must be held.
func (r *Resolver) repoName(repoPath string) string {
	if repoPath == r.Home {
		return "Home"
	}
	base := filepath.Base(repoPath)
	for other := range r.repos {
		if other != repoPath && other != r.Home && filepath.Base(other) == base {
			return filepath.Base(filepath.Dir(repoPath)) + " / " + base
		}
	}
	return base
}

// ResolveNow resolves one path synchronously. A path that no longer exists is
// left on the non-git rule, so a deleted worktree never resolved stays that way.
func (r *Resolver) ResolveNow(path string) {
	if path == "" {
		return
	}
	path = filepath.Clean(path)

	r.mu.Lock()
	_, have := r.known[path]
	r.mu.Unlock()
	if have {
		return
	}

	var (
		top, common string
		err         error
	)
	exists := r.Exists(path)
	if exists {
		top, common, err = r.Git(path)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if exists && err == nil {
		c := cached{Top: filepath.Clean(top), RepoPath: filepath.Dir(filepath.Clean(common))}
		r.known[path] = c
		r.repos[c.RepoPath] = struct{}{}
		r.dirty = true
	} else {
		r.nonGit[path] = struct{}{}
		if exists { // git said no: a real non-git folder, not a guess about a gone path
			r.repos[r.build(path, cached{}, false).RepoPath] = struct{}{}
		}
	}
}

// step resolves up to perTick queued paths and saves the cache if it changed.
func (r *Resolver) step() {
	r.mu.Lock()
	n := min(perTick, len(r.queue))
	batch := append([]string(nil), r.queue[:n]...)
	r.queue = r.queue[n:]
	for _, p := range batch {
		delete(r.queued, p)
	}
	r.mu.Unlock()

	for _, p := range batch {
		r.ResolveNow(p)
	}
	_ = r.save()
}

// Run resolves queued paths in the background until ctx ends.
func (r *Resolver) Run(ctx context.Context) {
	tick := r.Tick
	if tick <= 0 {
		tick = defaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		r.step()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// save writes the cache atomically (temp file, then rename) with mode 0600.
func (r *Resolver) save() error {
	r.mu.Lock()
	if !r.dirty || r.CachePath == "" {
		r.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(r.known)
	r.dirty = false
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err := r.write(b); err != nil {
		r.mu.Lock()
		r.dirty = true
		r.mu.Unlock()
		return err
	}
	return nil
}

func (r *Resolver) write(b []byte) error {

	dir := filepath.Dir(r.CachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".folders-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), r.CachePath)
}

// gitTop is the default Git: top-level and common dir of dir, both absolute.
func gitTop(dir string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel", "--git-common-dir").Output() //nolint:gosec // dir is a session path passed as an argument, no shell
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "", "", os.ErrNotExist
	}
	top, common := lines[0], lines[1]
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return top, common, nil
}
