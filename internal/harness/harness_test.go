package harness

import (
	"strings"
	"testing"
)

func TestPasteText(t *testing.T) {
	for text, want := range map[string]bool{
		"short":                  false,
		"two\nlines":             true,
		"a\tb":                   true,
		strings.Repeat("a", 200): false,
		strings.Repeat("a", 201): true,
		strings.Repeat("س", 200): false,
	} {
		if got := PasteText(text); got != want {
			t.Errorf("PasteText(%d chars) = %v, want %v", len([]rune(text)), got, want)
		}
	}
}
