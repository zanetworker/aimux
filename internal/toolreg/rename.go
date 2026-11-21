package toolreg

import (
	"errors"
	"fmt"
	"syscall"
)

// renameNoClobber moves from to to with atomic (an exclusive rename). A
// filesystem without one is refused, nothing moved: a two-step fallback
// could delete a file another writer saved between the steps.
func renameNoClobber(from, to string, atomic func(from, to string) error) error {
	err := atomic(from, to)
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EOPNOTSUPP) {
		return fmt.Errorf("move %s to %s: this filesystem has no no-replace rename, so nothing was moved: %w", from, to, err)
	}
	return err
}
