package views

import tea "github.com/charmbracelet/bubbletea"

// TextInput is a minimal cursor-aware text input for inline editing.
// It supports readline-style navigation: Ctrl+A/E (home/end),
// left/right arrows, Ctrl+W (delete word), and clipboard paste.
type TextInput struct {
	text []rune
	pos  int
}

// Value returns the current text.
func (t *TextInput) Value() string {
	return string(t.text)
}

// SetValue replaces the text and moves the cursor to the end.
func (t *TextInput) SetValue(s string) {
	t.text = []rune(s)
	t.pos = len(t.text)
}

// Reset clears the text and cursor.
func (t *TextInput) Reset() {
	t.text = nil
	t.pos = 0
}

// BeforeCursor returns text to the left of the cursor.
func (t *TextInput) BeforeCursor() string {
	return string(t.text[:t.pos])
}

// AfterCursor returns text to the right of the cursor.
func (t *TextInput) AfterCursor() string {
	return string(t.text[t.pos:])
}

// HandleKey processes a key event and returns true if it was consumed.
// The caller handles enter/esc; this only handles editing keys.
func (t *TextInput) HandleKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "left", "ctrl+b":
		if t.pos > 0 {
			t.pos--
		}
	case "right", "ctrl+f":
		if t.pos < len(t.text) {
			t.pos++
		}
	case "ctrl+a", "home":
		t.pos = 0
	case "ctrl+e", "end":
		t.pos = len(t.text)
	case "ctrl+w":
		// Delete word before cursor
		if t.pos > 0 {
			i := t.pos - 1
			for i > 0 && t.text[i-1] == ' ' {
				i--
			}
			for i > 0 && t.text[i-1] != ' ' {
				i--
			}
			t.text = append(t.text[:i], t.text[t.pos:]...)
			t.pos = i
		}
	case "ctrl+u":
		// Delete to beginning of line
		t.text = t.text[t.pos:]
		t.pos = 0
	case "ctrl+k":
		// Delete to end of line
		t.text = t.text[:t.pos]
	case "backspace":
		if t.pos > 0 {
			t.text = append(t.text[:t.pos-1], t.text[t.pos:]...)
			t.pos--
		}
	case "delete":
		if t.pos < len(t.text) {
			t.text = append(t.text[:t.pos], t.text[t.pos+1:]...)
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			runes := msg.Runes
			if msg.Type == tea.KeySpace {
				runes = []rune{' '}
			}
			after := make([]rune, len(t.text[t.pos:]))
			copy(after, t.text[t.pos:])
			t.text = append(t.text[:t.pos], append(runes, after...)...)
			t.pos += len(runes)
		} else {
			return false
		}
	}
	return true
}
