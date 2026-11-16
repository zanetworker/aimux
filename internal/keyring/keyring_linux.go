//go:build linux

package keyring

// SecretService is the Secret Service item with attribute service=<service>,
// read with secret-tool (absent tool means no key).
func SecretService(service string) Source { return secretServiceSource{service: service} }

// System is this platform's secret store for service: the Secret Service on
// Linux.
func System(service string) Source { return SecretService(service) }

// Probe reports whether the Secret Service has an item for service.
// secret-tool has no metadata-only lookup, so the secret is read and
// discarded at once. A missing item or no secret-tool is (false, nil); any
// other failure (a locked store, D-Bus) is an error.
func Probe(service string) (bool, error) { return probeSecretService(service) }

// Exists is Probe with any failure read as absent.
func Exists(service string) bool {
	ok, _ := Probe(service)
	return ok
}
