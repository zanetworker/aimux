//go:build unix

package toolgateway

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockControl takes an exclusive lock on path, held until the file is
// closed, so two starting gateways cannot both replace the socket.
func lockControl(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is the gateway's own lock file
	if err != nil {
		return nil, fmt.Errorf("control socket lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: an open file descriptor is a small non-negative int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another tool gateway is running (%s is locked)", path)
		}
		return nil, fmt.Errorf("control socket lock: %w", err)
	}
	return f, nil
}
