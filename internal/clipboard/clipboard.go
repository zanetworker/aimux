// Package clipboard provides cross-platform text clipboard operations.
package clipboard

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Copy places text on the system clipboard.
// On macOS it uses pbcopy; on Linux it tries xclip then xsel.
func Copy(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return fmt.Errorf("no clipboard tool found (install xclip or xsel)")
		}
	default:
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}

	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// ResumeCommand returns the full CLI command to resume a session.
// If workingDir is provided, the command includes a cd prefix so the
// resume works regardless of the caller's current directory.
func ResumeCommand(sessionID, workingDir string) string {
	if workingDir != "" {
		return "cd " + workingDir + " && claude --resume " + sessionID
	}
	return "claude --resume " + sessionID
}
