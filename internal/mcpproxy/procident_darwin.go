package mcpproxy

import "golang.org/x/sys/unix"

// processIdentity is pid's start time in microseconds, from the kernel; false
// when pid runs no process. A reused pid has another start time.
func processIdentity(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	t := kp.Proc.P_starttime
	return t.Sec*1_000_000 + int64(t.Usec), true
}
