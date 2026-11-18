package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// gatewayLockWait is how long a gateway install or uninstall waits for
// another one to finish before giving up.
var gatewayLockWait = 3 * time.Second

// withGatewayLifecycle runs fn holding ~/.aimux/gateway-install.lock, the
// lock every gateway install (with its rollback) and uninstall takes, so an
// uninstall cannot run while an install waits for its worker and then be
// undone by that install's rollback.
func withGatewayLifecycle(home string, fn func() error) error {
	path := filepath.Join(home, ".aimux", "gateway-install.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: aimux's own lock file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // closing releases the lock
	deadline := time.Now().Add(gatewayLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: an open file descriptor is a small non-negative int
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			return errors.New("another aimux mcp proxy gateway install or uninstall is running; try again when it finishes")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fn()
}
