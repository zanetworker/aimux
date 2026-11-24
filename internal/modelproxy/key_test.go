package modelproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/keyring"
)

func writeKeyFile(path string) func(string) error {
	return func(key string) error { return os.WriteFile(path, []byte(key+"\n"), 0o600) }
}

func TestEnsureKeyReturnsTheStoredKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("existing-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored := false
	key, err := EnsureKey(context.Background(), keyring.File(path), func(string) error { stored = true; return nil })
	if err != nil || key != "existing-key" || stored {
		t.Fatalf("key %q, err %v, stored %v; want the existing key and no store", key, err, stored)
	}
}

func TestEnsureKeyCreatesOnceAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	first, err := EnsureKey(context.Background(), keyring.File(path), writeKeyFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("new key %q is not 32 random bytes in hex", first)
	}
	second, err := EnsureKey(context.Background(), keyring.File(path), func(string) error {
		t.Fatal("store called although a key exists")
		return nil
	})
	if err != nil || second != first {
		t.Fatalf("second call: key %q, err %v; want %q", second, err, first)
	}
}

func TestEnsureKeyStoreFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	_, err := EnsureKey(context.Background(), keyring.File(path), func(string) error { return errors.New("keychain locked") })
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("got %v, want the store error", err)
	}
}

func TestEnsureKeyUnreadableSourceIsNotReplaced(t *testing.T) {
	dir := t.TempDir() // a directory cannot be read as a key file
	_, err := EnsureKey(context.Background(), keyring.File(dir), func(string) error {
		t.Fatal("store called for a source that could not be read")
		return nil
	})
	if err == nil {
		t.Fatal("want an error for an unreadable key source")
	}
}
