//go:build linux

package keyring

import "context"

// SecretService is the Secret Service item with attribute service=<service>,
// read with secret-tool (absent tool means no key).
func SecretService(service string) Source { return secretServiceSource{service: service} }

// System is this platform's secret store for service: the Secret Service on
// Linux.
func System(service string) Source { return SecretService(service) }

// Exists reports whether the Secret Service has an item for service.
// secret-tool has no metadata-only lookup, so the secret is read and
// discarded at once.
func Exists(service string) bool {
	key, err := SecretService(service).Lookup(context.Background())
	return err == nil && key != ""
}
