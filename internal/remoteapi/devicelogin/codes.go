package devicelogin

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
)

// crockford is Crockford's base32 alphabet: no I, L, O or U, so a typed
// code survives being read aloud or misread.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const codeLen = 8

// errBadCode never repeats what was typed: it may be most of a valid code.
var errBadCode = errors.New("a pairing code is 8 characters from 0-9 and A-Z without U, written XXXX-XXXX or XXXXXXXX")

// NewCode returns a random pairing code for display (XXXX-XXXX, 40 bits) and
// the SHA-256 of its canonical form (the 8 characters, no dash), which is
// all that is ever stored.
func NewCode() (display string, hash [32]byte) {
	var b [5]byte          // 40 bits = 8 symbols of 5 bits, no modulo bias
	_, _ = rand.Read(b[:]) // never fails (crypto/rand, Go 1.24+)
	v := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	code := make([]byte, codeLen)
	for i := codeLen - 1; i >= 0; i-- {
		code[i] = crockford[v&31]
		v >>= 5
	}
	return string(code[:4]) + "-" + string(code[4:]), sha256.Sum256(code)
}

// ParseCode turns a typed code into the hash NewCode gave for it: the
// SHA-256 of CanonicalCode's form.
func ParseCode(s string) ([32]byte, error) {
	code, err := CanonicalCode(s)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256([]byte(code)), nil
}

// CanonicalCode returns a typed code in the form that is hashed and that the
// iPhone signs: the 8 characters, uppercase, no dash. It takes the code with
// or without its dash, in either case, with surrounding spaces, and reads I
// and L as 1 and O as 0.
func CanonicalCode(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) == codeLen+1 && s[4] == '-' {
		s = s[:4] + s[5:]
	}
	if len(s) != codeLen {
		return "", errBadCode
	}
	code := make([]byte, codeLen)
	for i := range codeLen {
		c := s[i]
		switch c {
		case 'I', 'L':
			c = '1'
		case 'O':
			c = '0'
		}
		if strings.IndexByte(crockford, c) < 0 {
			return "", errBadCode
		}
		code[i] = c
	}
	return string(code), nil
}
