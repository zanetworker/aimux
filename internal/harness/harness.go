// Package harness holds the types shared by the core packages and the
// harness-specific code behind them, so that terminput never imports
// provider and provider never imports terminput. What one harness draws on
// its screen, how its process is named and how its input box is emptied
// live behind the Composer interface, implemented per harness (claude/),
// never in the core. This package must not import other internal packages.
package harness

import (
	"strings"
	"unicode/utf8"
)

// Key is a single key sent to a terminal, such as "1" for a prompt option.
type Key string

// Keys other than digits that a terminal driver can press.
const (
	KeyEscape    Key = "escape"
	KeyEnter     Key = "enter"
	KeySpace     Key = "space"
	KeyBackspace Key = "backspace"
	// KeyClear empties the input row the cursor is on, from any cursor
	// position (drivers send end of line, then kill to line start).
	KeyClear Key = "clear"
)

// Option is one numbered choice in a permission prompt.
type Option struct {
	Key   Key
	Label string
}

// Prompt is a permission prompt read from the screen.
type Prompt struct {
	// ID is stable for the same body, question and labels, so a retried key
	// can be matched against the prompt it was meant for.
	ID       string
	Question string
	// Detail is what the prompt is about, as the harness shows it above the
	// question (a tool and its argument, a file and a preview), bounded in
	// size by the harness; empty when it cannot be read whole.
	Detail  string
	Options []Option
}

// Screen is what a harness's screen shows, as one read sees it.
type Screen struct {
	// Prompt is the live permission prompt, or nil.
	Prompt *Prompt
	// Working: the harness shows it is working on a turn.
	Working bool
	// WorkingSeconds is how long the harness says the turn has run; 0 when
	// it shows no time or is not working.
	WorkingSeconds int
	// InputEmpty: the live input box shows nothing typed (placeholders
	// count as empty).
	InputEmpty bool
	// BoxHasText: with no prompt showing, the input box holds text, which
	// may be the owner's draft or a suggestion the harness drew.
	BoxHasText bool
	// BoxRows is the number of text rows in the input box; 0 when none.
	BoxRows int
	// PasteMarker is the box's only row when that row is the harness's
	// placeholder for a long paste; "" otherwise. It is never other text.
	PasteMarker string
}

// Match is how the input box compares with a given text.
type Match struct {
	// Words: the box holds the same words in order, only spacing and
	// wrapping changed.
	Words bool
	// Shown: the box visibly holds the text, compared without whitespace.
	Shown bool
	// Typed: Shown, or, for text that is pasted (PasteText), the box shows
	// the harness's paste placeholder: the text just typed waits there.
	Typed bool
}

// RoomKeys is how a harness's input box is made ready for a message when
// it holds text. The caller checks the screen and its own safety rules
// before every key.
type RoomKeys struct {
	// Probe, when set, is pressed first: a box that then reads empty held
	// only a suggestion, which the message replaces. Unprobe, when set, is
	// pressed when the box still holds text, to take Probe back out.
	Probe, Unprobe Key
}

// Composer is one harness's terminal knowledge: it reads the screen and
// says which process is the harness, which texts it would not take as a
// message, and which keys make room in its input box.
type Composer interface {
	// Read parses a screen.
	Read(screen string) Screen
	// BoxShows compares the input box on screen with text.
	BoxShows(screen, text string) Match
	// IsAgentProcess reports whether comm, a tty's foreground command name
	// or path, is the harness itself.
	IsAgentProcess(comm string) bool
	// RefuseText reports whether the harness would treat text as something
	// other than a message (a shell escape; a command, unless allowCommands).
	RefuseText(text string, allowCommands bool) bool
	// RoomKeys is the probe for a box with text.
	RoomKeys() RoomKeys
	// ClearKeys are the keys that empty an input box of rows rows from any
	// cursor position.
	ClearKeys(rows int) []Key
}

// pasteOver is the length in characters above which text is pasted.
const pasteOver = 200

// PasteText reports whether text goes in as a bracketed paste: it has a
// newline or a tab, or more than 200 characters.
func PasteText(text string) bool {
	return strings.ContainsAny(text, "\n\t") || utf8.RuneCountInString(text) > pasteOver
}
