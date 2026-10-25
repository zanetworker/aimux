// Package jsonl reads agent session files line by line.
package jsonl

import (
	"bufio"
	"io"
)

// MaxLineSize is the longest line NewScanner accepts. One session line holds a
// whole turn, and a large tool result can exceed 1 MB, so the limit is set far
// above real lines while still bounding memory on a corrupt file.
const MaxLineSize = 64 << 20

// NewScanner returns a line scanner whose buffer starts small and grows only
// for long lines, up to MaxLineSize.
func NewScanner(r io.Reader) *bufio.Scanner {
	return newScanner(r, MaxLineSize)
}

func newScanner(r io.Reader, max int) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, min(64*1024, max)), max)
	return s
}
