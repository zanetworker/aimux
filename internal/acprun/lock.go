package acprun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrInUseElsewhere: another aimux process (a second `aimux chat`, or the
// remote server) is already continuing the session.
var ErrInUseElsewhere = errors.New("the session is being continued by another aimux process")

// SessionLock takes an exclusive, non-blocking lock on sessionID in dir, so
// separate aimux processes never drive one session at the same time. The OS
// drops the lock if the process dies. Call release to let go.
func SessionLock(dir, sessionID string) (release func(), err error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session lock dir %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".lock"), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- id validated above
	if err != nil {
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- file descriptors are small
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (session %s)", ErrInUseElsewhere, sessionID)
		}
		return nil, fmt.Errorf("lock session %s: %w", sessionID, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- file descriptors are small
		_ = f.Close()
	}, nil
}
