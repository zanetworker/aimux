//go:build unix

package secretref

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether fi belongs to the user running aimux.
func ownedByCurrentUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
