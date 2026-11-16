// Package browserhost implements the Chrome native messaging host side of
// the Wingman extension.
package browserhost

import (
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf8"
)

const (
	// MaxInbound is the largest message Chrome may send to the host (64 MiB).
	MaxInbound = 64 << 20
	// MaxOutbound is the largest message Chrome accepts from the host (1 MiB).
	MaxOutbound = 1 << 20
)

// ErrTooLarge reports a message over the native messaging size cap.
var ErrTooLarge = errors.New("native message too large")

// ReadMessage reads one length-prefixed message. It returns io.EOF on a clean
// close before any header byte, io.ErrUnexpectedEOF on a truncated header or
// body, and ErrTooLarge when the declared length exceeds MaxInbound. The body
// buffer grows with the data actually received, so a bogus header cannot force
// a large allocation.
func ReadMessage(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err // io.EOF if no bytes, ErrUnexpectedEOF if partial
	}
	n := binary.NativeEndian.Uint32(hdr[:])
	if n > MaxInbound {
		return nil, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(r, int64(n)))
	if err != nil {
		return nil, err
	}
	if uint32(len(body)) < n { //nolint:gosec // G115: len(body) <= n, and n <= MaxInbound
		return nil, io.ErrUnexpectedEOF
	}
	return body, nil
}

// WriteMessage writes msg with a native-endian length prefix. It rejects
// messages over MaxOutbound without writing anything.
func WriteMessage(w io.Writer, msg []byte) error {
	if len(msg) > MaxOutbound {
		return ErrTooLarge
	}
	buf := make([]byte, 4+len(msg))
	binary.NativeEndian.PutUint32(buf, uint32(len(msg))) //nolint:gosec // G115: len(msg) <= MaxOutbound, checked above
	copy(buf[4:], msg)
	_, err := w.Write(buf)
	return err
}

// ChunkText splits s into pieces of at most max bytes without breaking a UTF-8
// rune. A rune wider than max is emitted alone. Joining the chunks yields s.
func ChunkText(s string, max int) []string {
	if max < 1 {
		max = 1
	}
	var out []string
	for len(s) > 0 {
		if len(s) <= max {
			out = append(out, s)
			break
		}
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 { // first rune wider than max
			_, cut = utf8.DecodeRuneInString(s)
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}
