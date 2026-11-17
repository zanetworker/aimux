package mcpproxy

import (
	"context"
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

// httpHeaders resolves server's header values from its secret refs, each
// item read once. Errors name the server, the header and the item, never
// a value.
func httpHeaders(ctx context.Context, server string, cfg ServerConfig, reader SecretReader) (map[string]string, error) {
	if cfg.Secrets == nil || len(cfg.Secrets.Headers) == 0 {
		return nil, nil
	}
	fix := fmt.Sprintf("unlock the Keychain and restart the proxy; if the item is gone, run aimux mcp proxy uninstall --without %s (then add %s again with claude mcp add)", server, server)
	read := memoReader(reader)
	headers := make(map[string]string, len(cfg.Secrets.Headers))
	for _, h := range sortedKeys(cfg.Secrets.Headers) {
		v, err := resolveSecret(ctx, cfg.Secrets.Headers[h], read)
		if err != nil {
			return nil, fmt.Errorf("%s header %s: %w; %s", server, h, err, fix)
		}
		headers[h] = v
	}
	return headers, nil
}

// memoReader reads each item once per call chain; nil stays nil.
func memoReader(reader SecretReader) SecretReader {
	if reader == nil {
		return nil
	}
	type result struct {
		v   string
		err error
	}
	seen := map[string]result{}
	return func(ctx context.Context, service string) (string, error) {
		if r, ok := seen[service]; ok {
			return r.v, r.err
		}
		v, err := reader(ctx, service)
		seen[service] = result{v, err}
		return v, err
	}
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
