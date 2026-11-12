package remoteapp

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// socketDialTimeout bounds asking whether a process answers on a socket.
const socketDialTimeout = time.Second

// listenSocket listens on the unix socket at path for one local client. Its
// directory is created 0700 (an existing one must not be open to group or
// others); the socket is made 0600. A socket file left by a process that is
// gone is removed; one a process answers on, or a path that is not a
// socket, is an error and is left alone. Closing the listener removes the
// socket.
func listenSocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: 0700 is the intended owner-only socket dir; umask may have loosened it
			return nil, fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("socket directory %s: %w", dir, err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("socket directory %s is %v; it must be private (0700) so no other user can reach the socket", dir, fi.Mode().Perm())
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; move it aside", path)
		}
		if answers(path) {
			return nil, fmt.Errorf("a process answers on %s already", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove the stale socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // the 0700 directory guards the moment before
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	return ln, nil
}

// answers reports whether a process accepts connections on the socket.
func answers(path string) bool {
	c, err := net.DialTimeout("unix", path, socketDialTimeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// socketOwner is the pid of the process answering on the socket at path;
// ok is false when none answers. pid is 0 when the platform cannot say.
func socketOwner(path string) (pid int, ok bool) {
	c, err := net.DialTimeout("unix", path, socketDialTimeout)
	if err != nil {
		return 0, false
	}
	defer func() { _ = c.Close() }()
	uc, isUnix := c.(*net.UnixConn)
	if !isUnix {
		return 0, true
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, true
	}
	_ = raw.Control(func(fd uintptr) { pid = peerPID(int(fd)) })
	return pid, true
}
