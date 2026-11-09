package waiting

import (
	"time"

	"github.com/zanetworker/aimux/internal/claudehooks"
)

// commandSuffix ends every hook command aimux installs for permission
// alerts, optionally followed by " || true"; it is how these entries are told
// apart from the owner's and from other aimux hooks.
const commandSuffix = " remote hooks notify"

// hookTimeout (seconds) bounds one hook call; the helper takes milliseconds.
const hookTimeout = 10

// spec is the remote hooks: the marker set on SetEvent, cleared on each of
// ClearEvents, all running the same command.
func spec() claudehooks.Spec {
	out := claudehooks.Spec{{Event: SetEvent, Matcher: SetMatcher, Suffix: commandSuffix, Timeout: hookTimeout}}
	for _, ev := range ClearEvents {
		out = append(out, claudehooks.Hook{Event: ev, Suffix: commandSuffix, Timeout: hookTimeout})
	}
	return out
}

// HookCommand is the hook command for the aimux binary at binary, quoted
// for the shell when it needs to be.
func HookCommand(binary string) string { return claudehooks.Command(binary, commandSuffix) }

// CommandBinary is the binary an aimux hook command runs, unquoted; false
// for any other command.
func CommandBinary(command string) (string, bool) {
	return claudehooks.CommandBinary(command, commandSuffix)
}

// Events are the hook events aimux installs, in the order it adds them.
func Events() []string { return append([]string{SetEvent}, ClearEvents...) }

// Change is what Install or Uninstall did, by event.
type Change = claudehooks.Change

// State is what Status found.
type State = claudehooks.State

// Install adds aimux's hooks (command) to the Claude Code settings at path,
// creating the file if missing and keeping every other key and hook. An
// aimux hook with another command (the binary moved) is updated in place.
// Nothing is written when nothing changes.
func Install(path, command string, now time.Time) (Change, error) {
	return claudehooks.InstallWith(path, spec(), func(claudehooks.Hook) string { return command }, now)
}

// Uninstall removes aimux's hooks from the settings at path, and an event
// list or the hooks object it leaves empty; nothing else.
func Uninstall(path string, now time.Time) (Change, error) {
	return claudehooks.Uninstall(path, spec(), now)
}

// Status reports which of aimux's hooks the settings at path hold.
func Status(path string) (State, error) { return claudehooks.Status(path, spec()) }
