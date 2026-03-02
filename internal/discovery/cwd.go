package discovery

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GetProcessCwd returns the current working directory of a process.
// Uses `lsof -a -d cwd -p PID -Fn` which only queries the cwd file descriptor
// instead of listing all open files. Times out after 3 seconds to avoid
// blocking the discovery loop when lsof hangs on a process.
func GetProcessCwd(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof cwd for pid %d: %w", pid, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			return line[1:], nil
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}
