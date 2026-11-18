package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// worktreeBinary reports whether bin is a build inside a git worktree: a
// directory above it holds a .git file (not the main clone's .git
// directory), or its path runs through aimux-worktrees. Such a binary goes
// away with the worktree, and a launchd job left pointing at it stops.
func worktreeBinary(bin string) bool {
	// The path launchd runs and, through a symlink, the build it reaches.
	if real, err := filepath.EvalSymlinks(bin); err == nil && real != bin && inWorktree(real) {
		return true
	}
	return inWorktree(bin)
}

func inWorktree(bin string) bool {
	if strings.Contains(filepath.ToSlash(bin), "/aimux-worktrees/") {
		return true
	}
	for dir := filepath.Dir(bin); ; {
		if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return fi.Mode().IsRegular()
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// guardWorktreeBinary warns on w when an install runs from a binary inside
// a git worktree (other than the main clone), on PATH or not, and refuses
// it unless allow.
func guardWorktreeBinary(bin string, allow bool, w io.Writer) error {
	if !worktreeBinary(bin) {
		return nil
	}
	_, _ = fmt.Fprintf(w, "Warning: %s is a build inside a git worktree; the launchd job stops working when the worktree is removed.\n", bin)
	if !allow {
		return fmt.Errorf("installing from a worktree binary (%s) needs --allow-worktree-binary; or install aimux (make install, go install) and run it from there", bin)
	}
	return nil
}
