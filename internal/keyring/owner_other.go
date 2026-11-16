//go:build !unix

package keyring

import "os"

// ownedByCurrentUser cannot check ownership here; the 0600 mode check still applies.
func ownedByCurrentUser(os.FileInfo) bool { return true }
