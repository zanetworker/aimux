package jump

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// focusScript selects the iTerm2 window, tab and pane whose tty equals the
// first argument. Windows are addressed by id: index and loop-variable
// references shift as windows reorder and end up selecting the wrong pane.
const focusScript = `on run argv
  set target to item 1 of argv
  tell application "iTerm2"
    set ids to id of every window
    repeat with n from 1 to count of ids
      set w to window id (item n of ids)
      repeat with j from 1 to count of tabs of w
        repeat with k from 1 to count of sessions of tab j of w
          if tty of session k of tab j of w is target then
            tell w to select
            tell tab j of w to select
            tell session k of tab j of w to select
            activate
            return "ok|" & n & "|" & j & "|" & k & "|" & (count of sessions of tab j of w)
          end if
        end repeat
      end repeat
    end repeat
  end tell
  return "missing"
end run`

// runOsascript is swapped in tests.
var runOsascript = func(script string, args ...string) (string, error) {
	out, err := exec.Command("osascript", append([]string{"-e", script}, args...)...).Output() // #nosec G204 -- fixed script, tty passed as argv
	return string(out), err
}

// ITerm2FocusByTTY brings the iTerm2 pane attached to tty (e.g. /dev/ttys031)
// to the front. It reports false, without error, when no pane has that tty.
func ITerm2FocusByTTY(tty string) (bool, error) {
	_, found, err := ITerm2FocusLocate(tty)
	return found, err
}

// ITerm2FocusLocate focuses the pane like ITerm2FocusByTTY and also says
// where it is ("tab 3 · pane 1 of 4"), so a jump to a split pane
// next to the current one is not mistaken for nothing happening.
func ITerm2FocusLocate(tty string) (string, bool, error) {
	if tty == "" {
		return "", false, errors.New("focus: empty tty")
	}
	out, err := runOsascript(focusScript, tty)
	if err != nil {
		return "", false, fmt.Errorf("focus iTerm2 pane %s: %w", tty, err)
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if parts[0] != "ok" {
		return "", false, nil
	}
	if len(parts) != 5 {
		return "", true, nil
	}
	return fmt.Sprintf("tab %s · pane %s of %s", parts[2], parts[3], parts[4]), true, nil // parts[1] is z-order, not meaningful to the user
}

// TTYForPID returns the controlling terminal of pid as /dev/ttysNNN, or ""
// when the process has none (background or headless).
func TTYForPID(pid int) string {
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output() // #nosec G204
	if err != nil {
		return ""
	}
	return parsePSTTY(string(out))
}

func parsePSTTY(out string) string {
	t := strings.TrimSpace(out)
	if t == "" || t == "??" {
		return ""
	}
	return "/dev/" + t
}
