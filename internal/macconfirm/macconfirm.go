// Package macconfirm asks the person at the Mac to confirm an action in a
// native dialog (osascript display dialog), as a step-up that runs on the
// Mac itself: a remote client can request it but never answer it.
package macconfirm

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxText caps the dialog's text, in bytes, before quoting.
const MaxText = 1500

// GiveUpAfter is how long the dialog waits for an answer, in seconds.
const GiveUpAfter = 60

// Answer is how the dialog ended.
type Answer int

const (
	// Unavailable: no dialog could be shown (no GUI session, no osascript).
	Unavailable Answer = iota
	// Approved: the person clicked Approve.
	Approved
	// Cancelled: the person clicked Cancel, or the request ended.
	Cancelled
	// TimedOut: no answer within GiveUpAfter seconds.
	TimedOut
)

// Dialog shows the confirm dialog through Run (exec.CommandContext's
// output when nil).
type Dialog struct {
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Command is the osascript to run ("" is osascript on PATH).
	Command string
}

// System is the dialog through the real osascript.
var System = Dialog{}

func (d Dialog) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if d.Run != nil {
		return d.Run(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // G204: osascript with one -e script; the text is a quoted AppleScript string, no shell
	// osascript says why it failed (a Cancel is "(-128)") on stderr only.
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// Fits reports whether text can be shown whole: longer text would be cut,
// and a person must see everything they approve.
func Fits(text string) bool { return len(text) <= MaxText }

// Confirm shows text titled title with Cancel (the default) and Approve,
// giving up after GiveUpAfter seconds. Only a click on Approve is
// Approved; the error, when not nil, says why no answer was had.
func (d Dialog) Confirm(ctx context.Context, title, text string) (Answer, error) {
	script := "display dialog " + Quote(text) + " with title " + Quote(title) +
		` buttons {"Cancel", "Approve"} default button "Cancel" cancel button "Cancel" with icon caution giving up after ` +
		strconv.Itoa(GiveUpAfter)
	cmd := d.Command
	if cmd == "" {
		cmd = "osascript"
	}
	out, err := d.run(ctx, cmd, "-e", script)
	switch {
	case ctx.Err() != nil:
		return Cancelled, ctx.Err()
	case err != nil && strings.Contains(err.Error(), "(-128)"): // User canceled
		return Cancelled, nil
	case err != nil:
		return Unavailable, err
	}
	res := string(out)
	switch {
	case strings.Contains(res, "gave up:true"):
		return TimedOut, nil
	case strings.Contains(res, "button returned:Approve"):
		return Approved, nil
	}
	return Cancelled, nil
}

// Quote is s as an AppleScript string literal: backslashes and double
// quotes escaped, control characters other than newline and tab dropped,
// cut to MaxText bytes (on a rune boundary, marked with an ellipsis).
func Quote(s string) string {
	cut := false
	if len(s) > MaxText {
		s = s[:MaxText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		cut = true
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			// dropped: a control character has no place in the dialog
		default:
			b.WriteRune(r)
		}
	}
	if cut {
		b.WriteString("…")
	}
	b.WriteByte('"')
	return b.String()
}
