package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/jsonedit"
)

// VaultStore holds the header values of moved HTTP servers (the Keychain).
type VaultStore interface {
	Put(service, value string) error
	Get(ctx context.Context, service string) (string, error)
	Delete(service string) error
	// MaxValue is the longest value Put accepts for service.
	MaxValue(service string) int
}

// refreshAll lists the tools of names into the catalog at catalogPath:
// stdio servers through Refresh, HTTP servers (in http) through
// RefreshHTTP. Failures by server.
func (i Installer) refreshAll(catalogPath string, names []string, http map[string]HTTPEntry) map[string]error {
	var stdio, web []string
	for _, n := range names {
		if _, ok := http[n]; ok {
			web = append(web, n)
		} else {
			stdio = append(stdio, n)
		}
	}
	failed := map[string]error{}
	if len(stdio) > 0 {
		for n, err := range i.Refresh(catalogPath, stdio) {
			if err != nil {
				failed[n] = err
			}
		}
	}
	if len(web) > 0 {
		refresh := i.RefreshHTTP
		if refresh == nil {
			refresh = refreshHTTPCatalog
		}
		for n, err := range refresh(catalogPath, http, web) {
			if err != nil {
				failed[n] = err
			}
		}
	}
	return failed
}

// refreshHTTPCatalog lists the tools of the named HTTP servers with their
// headers held in memory and saves them in the catalog at catalogPath.
func refreshHTTPCatalog(catalogPath string, entries map[string]HTTPEntry, names []string) map[string]error {
	fail := func(err error) map[string]error {
		out := map[string]error{}
		for _, n := range names {
			out[n] = err
		}
		return out
	}
	cat, err := LoadCatalog(catalogPath)
	if err != nil {
		return fail(err)
	}
	cfgs := map[string]HTTPServerConfig{}
	for _, n := range names {
		cfgs[n] = HTTPServerConfig{URL: entries[n].URL, Headers: entries[n].Headers}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	errs := RefreshCatalog(ctx, cat, HTTPStarter(cfgs), names)
	if err := cat.Save(catalogPath); err != nil {
		return fail(err)
	}
	return errs
}

// storeHeaders writes one Keychain item per HTTP server with headers and
// returns the items written, for deleteItems on a failed run.
func (i Installer) storeHeaders(http map[string]HTTPEntry) ([]string, error) {
	var written []string
	for _, name := range sortedKeys(http) {
		e := http[name]
		if len(e.Headers) == 0 {
			continue
		}
		if i.Vault == nil {
			return written, fmt.Errorf("%s has headers but there is no Keychain to hold them", name)
		}
		enc, err := encodeHeaderSecret(headerSecretOf(e))
		if err != nil {
			return written, err
		}
		if err := i.Vault.Put(HeaderService(name), enc); err != nil {
			return written, fmt.Errorf("store the headers of %s: %w", name, err)
		}
		written = append(written, HeaderService(name))
	}
	return written, nil
}

// deleteItems removes Keychain items a failed run wrote; failures are
// noted, not returned.
func (i Installer) deleteItems(services []string) {
	for _, s := range services {
		if err := i.Vault.Delete(s); err != nil {
			i.printf("Note: could not delete Keychain item %s (%v); remove it with: security delete-generic-password -s %s\n", s, err, s)
		}
	}
}

// movedEntry is the manifest record of one moved server: an HTTP server
// with headers gets its Original redacted and its Keychain item named.
func (i Installer) movedEntry(name string, raw json.RawMessage, http map[string]HTTPEntry) (MovedEntry, error) {
	e := MovedEntry{Original: string(raw), Written: string(i.entryFor(name))}
	if h, ok := http[name]; ok && len(h.Literals) > 0 {
		red, _, err := redactHeaders(raw)
		if err != nil {
			return MovedEntry{}, fmt.Errorf("record %s: %w", name, err)
		}
		e.Original, e.Keychain = string(red), HeaderService(name)
	}
	return e, nil
}

// extendedWith is extended with the OAuth servers this run found recorded.
func (i Installer) extendedWith(m *Manifest, servers map[string]MovedEntry, skipped []string, direct []KeptDirect) *Manifest {
	out := i.extended(m, servers, skipped)
	out.Direct = direct
	return out
}

// redactBackup replaces the header values of the HTTP servers this run
// moves in the backup install just wrote; the rest of it stays byte for
// byte. Uninstall never reads backups.
func (i Installer) redactBackup(path string, http map[string]HTTPEntry) error {
	var names []string
	for _, n := range sortedKeys(http) {
		if len(http[n].Literals) > 0 {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	err := jsonedit.EditKeyVerbatim(path, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		members, err := objectMembers(old)
		if err != nil {
			return nil, err
		}
		cur := old
		for _, n := range names {
			raw, ok := members[n]
			if !ok {
				continue
			}
			red, _, err := redactHeaders(raw)
			if err != nil {
				return nil, err
			}
			if cur, err = jsonedit.ReplaceMemberVerbatim(cur, n, red); err != nil {
				return nil, err
			}
		}
		return cur, nil
	})
	if err != nil {
		return fmt.Errorf("remove header values from the backup %s: %w", path, err)
	}
	return nil
}

// noteOlderBackups prints one note when older backups still hold the
// headers of a server this run moved (found by name, never by value).
func (i Installer) noteOlderBackups(current string, http map[string]HTTPEntry) {
	var names []string
	for _, n := range sortedKeys(http) {
		if len(http[n].Literals) > 0 {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return
	}
	entries, err := os.ReadDir(i.BackupDir)
	if err != nil {
		return
	}
	prefix := filepath.Base(i.ClaudeConfig) + "."
	var paths []string
	holders := map[string]bool{}
	for _, e := range entries {
		p := filepath.Join(i.BackupDir, e.Name())
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || p == current {
			continue
		}
		_, members, err := readMCPServers(p)
		if err != nil {
			continue
		}
		found := false
		for _, n := range names {
			if spans, err := headerSpans(members[n]); err == nil && len(spans) > 0 && !onlyPlaceholders(members[n], spans) {
				holders[n], found = true, true
			}
		}
		if found {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return
	}
	sort.Strings(paths)
	i.printf("note: %d older backups in %s still hold header values for %s; delete them with: rm %s\n",
		len(paths), i.BackupDir, strings.Join(sortedKeys(holders), ", "), strings.Join(paths, " "))
}

// onlyPlaceholders reports whether every header value in raw is the
// placeholder (a backup install already redacted).
func onlyPlaceholders(raw json.RawMessage, spans []headerSpan) bool {
	for _, s := range spans {
		if string(raw[s.start:s.end]) != headerPlaceholder {
			return false
		}
	}
	return true
}

// printKeptDirect prints one line per HTTP server left direct.
func (i Installer) printKeptDirect(kept []KeptDirect) {
	for _, k := range kept {
		i.printf("  kept direct: %s: %s\n", k.Server, k.Reason)
	}
}

// unredacted returns m.Servers with each Keychain-backed Original rebuilt
// from its item; any unreadable item fails before anything changes. Errors
// name the item, never a value.
func (i Installer) unredacted(ctx context.Context, m *Manifest, current map[string]json.RawMessage) (map[string]MovedEntry, error) {
	out := make(map[string]MovedEntry, len(m.Servers))
	for name, e := range m.Servers {
		// Only an entry that is still the proxy entry gets its original back;
		// for any other (restored already, changed, removed) the item is not
		// needed, so an uninstall interrupted after deleting items can finish.
		if e.Keychain == "" {
			out[name] = e
			continue
		}
		// The item is read whenever it can be, so the edit restores real
		// values even if the entry changes meanwhile. It may be missing only
		// when the entry needs none of its values: not the proxy entry and
		// no placeholder left (an uninstall that stopped after deleting it).
		needed := jsonEqual(current[name], json.RawMessage(e.Written)) || anyPlaceholder(current[name])
		fix := fmt.Sprintf("nothing was changed; unlock the Keychain (or restore %s from %s) and run uninstall again", name, i.BackupDir)
		if i.Vault == nil {
			if !needed {
				out[name] = e
				continue
			}
			return nil, fmt.Errorf("the original entry of %s is in Keychain item %s, which this system cannot read; %s", name, e.Keychain, fix)
		}
		v, err := i.Vault.Get(ctx, e.Keychain)
		if err != nil && !needed {
			out[name] = e
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Keychain item %s for %s: could not read it; %s", e.Keychain, name, fix)
		}
		item, err := decodeHeaderSecret(v)
		if err != nil {
			return nil, fmt.Errorf("keychain item %s for %s: %w; %s", e.Keychain, name, err, fix)
		}
		literals := make(map[string]string, len(item.Headers))
		for h, it := range item.Headers {
			literals[h] = it.literal()
		}
		orig, err := restoreHeaders(json.RawMessage(e.Original), literals)
		if err != nil {
			return nil, fmt.Errorf("rebuild the entry of %s from Keychain item %s: %w; %s", name, e.Keychain, err, fix)
		}
		e.redacted, e.Original = e.Original, string(orig)
		out[name] = e
	}
	return out, nil
}

// dropItems deletes the Keychain items of the servers uninstall restored;
// a server whose entry the owner changed or removed keeps its item, with a
// note. A failed delete is a note, not an error.
func (i Installer) dropItems(m *Manifest, restored []string) {
	for _, name := range sortedKeys(m.Servers) {
		svc := m.Servers[name].Keychain
		if svc == "" || i.Vault == nil {
			continue
		}
		if !contains(restored, name) {
			i.printf("Kept Keychain item %s (its entry changed since install); remove it with: security delete-generic-password -s %s\n", svc, svc)
			continue
		}
		if err := i.Vault.Delete(svc); err != nil {
			i.printf("Note: could not delete Keychain item %s (%v); remove it with: security delete-generic-password -s %s\n", svc, err, svc)
		}
	}
}

// sameRedacted reports whether entry, with its header values replaced by
// the placeholder, equals redacted (an entry uninstall already restored).
func sameRedacted(entry json.RawMessage, redacted string) bool {
	red, _, err := redactHeaders(entry)
	return err == nil && jsonEqual(red, json.RawMessage(redacted))
}

// headersTooLong is the kept-direct reason when e's Keychain item would not
// fit what keyring.Store can write, or "".
func headersTooLong(v VaultStore, name string, e HTTPEntry) string {
	if len(e.Headers) == 0 || v == nil {
		return ""
	}
	enc, err := encodeHeaderSecret(headerSecretOf(e))
	if max := v.MaxValue(HeaderService(name)); err != nil || len(enc) > max {
		return fmt.Sprintf("header values too long for the Keychain (%d bytes; at most %d)", len(enc), max)
	}
	return ""
}

// anyPlaceholder reports whether any header value in raw is the
// placeholder (an entry put back from a backup install redacted).
func anyPlaceholder(raw json.RawMessage) bool {
	spans, err := headerSpans(raw)
	if err != nil {
		return false
	}
	for _, s := range spans {
		if string(raw[s.start:s.end]) == headerPlaceholder {
			return true
		}
	}
	return false
}

// KeptDirectLines groups kept-direct servers by reason, one line each:
// "kept direct: <reason>: <names>".
func KeptDirectLines(kept []KeptDirect) []string {
	by := map[string][]string{}
	for _, k := range kept {
		by[k.Reason] = append(by[k.Reason], k.Server)
	}
	var out []string
	for _, reason := range sortedKeys(by) {
		names := by[reason]
		sort.Strings(names)
		out = append(out, "kept direct: "+reason+": "+strings.Join(names, ", "))
	}
	return out
}
