package terminal

import "testing"

// Compile-time interface compliance checks.
var _ SessionBackend = (*Session)(nil)
var _ SessionBackend = (*TmuxSession)(nil)
var _ SessionBackend = (*KubectlExecBackend)(nil)

func TestSessionBackendInterface(t *testing.T) {
	// This test exists to verify at compile time that Session, TmuxSession,
	// and KubectlExecBackend implement the SessionBackend interface. The var _
	// declarations above catch any missing methods.
}
