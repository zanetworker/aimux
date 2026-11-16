//go:build unix

package remoteapp

import (
	"os"
	"syscall"
)

// inode is fi's inode number, so a file replaced by rename is a change even
// when its time and size match.
func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino) //nolint:unconvert // Ino is uint64 on darwin, other widths elsewhere
	}
	return 0
}
