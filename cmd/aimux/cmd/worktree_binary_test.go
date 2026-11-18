package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeBinary(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	clone := filepath.Join(root, "clone")
	for _, d := range []string{filepath.Join(wt, "bin"), filepath.Join(clone, ".git"), filepath.Join(clone, "bin"), filepath.Join(root, "aimux-worktrees", "x")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link outside the worktree to a build inside it counts too.
	target := filepath.Join(wt, "bin", "aimux")
	if err := os.WriteFile(target, []byte("bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "aimux-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if !worktreeBinary(link) {
		t.Error("a symlink to a worktree build passed")
	}
	for bin, want := range map[string]bool{
		filepath.Join(wt, "bin", "aimux"):                    true, // a .git file above: a git worktree
		filepath.Join(root, "aimux-worktrees", "x", "aimux"): true,
		filepath.Join(clone, "bin", "aimux"):                 false, // a .git directory: the main clone
		filepath.Join(root, "aimux"):                         false,
	} {
		if got := worktreeBinary(bin); got != want {
			t.Errorf("worktreeBinary(%s) = %v, want %v", bin, got, want)
		}
	}
}

// Installing from a worktree binary warns and needs the flag, also when
// it is the aimux on PATH.
func TestGuardWorktreeBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "aimux-worktrees", "f", "aimux")
	var w bytes.Buffer
	if err := guardWorktreeBinary(bin, false, &w); err == nil || !strings.Contains(err.Error(), "--allow-worktree-binary") {
		t.Fatalf("err %v, want a refusal naming the flag", err)
	}
	if !strings.Contains(w.String(), "worktree") {
		t.Fatalf("no warning: %q", w.String())
	}
	w.Reset()
	if err := guardWorktreeBinary(bin, true, &w); err != nil || !strings.Contains(w.String(), "worktree") {
		t.Fatalf("allowed: err %v, warning %q", err, w.String())
	}
	// A binary outside any worktree needs nothing.
	w.Reset()
	if err := guardWorktreeBinary(filepath.Join(t.TempDir(), "aimux"), false, &w); err != nil || w.Len() != 0 {
		t.Fatalf("outside a worktree: err %v, output %q", err, w.String())
	}
}

// A link to a worktree build needs the flag: the job would break with the
// worktree all the same.
func TestGuardWorktreeBinary_PathLinkIntoAWorktree(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "aimux-worktrees", "f", "aimux")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "aimux")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := guardWorktreeBinary(link, false, &bytes.Buffer{}); err == nil {
		t.Fatal("a PATH link into a worktree passed")
	}
}
