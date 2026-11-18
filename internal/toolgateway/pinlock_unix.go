//go:build unix

package toolgateway

import (
	"fmt"
	"os"
	"syscall"
)

// lockPins takes an exclusive lock on path, waiting for another holder
// (aimux tools approve, the remote API) to finish; unlock releases it.
func lockPins(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is the pins file's own lock
	if err != nil {
		return nil, fmt.Errorf("tool pins lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: an open file descriptor is a small non-negative int
		_ = f.Close()
		return nil, fmt.Errorf("tool pins lock %s: %w", path, err)
	}
	return func() { _ = f.Close() }, nil // closing releases the lock
}
