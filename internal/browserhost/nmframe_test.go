package browserhost

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	for _, m := range [][]byte{[]byte(`{"a":1}`), {}, []byte(strings.Repeat("x", 1<<20))} {
		if err := WriteMessage(&buf, m); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	for _, want := range []int{7, 0, 1 << 20} {
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(got) != want {
			t.Fatalf("len = %d, want %d", len(got), want)
		}
	}
	if _, err := ReadMessage(&buf); err != io.EOF {
		t.Fatalf("after last: %v, want io.EOF", err)
	}
}

func TestReadMessageEOF(t *testing.T) {
	if _, err := ReadMessage(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	// Partial header is not a clean close.
	if _, err := ReadMessage(bytes.NewReader([]byte{1, 2})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial header err = %v, want ErrUnexpectedEOF", err)
	}
}

func TestReadMessageShortBody(t *testing.T) {
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], 100)
	in := append(hdr[:], []byte("short")...)
	_, err := ReadMessage(bytes.NewReader(in))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want ErrUnexpectedEOF", err)
	}
}

func TestReadMessageTooLarge(t *testing.T) {
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], MaxInbound+1)
	_, err := ReadMessage(bytes.NewReader(hdr[:]))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestWriteMessageTooLarge(t *testing.T) {
	var buf bytes.Buffer
	err := WriteMessage(&buf, make([]byte, MaxOutbound+1))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes despite error", buf.Len())
	}
	if err := WriteMessage(&buf, make([]byte, MaxOutbound)); err != nil {
		t.Fatalf("exactly max should pass: %v", err)
	}
}

func TestChunkTextRuneSafe(t *testing.T) {
	s := strings.Repeat("aé世😀", 50)
	for _, max := range []int{1, 3, 4, 5, 7, 64, 1000} {
		chunks := ChunkText(s, max)
		if strings.Join(chunks, "") != s {
			t.Fatalf("max=%d: chunks do not rejoin", max)
		}
		for _, c := range chunks {
			if !utf8.ValidString(c) {
				t.Fatalf("max=%d: broken rune in %q", max, c)
			}
			if len(c) > max && utf8.RuneCountInString(c) > 1 {
				t.Fatalf("max=%d: chunk len %d too big", max, len(c))
			}
		}
	}
	if got := ChunkText("", 10); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}
	if got := ChunkText("abc", 10); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("short = %v", got)
	}
}
