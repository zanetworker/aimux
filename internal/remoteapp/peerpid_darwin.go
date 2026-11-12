package remoteapp

import "golang.org/x/sys/unix"

// peerPID is the pid of the process at the other end of the unix socket fd
// (LOCAL_PEERPID); 0 when it cannot be read.
func peerPID(fd int) int {
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil {
		return 0
	}
	return pid
}
