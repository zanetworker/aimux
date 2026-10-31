package folders

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeGit struct {
	mu    sync.Mutex
	repos map[string][2]string // dir -> top, commonDir
	calls []string
}

func (g *fakeGit) run(dir string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, dir)
	if v, ok := g.repos[dir]; ok {
		return v[0], v[1], nil
	}
	return "", "", errors.New("not a git repository")
}

func (g *fakeGit) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func newTestResolver(t *testing.T, repos map[string][2]string) (*Resolver, *fakeGit) {
	t.Helper()
	g := &fakeGit{repos: repos}
	r := NewResolver("/home/me", filepath.Join(t.TempDir(), "folders.json"))
	r.Git = g.run
	r.Exists = func(string) bool { return true }
	return r, g
}

func TestFolder_MainCheckoutAndSub(t *testing.T) {
	r, _ := newTestResolver(t, map[string][2]string{
		"/src/aimux/clients/ios": {"/src/aimux", "/src/aimux/.git"},
		"/src/aimux":             {"/src/aimux", "/src/aimux/.git"},
	})
	r.ResolveNow("/src/aimux/clients/ios")
	r.ResolveNow("/src/aimux")

	want := Folder{Repo: "aimux", RepoPath: "/src/aimux", Sub: "clients/ios", Label: "aimux · clients/ios"}
	if got := r.Folder("/src/aimux/clients/ios"); got != want {
		t.Errorf("sub: got %+v want %+v", got, want)
	}
	wantTop := Folder{Repo: "aimux", RepoPath: "/src/aimux", Label: "aimux"}
	if got := r.Folder("/src/aimux"); got != wantTop {
		t.Errorf("top: got %+v want %+v", got, wantTop)
	}
}

func TestFolder_LinkedWorktree(t *testing.T) {
	r, _ := newTestResolver(t, map[string][2]string{
		"/wt/clients":     {"/wt/clients", "/src/aimux/.git"},
		"/wt/clients/ios": {"/wt/clients", "/src/aimux/.git"},
	})
	r.ResolveNow("/wt/clients")
	r.ResolveNow("/wt/clients/ios")

	want := Folder{Repo: "aimux", RepoPath: "/src/aimux", Worktree: "clients", Label: "aimux / clients"}
	if got := r.Folder("/wt/clients"); got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
	wantSub := Folder{Repo: "aimux", RepoPath: "/src/aimux", Worktree: "clients", Sub: "ios", Label: "aimux / clients · ios"}
	if got := r.Folder("/wt/clients/ios"); got != wantSub {
		t.Errorf("got %+v want %+v", got, wantSub)
	}
}

func TestFolder_NonGit(t *testing.T) {
	r, _ := newTestResolver(t, nil)
	r.ResolveNow("/home/me/_experiments/x")
	r.ResolveNow("/home/me")

	want := Folder{Repo: "_experiments", RepoPath: "/home/me/_experiments", Worktree: "x", Label: "_experiments / x"}
	if got := r.Folder("/home/me/_experiments/x"); got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
	home := Folder{Repo: "Home", RepoPath: "/home/me", Label: "Home"}
	if got := r.Folder("/home/me"); got != home {
		t.Errorf("home: got %+v want %+v", got, home)
	}
}

func TestFolder_TrailingSlashAndEmpty(t *testing.T) {
	r, _ := newTestResolver(t, nil)
	if got := r.Folder("/home/me/"); got.Label != "Home" {
		t.Errorf("trailing slash: got %+v", got)
	}
	if got := r.Folder(""); got != (Folder{}) {
		t.Errorf("empty path: got %+v want zero", got)
	}
}

func TestFolder_SameNamesDisambiguated(t *testing.T) {
	r, _ := newTestResolver(t, map[string][2]string{
		"/a/aimux": {"/a/aimux", "/a/aimux/.git"},
		"/b/aimux": {"/b/aimux", "/b/aimux/.git"},
	})
	for _, p := range []string{"/a/aimux", "/b/aimux", "/home/me/work/src/x", "/home/me/play/src/y"} {
		r.ResolveNow(p)
	}
	cases := map[string]string{
		"/a/aimux":            "a / aimux",
		"/b/aimux":            "b / aimux",
		"/home/me/work/src/x": "work / src",
		"/home/me/play/src/y": "play / src",
	}
	for p, want := range cases {
		if got := r.Folder(p).Repo; got != want {
			t.Errorf("%s: Repo %q want %q", p, got, want)
		}
	}
	if got := r.Folder("/a/aimux").Label; got != "a / aimux" {
		t.Errorf("label should use the disambiguated repo, got %q", got)
	}
}

func TestFolder_LoneNameNeverPrefixed(t *testing.T) {
	r, _ := newTestResolver(t, map[string][2]string{
		"/a/aimux":      {"/a/aimux", "/a/aimux/.git"},
		"/wt/other-one": {"/wt/other-one", "/a/aimux/.git"},
	})
	r.ResolveNow("/a/aimux")
	r.ResolveNow("/wt/other-one") // same RepoPath, not a second repo
	if got := r.Folder("/a/aimux").Repo; got != "aimux" {
		t.Errorf("got %q want aimux", got)
	}
	if got := r.Folder("/wt/other-one").Repo; got != "aimux" {
		t.Errorf("got %q want aimux", got)
	}
}

func TestFolder_UnresolvedUsesNonGitRuleUntilRun(t *testing.T) {
	r, g := newTestResolver(t, map[string][2]string{
		"/src/aimux/clients": {"/src/aimux", "/src/aimux/.git"},
	})
	got := r.Folder("/src/aimux/clients")
	want := Folder{Repo: "aimux", RepoPath: "/src/aimux", Worktree: "clients", Label: "aimux / clients"}
	if got != want {
		t.Errorf("non-git rule: got %+v want %+v", got, want)
	}
	if n := g.callCount(); n != 0 {
		t.Fatalf("Folder ran git %d times", n)
	}

	r.ResolveNow("/src/aimux/clients")
	got = r.Folder("/src/aimux/clients")
	want = Folder{Repo: "aimux", RepoPath: "/src/aimux", Sub: "clients", Label: "aimux · clients"}
	if got != want {
		t.Errorf("git rule: got %+v want %+v", got, want)
	}
}

func TestRun_ResolvesQueuedPathsInBackground(t *testing.T) {
	r, g := newTestResolver(t, map[string][2]string{
		"/src/aimux/clients": {"/src/aimux", "/src/aimux/.git"},
	})
	r.Tick = 5 * time.Millisecond
	r.Folder("/src/aimux/clients")
	r.Folder("/src/aimux/clients") // queued once only

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for r.Folder("/src/aimux/clients").Sub == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if got := r.Folder("/src/aimux/clients"); got.Sub != "clients" {
		t.Errorf("not resolved by Run: %+v", got)
	}
	if n := g.callCount(); n != 1 {
		t.Errorf("git calls = %d, want 1", n)
	}
}

func TestRun_AtMostFourPerTick(t *testing.T) {
	repos := map[string][2]string{}
	var paths []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		p := "/src/" + n
		repos[p] = [2]string{p, p + "/.git"}
		paths = append(paths, p)
	}
	r, g := newTestResolver(t, repos)
	for _, p := range paths {
		r.Folder(p)
	}
	r.step()
	if n := g.callCount(); n != 4 {
		t.Errorf("after one tick: %d git calls, want 4", n)
	}
	r.step()
	if n := g.callCount(); n != 6 {
		t.Errorf("after two ticks: %d git calls, want 6", n)
	}
}

func TestFolder_GonePathNotResolved(t *testing.T) {
	r, g := newTestResolver(t, map[string][2]string{
		"/src/aimux/clients": {"/src/aimux", "/src/aimux/.git"},
	})
	r.Exists = func(string) bool { return false }
	r.ResolveNow("/src/aimux/clients")
	if n := g.callCount(); n != 0 {
		t.Errorf("git ran on a missing path")
	}
	if got := r.Folder("/src/aimux/clients"); got.Worktree != "clients" {
		t.Errorf("want non-git fallback, got %+v", got)
	}
}

func TestFolder_CacheSurvivesDeletedWorktree(t *testing.T) {
	r, _ := newTestResolver(t, map[string][2]string{
		"/wt/clients/ios": {"/wt/clients", "/src/aimux/.git"},
	})
	r.ResolveNow("/wt/clients/ios")
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	want := r.Folder("/wt/clients/ios")

	r2 := NewResolver("/home/me", r.CachePath)
	r2.Exists = func(string) bool { return false }
	r2.Git = func(string) (string, string, error) { return "", "", errors.New("gone") }
	if got := r2.Folder("/wt/clients/ios"); got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestFolder_CacheFileIsOwnerOnly(t *testing.T) {
	r, _ := newTestResolver(t, map[string][2]string{
		"/src/aimux": {"/src/aimux", "/src/aimux/.git"},
	})
	r.ResolveNow("/src/aimux")
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(r.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode %o want 600", mode)
	}
	entries, _ := os.ReadDir(filepath.Dir(r.CachePath))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %d entries", len(entries))
	}
}

func TestNewResolver_CorruptCacheIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "folders.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewResolver("/home/me", p)
	if got := r.Folder("/home/me"); got.Label != "Home" {
		t.Errorf("got %+v", got)
	}
}

func TestGitTop_RealRepoAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "proj")
	wt := filepath.Join(root, "proj-wt")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...) //nolint:gosec // fixed git args in a temp repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "x")
	git("worktree", "add", "-q", wt)

	r := NewResolver("/home/me", filepath.Join(root, "cache.json"))
	r.ResolveNow(filepath.Join(repo, "sub"))
	r.ResolveNow(wt)
	if got := r.Folder(filepath.Join(repo, "sub")); got.Label != "proj · sub" || got.RepoPath != repo {
		t.Errorf("main: %+v", got)
	}
	if got := r.Folder(wt); got.Label != "proj / proj-wt" || got.RepoPath != repo {
		t.Errorf("worktree: %+v", got)
	}
}
