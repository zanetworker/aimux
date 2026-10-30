//go:build !unix

package history

import "os/exec"

// ownProcessGroup is a no-op without process groups: cancelling kills only
// claude -p itself, and WaitDelay still bounds the wait for its output.
func ownProcessGroup(*exec.Cmd) {}
