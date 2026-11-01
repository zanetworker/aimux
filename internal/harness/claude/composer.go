package claude

import (
	"path"
	"strings"
	"unicode"

	"github.com/zanetworker/aimux/internal/harness"
)

// Composer is Claude Code's harness.Composer over the parsers in screen.go.
type Composer struct{}

var _ harness.Composer = Composer{}

// Read parses the prompt once and the input box once and derives every
// field from them; each field equals its exported parser's answer.
func (Composer) Read(screen string) harness.Screen {
	prompt := ParsePrompt(screen)
	box, boxOK := bottomBox(screen)
	st := harness.Screen{
		Prompt:     prompt,
		Working:    Working(screen),
		InputEmpty: inputEmpty(box, boxOK),
	}
	if st.Working {
		st.WorkingSeconds, _ = WorkingSeconds(screen)
	}
	if prompt != nil {
		return st // boxRows: no input box reads while a prompt shows
	}
	rows, ok := composerRows(box, boxOK)
	st.BoxHasText = hasText(rows, ok)
	st.BoxRows = len(rows)
	if ok && len(rows) == 1 && pastedMarker.MatchString(rows[0]) {
		st.PasteMarker = rows[0]
	}
	return st
}

// BoxShows reads the input box once for all three comparisons.
func (Composer) BoxShows(screen, text string) harness.Match {
	rows, ok := boxRows(screen)
	return harness.Match{
		Words: wordsIn(rows, ok, text),
		Shown: textShownIn(rows, ok, text),
		Typed: textIn(rows, ok, text),
	}
}

// IsAgentProcess: Claude Code runs as claude or node.
func (Composer) IsAgentProcess(comm string) bool {
	switch path.Base(strings.TrimSpace(comm)) {
	case "claude", "node":
		return true
	}
	return false
}

// RefuseText refuses a leading "!" after optional white space, which
// switches Claude Code to bash mode (Enter would then run a shell command
// unasked), and a leading "/", which Claude Code runs as a slash command,
// unless allowCommands.
func (Composer) RefuseText(text string, allowCommands bool) bool {
	t := strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(t, "!") || (!allowCommands && strings.HasPrefix(t, "/"))
}

// RoomKeys: KeySpace tells Claude Code's grey suggestion from a draft.
// Typing dismisses a suggestion, so the box then reads empty; a draft keeps
// its text and gains the space, which KeyBackspace takes back out.
func (Composer) RoomKeys() harness.RoomKeys {
	return harness.RoomKeys{Probe: harness.KeySpace, Unprobe: harness.KeyBackspace}
}

// ClearKeys: two KeyClear per box row, at least one row.
func (Composer) ClearKeys(rows int) []harness.Key {
	keys := make([]harness.Key, 2*max(1, rows))
	for i := range keys {
		keys[i] = harness.KeyClear
	}
	return keys
}
