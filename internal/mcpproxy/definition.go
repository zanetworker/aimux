package mcpproxy

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/zanetworker/aimux/internal/keyring"
)

// SecretReader reads the Keychain item service.
type SecretReader func(ctx context.Context, service string) (string, error)

// SystemSecrets reads the items install wrote (keyring.Stored).
func SystemSecrets() SecretReader {
	return func(ctx context.Context, service string) (string, error) {
		return keyring.Stored(service).Lookup(ctx)
	}
}

// DefinitionStarter starts stdio definitions as StdioStarter does and HTTP
// ones (Type "http") with their headers read from secrets at each start.
func DefinitionStarter(cfgs map[string]ServerConfig, resolved []string, secrets SecretReader) Starter {
	stdio := StdioStarter(cfgs, resolved...)
	return func(ctx context.Context, server string) (Backend, error) {
		cfg, ok := cfgs[server]
		if !ok || cfg.Type != "http" {
			return stdio(ctx, server)
		}
		headers, err := httpHeaders(ctx, server, cfg, secrets)
		if err != nil {
			return nil, err
		}
		b, err := startHTTP(ctx, server, HTTPServerConfig{URL: cfg.URL, Headers: headers})
		if err != nil {
			return nil, err // not a nil *httpBackend inside a non-nil Backend
		}
		return &definedHTTPBackend{httpBackend: b, def: cfg}, nil
	}
}

// httpHeaders reads server's header values from its Keychain item. Errors
// name the item and the header, never a value.
func httpHeaders(ctx context.Context, server string, cfg ServerConfig, secrets SecretReader) (map[string]string, error) {
	if len(cfg.HeaderNames) == 0 {
		return nil, nil
	}
	service := HeaderService(server)
	fix := "run aimux mcp proxy uninstall and install again"
	if secrets == nil {
		return nil, fmt.Errorf("keychain item %s is missing or locked (no secret store here); %s", service, fix)
	}
	v, err := secrets(ctx, service)
	if err != nil {
		cause := "could not read it"
		if errors.Is(err, keyring.ErrNotFound) {
			cause = "not found"
		}
		return nil, fmt.Errorf("keychain item %s is missing or locked (%s); %s", service, cause, fix)
	}
	item, err := decodeHeaderSecret(v)
	if err != nil {
		return nil, fmt.Errorf("keychain item %s: %w; %s", service, err, fix)
	}
	headers := make(map[string]string, len(cfg.HeaderNames))
	for _, h := range cfg.HeaderNames {
		it, ok := item.Headers[h]
		if !ok {
			return nil, fmt.Errorf("keychain item %s has no value for header %s; %s", service, h, fix)
		}
		headers[h] = it.Value
	}
	return headers, nil
}

// definedHTTPBackend is an HTTP backend started from a saved definition; it
// keeps that definition so a catalog refresh can tell whether it was
// replaced meanwhile (CatalogUpdater.superseded), as stdio backends do.
type definedHTTPBackend struct {
	*httpBackend
	def ServerConfig
}

// Definition is the definition the backend was started with.
func (b *definedHTTPBackend) Definition() ServerConfig { return b.def }

// SystemVault is the Keychain on macOS, where install can keep header
// values; nil elsewhere, so HTTP servers with headers stay direct.
func SystemVault() VaultStore {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return keychainVault{}
}

// keychainVault keeps items with keyring.Store, reads them with
// keyring.Stored and removes them with keyring.Delete.
type keychainVault struct{}

func (keychainVault) Put(service, value string) error { return keyring.Store(service, value) }
func (keychainVault) Get(ctx context.Context, service string) (string, error) {
	return keyring.Stored(service).Lookup(ctx)
}
func (keychainVault) Delete(service string) error { return keyring.Delete(service) }
func (keychainVault) MaxValue(service string) int { return keyring.MaxStoreValue(service) }
