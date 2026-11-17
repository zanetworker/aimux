package toolgateway

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the user id of the process at the other end of a Unix socket.
func peerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, serr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) //nolint:gosec // G115: fd is an open file descriptor, a small non-negative int
		if e != nil {
			serr = e
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return -1, err
	}
	return uid, serr
}
