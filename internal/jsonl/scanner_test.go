package jsonl

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func TestNewScanner_ReadsLinesLongerThanOldLimits(t *testing.T) {
	long := `{"x":"` + strings.Repeat("a", 2<<20) + `"}` // 2 MB, above the old 1 MB cap
	input := "{\"n\":1}\n" + long + "\n{\"n\":3}\n"
	s := NewScanner(strings.NewReader(input))
	var lens []int
	for s.Scan() {
		lens = append(lens, len(s.Bytes()))
	}
	if err := s.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if len(lens) != 3 || lens[1] != len(long) {
		t.Fatalf("got line lengths %v, want 3 lines with the middle one %d bytes", lens, len(long))
	}
}

func TestScanner_LineAtMaxIsReadAndOverMaxFails(t *testing.T) {
	const max = 1024
	atMax := strings.Repeat("a", max-1) // bufio needs room for the newline
	s := newScanner(strings.NewReader(atMax+"\n"), max)
	if !s.Scan() || len(s.Bytes()) != max-1 {
		t.Fatalf("line at the limit was not read: err=%v", s.Err())
	}

	s = newScanner(strings.NewReader(strings.Repeat("a", max+10)+"\n"), max)
	for s.Scan() {
	}
	if !errors.Is(s.Err(), bufio.ErrTooLong) {
		t.Fatalf("Err() = %v, want bufio.ErrTooLong", s.Err())
	}
}

func TestNewScanner_EmptyInput(t *testing.T) {
	s := NewScanner(strings.NewReader(""))
	if s.Scan() {
		t.Fatal("Scan() on empty input returned true")
	}
	if s.Err() != nil {
		t.Fatalf("Err() = %v, want nil", s.Err())
	}
}
