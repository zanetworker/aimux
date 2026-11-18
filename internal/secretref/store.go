package secretref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
)

// Seams for tests: the real Keychain item aimux owns.
var (
	readItem  = func(ctx context.Context, item string) (string, error) { return keyring.Stored(item).Lookup(ctx) }
	writeItem = keyring.Store
)

var (
	serverNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	varNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ItemFor is the Keychain item that holds server's secrets.
func ItemFor(server string) string { return "aimux/" + server }

// Store sets env.<name> of server's Keychain item (aimux/<server>) to value,
// keeping every other field; a missing item is created. The value travels
// to the Keychain on stdin (keyring.Store), never on a command line.
func Store(ctx context.Context, server, name, value string) error {
	if !serverNameRE.MatchString(server) {
		return fmt.Errorf("server name %q may hold only letters, digits, . _ and -", server)
	}
	if !varNameRE.MatchString(name) {
		return fmt.Errorf("variable name %q is not NAME (letters, digits, _)", name)
	}
	if value == "" {
		return errors.New("empty value")
	}
	item := ItemFor(server)
	unlock, err := lockItem(server)
	if err != nil {
		return err
	}
	defer unlock()
	var doc map[string]any
	raw, err := readItem(ctx, item)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
	case err != nil:
		return fmt.Errorf("keychain item %s: cannot read it, so nothing was written", item)
	default:
		if err := json.Unmarshal([]byte(raw), &doc); err != nil || doc == nil {
			return fmt.Errorf("keychain item %s is not a JSON object; nothing was written", item)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	env := map[string]any{}
	if cur, ok := doc["env"]; ok {
		m, isMap := cur.(map[string]any)
		if !isMap {
			return fmt.Errorf("keychain item %s has an env field that is not an object; nothing was written", item)
		}
		env = m
	}
	env[name] = value
	doc["env"] = env
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := writeItem(item, string(b)); err != nil {
		return fmt.Errorf("keychain item %s: %w", item, err)
	}
	return nil
}

// lockDir holds the per-item lock files ("" is ~/.aimux/locks).
var lockDir = ""

// lockItem serializes Store's read-modify-write of server's item across
// processes, waiting at most 5 s.
func lockItem(server string) (func(), error) {
	dir := lockDir
	if dir == "" {
		home, err := userHome()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".aimux", "locks")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lock for %s: %w", server, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "secret-"+server+".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: a lock file under aimux's own folder, name checked by serverNameRE
	if err != nil {
		return nil, fmt.Errorf("lock for %s: %w", server, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: an open file descriptor is a small non-negative int
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil //nolint:gosec // G115: as above
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("another aimux is writing %s's secrets; try again", server)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
