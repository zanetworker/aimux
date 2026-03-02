package terminal

import "testing"

// Compile-time interface compliance checks.
var _ SessionBackend = (*Session)(nil)
var _ SessionBackend = (*TmuxSession)(nil)

func TestSessionBackendInterface(t *testing.T) {
	// This test exists to verify at compile time that both Session and
	// TmuxSession implement the SessionBackend interface. The var _
	// declarations above catch any missing methods.
}
