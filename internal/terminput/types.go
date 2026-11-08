// Package terminput types text and keys into Claude Code sessions that are
// open in a terminal, after checking what the screen shows.
package terminput

import "github.com/zanetworker/aimux/internal/harness"

// Key is a single key sent to a terminal, such as "1" for a prompt option.
type Key = harness.Key

// Option is one numbered choice in a permission prompt.
type Option = harness.Option

// Prompt is a permission prompt read from the screen.
type Prompt = harness.Prompt

// Composer is the harness knowledge the Service relies on: what its screen
// shows, which foreground process is the harness, which texts it refuses
// and which keys make room in its input box.
type Composer = harness.Composer

// PasteText reports whether text goes in as a bracketed paste
// (harness.PasteText); drivers paste such text.
func PasteText(text string) bool { return harness.PasteText(text) }
