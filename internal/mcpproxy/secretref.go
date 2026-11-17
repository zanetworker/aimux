package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/keyring"
)

// SecretRefs say where a saved definition's secret values are: per header
// of an HTTP server, per env variable of a stdio server. Env refs are kept
// but not used yet (Plan 4c wires them).
type SecretRefs struct {
	Headers map[string]SecretRef `json:"headers,omitempty"`
	Env     map[string]SecretRef `json:"env,omitempty"`
}

// SecretRef names one secret in one backend; the Keychain is the only one.
type SecretRef struct {
	Keychain *KeychainRef `json:"keychain,omitempty"`
}

// KeychainRef is a field of a JSON Keychain item: Item is the generic
// password's service, Field a dotted path into its JSON value
// ("headers.Authorization.value"; a key may itself hold dots).
type KeychainRef struct {
	Item  string `json:"item"`
	Field string `json:"field"`
}

// keychainHeaderRefs are the refs install writes for server's headers:
// each to its value in the item install stores (HeaderService(server)).
func keychainHeaderRefs(server string, headers []string) *SecretRefs {
	if len(headers) == 0 {
		return nil
	}
	refs := &SecretRefs{Headers: map[string]SecretRef{}}
	for _, h := range headers {
		refs.Headers[h] = SecretRef{Keychain: &KeychainRef{Item: HeaderService(server), Field: "headers." + h + ".value"}}
	}
	return refs
}

// errSecretMissing marks an item that is missing or unreadable.
var errSecretMissing = errors.New("missing or locked")

// resolveSecret reads the value ref points at. It is the one place a ref
// is resolved (Plan 4c moves it to internal/secrets). Errors name the item
// and field, never a value.
func resolveSecret(ctx context.Context, ref SecretRef, reader SecretReader) (string, error) {
	k := ref.Keychain
	if k == nil {
		return "", errors.New("empty or unknown secret reference (only keychain is supported)")
	}
	if k.Item == "" || k.Field == "" {
		return "", errors.New("keychain reference without item or field")
	}
	if reader == nil {
		return "", fmt.Errorf("keychain item %s is %w (no secret store here)", k.Item, errSecretMissing)
	}
	v, err := reader(ctx, k.Item)
	if err != nil {
		cause := "could not read it"
		if errors.Is(err, keyring.ErrNotFound) {
			cause = "not found"
		}
		return "", fmt.Errorf("keychain item %s is %w (%s)", k.Item, errSecretMissing, cause)
	}
	var doc any
	if json.Unmarshal([]byte(v), &doc) != nil {
		return "", fmt.Errorf("keychain item %s is not JSON", k.Item)
	}
	got, ok := jsonField(doc, k.Field)
	s, isString := got.(string)
	if !ok || !isString {
		return "", fmt.Errorf("keychain item %s has no string at %s", k.Item, k.Field)
	}
	return s, nil
}

// jsonField walks a dotted path to a string; keys may hold dots. At each
// object every key that matches the path's start is tried, longest first,
// and a branch that does not end in a string is left for the next, so
// headers "X" and "X.value" both resolve.
func jsonField(doc any, path string) (any, bool) {
	if path == "" {
		_, ok := doc.(string)
		return doc, ok
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, false
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return len(keys[a]) > len(keys[b]) })
	for _, k := range keys {
		rest, matched := "", path == k
		if !matched && strings.HasPrefix(path, k+".") {
			rest, matched = path[len(k)+1:], true
		}
		if !matched {
			continue
		}
		if v, ok := jsonField(obj[k], rest); ok {
			return v, true
		}
	}
	return nil, false
}
