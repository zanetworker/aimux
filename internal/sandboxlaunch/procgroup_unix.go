//go:build unix

package sandboxlaunch

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd as the leader of its own process group, so a
// kill reaches the helpers it forks (they hold its stdout and stderr).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to cmd's whole process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
