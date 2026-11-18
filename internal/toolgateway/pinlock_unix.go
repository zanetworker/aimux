//go:build unix

package toolgateway

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPins takes an exclusive lock on path, retrying for up to pinLockWait
// while another holder (aimux tools approve, the remote API) has it, then
// fails with ErrPinsBusy; unlock releases it.
func lockPins(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is the pins file's own lock
	if err != nil {
		return nil, fmt.Errorf("tool pins lock: %w", err)
	}
	deadline := time.Now().Add(pinLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: an open file descriptor is a small non-negative int
		if err == nil {
			return func() { _ = f.Close() }, nil // closing releases the lock
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, ErrPinsBusy
			}
			return nil, fmt.Errorf("tool pins lock %s: %w", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
