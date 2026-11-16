//go:build darwin

package keyring

import "context"

// Keychain is the macOS Keychain generic password stored under service.
func Keychain(service string) Source { return keychainSource{service: service} }

// System is this platform's secret store for service: the Keychain on macOS.
func System(service string) Source { return Keychain(service) }

// Exists reports whether the Keychain has a generic password for service,
// without reading the secret (no -w).
func Exists(service string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	_, err := run(ctx, "/usr/bin/security", "find-generic-password", "-s", service)
	return err == nil
}
