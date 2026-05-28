// Package clipboard provides cross-platform text clipboard operations.
package clipboard

import (
	"fmt"
	"os/exec"
	"regexp"
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

var validSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ResumeCommand returns the full CLI command to resume a session.
// If workingDir is provided, the command includes a cd prefix so the
// resume works regardless of the caller's current directory.
// Both arguments are shell-quoted to prevent injection.
func ResumeCommand(sessionID, workingDir string) string {
	if !validSessionID.MatchString(sessionID) {
		return ""
	}
	quoted := shellQuote(sessionID)
	if workingDir != "" {
		return "cd " + shellQuote(workingDir) + " && claude --resume " + quoted
	}
	return "claude --resume " + quoted
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
