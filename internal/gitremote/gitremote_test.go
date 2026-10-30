package gitremote

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWebURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"git@github.com:acme/widget.git", "https://github.com/acme/widget", true},
		{"https://github.com/acme/widget", "https://github.com/acme/widget", true},
		{"https://github.com/acme/widget.git", "https://github.com/acme/widget", true},
		{"https://github.com/acme/widget/", "https://github.com/acme/widget", true},
		{"ssh://git@github.com/acme/widget.git", "https://github.com/acme/widget", true},
		{"ssh://git@github.com:22/acme/widget.git", "https://github.com/acme/widget", true},
		// http(s) keeps its scheme and a non-default port; ssh and scp forms become https
		{"http://gitlab.internal:8080/g/r", "http://gitlab.internal:8080/g/r", true},
		{"http://gitlab.internal/g/r.git", "http://gitlab.internal/g/r", true},
		{"https://gitlab.example.com:8443/g/r.git", "https://gitlab.example.com:8443/g/r", true},
		{"https://GitLab.Example.com:443/g/r", "https://gitlab.example.com/g/r", true},
		{"http://gitlab.internal:80/g/r", "http://gitlab.internal/g/r", true},
		{"http://user:tok@gitlab.internal:8080/g/r.git", "http://gitlab.internal:8080/g/r", true},
		{"ssh://git@gitlab.internal:2222/g/r.git", "https://gitlab.internal/g/r", true},
		{"git://github.com/acme/widget.git", "https://github.com/acme/widget", true},
		{"git@gitlab.com:acme/widget.git", "https://gitlab.com/acme/widget", true},
		{"git@gitlab.example.com:group/sub/widget.git", "https://gitlab.example.com/group/sub/widget", true},
		{"https://gitlab.example.com/group/sub/widget.git", "https://gitlab.example.com/group/sub/widget", true},
		{"  git@github.com:acme/widget.git\n", "https://github.com/acme/widget", true},
		// credentials never reach the web URL
		{"https://user:token@github.com/acme/widget.git", "https://github.com/acme/widget", true},
		{"https://token@gitlab.example.com/g/widget", "https://gitlab.example.com/g/widget", true},
		// unknown host, local paths, junk
		{"git@bitbucket.org:acme/widget.git", "", false},
		{"host:path@name", "", false}, // the colon only before the @: not scp-like, never a panic
		{"https://example.com/acme/widget.git", "", false},
		{"/srv/git/widget.git", "", false},
		{"", "", false},
		{"git@github.com:widget.git", "", false}, // no owner
		{"https://github.com/", "", false},
		{"git@git hub.com:acme/widget.git", "", false},
		{"git@evil.com/x#github.com:acme/widget.git", "", false},
		{"user@host_name:acme/widget.git", "", false},
	}
	for _, tc := range tests {
		got, ok := WebURL(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("WebURL(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test helper running git in a temp dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestLookup(t *testing.T) {
	t.Run("repo with a remote", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q", "-b", "feature/x")
		git(t, dir, "remote", "add", "origin", "https://user:tok@github.com/acme/widget.git")
		git(t, dir, "-c", "user.name=t", "-c", "user.email=t@e.x", "commit", "-q", "--allow-empty", "-m", "init")
		web, branch, root, ok := Lookup(dir)
		want, _ := filepath.EvalSymlinks(dir)
		if !ok || web != "https://github.com/acme/widget" || branch != "feature/x" || root != want {
			t.Fatalf("Lookup = %q, %q, %q, %v; want root %q", web, branch, root, ok, want)
		}
	})
	t.Run("subfolder reports the repo root", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
		sub := filepath.Join(dir, "internal")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, root, ok := Lookup(sub)
		want, _ := filepath.EvalSymlinks(dir)
		if !ok || root != want {
			t.Fatalf("root = %q, %v; want %q", root, ok, want)
		}
	})
	t.Run("insteadOf is applied", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "remote", "add", "origin", "gh:acme/widget.git")
		git(t, dir, "config", "url.git@github.com:.insteadOf", "gh:")
		if web, _, _, ok := Lookup(dir); !ok || web != "https://github.com/acme/widget" {
			t.Fatalf("Lookup = %q, %v", web, ok)
		}
	})
	t.Run("no remote", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		if web, _, _, ok := Lookup(dir); ok {
			t.Fatalf("Lookup with no remote = %q, true", web)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
		if _, _, _, ok := Lookup(t.TempDir()); ok {
			t.Fatal("Lookup outside a repo = true")
		}
	})
	t.Run("empty and missing dir", func(t *testing.T) {
		if _, _, _, ok := Lookup(""); ok {
			t.Fatal("empty dir = true")
		}
		if _, _, _, ok := Lookup("/nonexistent/dir/xyz"); ok {
			t.Fatal("missing dir = true")
		}
	})
	t.Run("unknown host", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "remote", "add", "origin", "git@bitbucket.org:acme/widget.git")
		if _, _, _, ok := Lookup(dir); ok {
			t.Fatal("unknown host = true")
		}
	})
	t.Run("cached for a minute", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
		if _, _, _, ok := Lookup(dir); !ok {
			t.Fatal("first lookup failed")
		}
		git(t, dir, "remote", "remove", "origin")
		if _, _, _, ok := Lookup(dir); !ok {
			t.Fatal("second lookup within 60 s should hit the cache")
		}
		now := cacheNow
		defer func() { cacheNow = now }()
		cacheNow = func() time.Time { return now().Add(61 * time.Second) }
		if _, _, _, ok := Lookup(dir); ok {
			t.Fatal("lookup after the TTL should re-read git")
		}
	})
}

// Writing an entry sweeps out the ones past their TTL, so the cache does not
// grow with every folder ever asked about.
func TestLookupPrunesExpiredEntries(t *testing.T) {
	now := cacheNow
	defer func() { cacheNow = now }()
	mu.Lock()
	saved := cache
	cache = map[string]entry{}
	mu.Unlock()
	defer func() { mu.Lock(); cache = saved; mu.Unlock() }()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cacheNow = func() time.Time { return base }
	stale, fresh := t.TempDir(), t.TempDir()
	Lookup(stale)
	cacheNow = func() time.Time { return base.Add(30 * time.Second) }
	Lookup(fresh)
	cacheNow = func() time.Time { return base.Add(cacheTTL + time.Second) } // stale expired, fresh not
	Lookup(t.TempDir())

	mu.Lock()
	defer mu.Unlock()
	if _, hit := cache[stale]; hit {
		t.Fatal("expired entry still cached after a write")
	}
	if _, hit := cache[fresh]; !hit {
		t.Fatal("unexpired entry was pruned")
	}
	if len(cache) != 2 {
		t.Fatalf("cache has %d entries, want 2 (fresh and the new one)", len(cache))
	}
}
