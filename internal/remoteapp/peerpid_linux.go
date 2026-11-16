package remoteapp

import "golang.org/x/sys/unix"

// peerPID is the pid of the process at the other end of the unix socket fd
// (SO_PEERCRED); 0 when it cannot be read.
func peerPID(fd int) int {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0
	}
	return int(cred.Pid)
}
