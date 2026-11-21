package toolreg

import "golang.org/x/sys/unix"

// renameNoReplace moves from to to, failing with EEXIST when to exists.
func renameNoReplace(from, to string) error {
	return renameNoClobber(from, to, func(f, t string) error {
		return unix.Renameat2(unix.AT_FDCWD, f, unix.AT_FDCWD, t, unix.RENAME_NOREPLACE)
	})
}
