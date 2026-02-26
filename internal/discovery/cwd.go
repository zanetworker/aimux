package discovery

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GetProcessCwd returns the current working directory of a process using
// `lsof -p PID -Fn`. It looks for the "fcwd" line followed by the "n/path" line.
func GetProcessCwd(pid int) (string, error) {
	out, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof -p %d: %w", pid, err)
	}

	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if line == "fcwd" && i+1 < len(lines) {
			next := lines[i+1]
			if strings.HasPrefix(next, "n") {
				return next[1:], nil
			}
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}
