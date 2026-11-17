//go:build darwin

package keyring

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

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

// storeAccount is the account of every item Store writes.
const storeAccount = "aimux"

// maxSecurityLine is the longest command line `security -i` reads whole:
// longer lines are split and the command fails (seen at 5000 bytes, fine
// at 4000), so Store refuses a value whose hex would not fit.
const maxSecurityLine = 4000

// Stored is the item Store wrote for service: the generic password of
// account "aimux" only, so an item another account owns under the same
// service (an older setup) is never read in its place.
func Stored(service string) Source { return keychainSource{service: service, account: storeAccount} }

// Store sets the generic password for service (account "aimux"), replacing
// an existing one. The value goes to `security -i` on stdin as hex, never on
// a command line, and is read back and compared, because `security -i` can
// report a failed command and still exit 0. Errors name the service and an
// exit status, never the value or the tool's output.
func Store(service, value string) error {
	if !validService(service) {
		return fmt.Errorf("keychain item name %q is not allowed (use letters, digits, '.', '_', '-', '/')", service)
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	line := "add-generic-password -U -a " + storeAccount + " -s " + service + " -X " + hex.EncodeToString([]byte(value)) + "\n"
	if len(line) > maxSecurityLine {
		return fmt.Errorf("keychain item %s: value too long (%d bytes; at most %d)", service, len(value), (maxSecurityLine-len(line)+2*len(value))/2)
	}
	if _, err := runStdin(ctx, "/usr/bin/security", []byte(line), "-i"); err != nil {
		return toolFailure(ctx, "keychain item "+service, err)
	}
	got, err := Stored(service).Lookup(context.Background())
	if err != nil || got != strings.TrimSpace(value) {
		return fmt.Errorf("keychain item %s did not take the new value", service)
	}
	return nil
}

// Delete removes the generic password Store wrote for service; a missing
// item is not an error.
func Delete(service string) error {
	if !validService(service) {
		return fmt.Errorf("keychain item name %q is not allowed", service)
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	_, err := run(ctx, "/usr/bin/security", "delete-generic-password", "-a", storeAccount, "-s", service)
	var ee *exec.ExitError
	if err == nil || (errors.As(err, &ee) && ee.ExitCode() == exitKeychainNotFound) {
		return nil
	}
	return toolFailure(ctx, "keychain item "+service, err)
}
