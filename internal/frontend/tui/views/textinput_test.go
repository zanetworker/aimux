package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func specialKey(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func ctrlKey(s string) tea.KeyMsg {
	// For ctrl keys, Type is the specific ctrl type, Runes is nil.
	// But bubbletea uses KeyRunes with special string for ctrl combos.
	// Actually in bubbletea v1, ctrl+a is its own KeyType.
	switch s {
	case "ctrl+a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	case "ctrl+e":
		return tea.KeyMsg{Type: tea.KeyCtrlE}
	case "ctrl+b":
		return tea.KeyMsg{Type: tea.KeyCtrlB}
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}
	case "ctrl+w":
		return tea.KeyMsg{Type: tea.KeyCtrlW}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestTextInput_TypeAndValue(t *testing.T) {
	var ti TextInput
	ti.HandleKey(runeKey("h"))
	ti.HandleKey(runeKey("i"))
	if ti.Value() != "hi" {
		t.Errorf("Value() = %q, want %q", ti.Value(), "hi")
	}
}

func TestTextInput_Backspace(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello")
	ti.HandleKey(specialKey(tea.KeyBackspace))
	if ti.Value() != "hell" {
		t.Errorf("after backspace: %q, want %q", ti.Value(), "hell")
	}
}

func TestTextInput_BackspaceAtStart(t *testing.T) {
	var ti TextInput
	ti.SetValue("hi")
	ti.pos = 0
	ti.HandleKey(specialKey(tea.KeyBackspace))
	if ti.Value() != "hi" {
		t.Errorf("backspace at start should be no-op: %q", ti.Value())
	}
}

func TestTextInput_LeftRight(t *testing.T) {
	var ti TextInput
	ti.SetValue("abc")
	// Cursor at end (pos=3)
	ti.HandleKey(specialKey(tea.KeyLeft))
	// pos=2, insert "X" between 'b' and 'c'
	ti.HandleKey(runeKey("X"))
	if ti.Value() != "abXc" {
		t.Errorf("after left+type: %q, want %q", ti.Value(), "abXc")
	}
}

func TestTextInput_CtrlA_Home(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello")
	ti.HandleKey(ctrlKey("ctrl+a"))
	if ti.pos != 0 {
		t.Errorf("ctrl+a: pos = %d, want 0", ti.pos)
	}
	// Type at beginning
	ti.HandleKey(runeKey("X"))
	if ti.Value() != "Xhello" {
		t.Errorf("after ctrl+a + type: %q, want %q", ti.Value(), "Xhello")
	}
}

func TestTextInput_CtrlE_End(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello")
	ti.pos = 0
	ti.HandleKey(ctrlKey("ctrl+e"))
	if ti.pos != 5 {
		t.Errorf("ctrl+e: pos = %d, want 5", ti.pos)
	}
}

func TestTextInput_CtrlW_DeleteWord(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello world")
	ti.HandleKey(ctrlKey("ctrl+w"))
	if ti.Value() != "hello " {
		t.Errorf("ctrl+w: %q, want %q", ti.Value(), "hello ")
	}
	ti.HandleKey(ctrlKey("ctrl+w"))
	if ti.Value() != "" {
		t.Errorf("ctrl+w again: %q, want empty", ti.Value())
	}
}

func TestTextInput_CtrlU_DeleteToStart(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello world")
	ti.pos = 5 // cursor after "hello"
	ti.HandleKey(ctrlKey("ctrl+u"))
	if ti.Value() != " world" {
		t.Errorf("ctrl+u: %q, want %q", ti.Value(), " world")
	}
	if ti.pos != 0 {
		t.Errorf("ctrl+u: pos = %d, want 0", ti.pos)
	}
}

func TestTextInput_CtrlK_DeleteToEnd(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello world")
	ti.pos = 5
	ti.HandleKey(ctrlKey("ctrl+k"))
	if ti.Value() != "hello" {
		t.Errorf("ctrl+k: %q, want %q", ti.Value(), "hello")
	}
}

func TestTextInput_Delete(t *testing.T) {
	var ti TextInput
	ti.SetValue("abc")
	ti.pos = 1
	ti.HandleKey(specialKey(tea.KeyDelete))
	if ti.Value() != "ac" {
		t.Errorf("delete: %q, want %q", ti.Value(), "ac")
	}
}

func TestTextInput_Paste(t *testing.T) {
	var ti TextInput
	ti.SetValue("hd")
	ti.pos = 1 // between 'h' and 'd'
	// Simulate bracketed paste
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ello worl"), Paste: true}
	ti.HandleKey(paste)
	if ti.Value() != "hello world" {
		t.Errorf("paste: %q, want %q", ti.Value(), "hello world")
	}
	if ti.pos != 10 {
		t.Errorf("paste: pos = %d, want 10", ti.pos)
	}
}

func TestTextInput_Space(t *testing.T) {
	var ti TextInput
	ti.HandleKey(runeKey("a"))
	ti.HandleKey(tea.KeyMsg{Type: tea.KeySpace})
	ti.HandleKey(runeKey("b"))
	if ti.Value() != "a b" {
		t.Errorf("space: %q, want %q", ti.Value(), "a b")
	}
}

func TestTextInput_Reset(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello")
	ti.Reset()
	if ti.Value() != "" {
		t.Errorf("after reset: %q, want empty", ti.Value())
	}
	if ti.pos != 0 {
		t.Errorf("after reset: pos = %d, want 0", ti.pos)
	}
}

func TestTextInput_BeforeAfterCursor(t *testing.T) {
	var ti TextInput
	ti.SetValue("hello")
	ti.pos = 3
	if ti.BeforeCursor() != "hel" {
		t.Errorf("BeforeCursor = %q, want %q", ti.BeforeCursor(), "hel")
	}
	if ti.AfterCursor() != "lo" {
		t.Errorf("AfterCursor = %q, want %q", ti.AfterCursor(), "lo")
	}
}

func TestTextInput_InsertMiddle(t *testing.T) {
	var ti TextInput
	ti.SetValue("hllo")
	ti.pos = 1
	ti.HandleKey(runeKey("e"))
	if ti.Value() != "hello" {
		t.Errorf("insert middle: %q, want %q", ti.Value(), "hello")
	}
	if ti.pos != 2 {
		t.Errorf("insert middle: pos = %d, want 2", ti.pos)
	}
}

func TestTextInput_SpecialKeyNotConsumed(t *testing.T) {
	var ti TextInput
	consumed := ti.HandleKey(specialKey(tea.KeyUp))
	if consumed {
		t.Error("arrow up should not be consumed")
	}
}
