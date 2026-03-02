package jump

import (
	"fmt"
	"os"
	"os/exec"
)

// IsITerm2 returns true if the current terminal is iTerm2.
func IsITerm2() bool {
	return os.Getenv("TERM_PROGRAM") == "iTerm.app"
}

// ITerm2SplitPane creates a horizontal split in the current iTerm2 session
// and runs the given command in the new pane.
func ITerm2SplitPane(command string) error {
	script := fmt.Sprintf(`
tell application "iTerm2"
	tell current session of current tab of current window
		split horizontally with default profile command %q
	end tell
end tell`, command)

	return exec.Command("osascript", "-e", script).Run()
}
