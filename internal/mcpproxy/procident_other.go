//go:build !darwin

package mcpproxy

import (
	"errors"
	"syscall"
)

// processIdentity reports whether pid runs. Without a start time a reused
// pid is not told apart; the proxy's launchd setup is macOS only.
func processIdentity(pid int) (int64, bool) {
	err := syscall.Kill(pid, 0)
	return 0, err == nil || errors.Is(err, syscall.EPERM)
}
