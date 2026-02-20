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

// ITerm2FocusByPID uses AppleScript to focus the iTerm2 tab containing a process.
func ITerm2FocusByPID(pid int) error {
	script := fmt.Sprintf(`
tell application "iTerm2"
    activate
    repeat with w in windows
        repeat with t in tabs of w
            repeat with s in sessions of t
                try
                    set sessionPID to (do shell script "ps -o ppid= -p %d 2>/dev/null | tr -d ' '")
                    if sessionPID is not "" then
                        select t
                        select s
                        return
                    end if
                end try
            end repeat
        end repeat
    end repeat
end tell`, pid)

	return exec.Command("osascript", "-e", script).Run()
}
