//go:build darwin

package keyring

// Keychain is the macOS Keychain generic password stored under service.
func Keychain(service string) Source { return keychainSource{service: service} }

// System is this platform's secret store for service: the Keychain on macOS.
func System(service string) Source { return Keychain(service) }

// Probe reports whether the Keychain has a generic password for service,
// without reading the secret (no -w). A missing item is (false, nil); a
// lookup that fails otherwise (timeout, access error) is an error.
func Probe(service string) (bool, error) { return probeKeychain(service) }

// Exists is Probe with any failure read as absent.
func Exists(service string) bool {
	ok, _ := Probe(service)
	return ok
}
