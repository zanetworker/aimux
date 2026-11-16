//go:build !darwin && !linux

package keyring

import (
	"context"
	"fmt"
)

// System is this platform's secret store for service; there is none here,
// so it never has a key.
func System(service string) Source { return notFoundSource{name: "secret store " + service} }

// Exists is always false where there is no supported secret store.
func Exists(string) bool { return false }

// notFoundSource has no key; it stands in for a secret store the platform
// does not have.
type notFoundSource struct{ name string }

func (s notFoundSource) String() string { return s.name }

func (s notFoundSource) Lookup(context.Context) (string, error) {
	return "", fmt.Errorf("%s: not available on this platform: %w", s.name, ErrNotFound)
}
