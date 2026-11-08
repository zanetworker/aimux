package gitinfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test helper, fixed command
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func real(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	top, common, err := Top(ctx, sub)
	if err != nil || real(t, top) != real(t, dir) || real(t, common) != real(t, filepath.Join(dir, ".git")) {
		t.Fatalf("Top = %q, %q, %v", top, common, err)
	}
	if _, _, err := Top(ctx, t.TempDir()); err == nil {
		t.Fatal("Top outside a repo = nil error")
	}
	if _, _, err := Top(ctx, "/nonexistent/dir/xyz"); err == nil {
		t.Fatal("Top of a missing dir = nil error")
	}
}

func TestTopIsRememberedForAMinute(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if _, _, err := Top(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Top(ctx, dir); err != nil {
		t.Fatalf("second Top within the TTL should not run git: %v", err)
	}
	now := topNow
	defer func() { topNow = now }()
	topNow = func() time.Time { return now().Add(topTTL + time.Second) }
	if _, _, err := Top(ctx, dir); err == nil {
		t.Fatal("Top after the TTL should read git again")
	}
}

func TestOriginAndBranch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if _, err := Origin(ctx, dir); err == nil {
		t.Fatal("Origin with no remote = nil error")
	}
	git(t, dir, "remote", "add", "origin", "gh:acme/w.git")
	git(t, dir, "config", "url.git@github.com:.insteadOf", "gh:")
	if o, err := Origin(ctx, dir); err != nil || o != "git@github.com:acme/w.git" {
		t.Fatalf("Origin = %q, %v", o, err)
	}
	if b, err := Branch(ctx, dir); err != nil || b != "main" { // no commit yet
		t.Fatalf("Branch on an unborn branch = %q, %v", b, err)
	}
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "x")
	git(t, dir, "checkout", "-q", "--detach")
	if b, _ := Branch(ctx, dir); b != "HEAD" {
		t.Fatalf("detached Branch = %q, want HEAD", b)
	}
}
