//go:build !unix

package toolgateway

// lockPins has no cross-process lock off Unix; the gateway itself needs a
// Unix system.
func lockPins(string) (func(), error) { return func() {}, nil }
