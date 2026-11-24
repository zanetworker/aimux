package modelproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/zanetworker/aimux/internal/keyring"
)

// KeyService is the secret-store entry holding the proxy key. Clients such as the claude-codex shell mode read it
// from there and send it as ANTHROPIC_API_KEY.
const KeyService = "aimux-model-proxy"

// EnsureKey returns the proxy key from src. Only when src has no key (keyring.ErrNotFound) does it create a random
// 32-byte key and save it with store; any other lookup error is returned, so a key that clients already use is
// never silently replaced.
func EnsureKey(ctx context.Context, src keyring.Source, store func(string) error) (string, error) {
	key, err := src.Lookup(ctx)
	if err == nil && key != "" {
		return key, nil
	}
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("read model proxy key: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate model proxy key: %w", err)
	}
	key = hex.EncodeToString(raw)
	if err := store(key); err != nil {
		return "", fmt.Errorf("save model proxy key in %s: %w", src, err)
	}
	return key, nil
}
