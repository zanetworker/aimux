//go:build unix

package keyring

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether fi belongs to the user running aimux.
func ownedByCurrentUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
