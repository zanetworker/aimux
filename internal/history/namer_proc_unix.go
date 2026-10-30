//go:build unix

package history

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup runs cmd in a new process group and makes cancelling it
// kill the whole group, so children of claude -p die with it.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
