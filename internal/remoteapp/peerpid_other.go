//go:build !darwin && !linux

package remoteapp

// peerPID cannot read a socket peer's pid on this platform.
func peerPID(int) int { return 0 }
