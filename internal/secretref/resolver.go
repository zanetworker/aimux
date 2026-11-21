package secretref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
)

// Options are the resolver's seams; zero values use the real Keychain, the
// process environment and the clock.
type Options struct {
	// Keychain reads an item's value: aimux's own items (account aimux) first,
	// then any generic password with that service.
	Keychain func(ctx context.Context, item string) (string, error)
	Getenv   func(name string) (string, bool)
	Now      func() time.Time
	Owner    func(fs.FileInfo) bool
}

// Resolver resolves references and remembers every value it returned in one
// scrubber. It is safe for concurrent use.
type Resolver struct {
	opt   Options
	scrub *Scrubber

	mu    sync.Mutex
	cache map[string]cmdResult // cmd results by argv
}

type cmdResult struct {
	value string
	err   error
	until time.Time
}

// failureCacheFor is how long a failed cmd is not run again.
const failureCacheFor = 30 * time.Second

var userHome = os.UserHomeDir

// NewResolver returns a resolver with opts' seams.
func NewResolver(opts Options) *Resolver {
	if opts.Keychain == nil {
		opts.Keychain = keychainLookup(
			func(ctx context.Context, item string) (string, error) { return keyring.Stored(item).Lookup(ctx) },
			func(ctx context.Context, item string) (string, error) { return keyring.System(item).Lookup(ctx) })
	}
	if opts.Getenv == nil {
		opts.Getenv = os.LookupEnv
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Owner == nil {
		opts.Owner = ownedByCurrentUser
	}
	return &Resolver{opt: opts, scrub: NewScrubber(), cache: map[string]cmdResult{}}
}

// keychainLookup reads aimux's own item (scoped) and falls back to any
// generic password with that service (legacy, as aimux-github is) only when
// the scoped item is absent, never when it exists but cannot be read.
func keychainLookup(scoped, legacy func(context.Context, string) (string, error)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, item string) (string, error) {
		v, err := scoped(ctx, item)
		if err == nil || !errors.Is(err, keyring.ErrNotFound) {
			return v, err
		}
		return legacy(ctx, item)
	}
}

// Scrubber holds every value this resolver returned.
func (r *Resolver) Scrubber() *Scrubber { return r.scrub }

// Resolve returns the values of server's refs. Each value is added to the
// scrubber before Resolve returns. An error names the server, the secret and
// the backend, never a value.
func (r *Resolver) Resolve(ctx context.Context, server string, refs Refs) (Values, error) {
	out := Values{Env: map[string]string{}, Headers: map[string]string{}}
	for _, part := range []struct {
		kind string
		refs map[string]Ref
		into map[string]string
	}{{"env", refs.Env, out.Env}, {"header", refs.Headers, out.Headers}} {
		for _, name := range sortedNames(part.refs) {
			v, err := r.one(ctx, part.refs[name])
			if err != nil {
				return Values{}, fmt.Errorf("secret %s %s of %s (%s): %w", part.kind, name, server, part.refs[name].Backend(), err)
			}
			r.scrub.Add(v)
			part.into[name] = v
		}
	}
	return out, nil
}

// CheckResult is one reference's state for `aimux secrets check`; it never
// holds a value.
type CheckResult struct {
	Name    string
	Backend string
	OK      bool
	Reason  string
}

// Check resolves each of server's refs and reports whether it works, without
// returning any value. Results are sorted by name.
func (r *Resolver) Check(ctx context.Context, server string, refs Refs) []CheckResult {
	var out []CheckResult
	for _, part := range []struct {
		kind string
		refs map[string]Ref
	}{{"env", refs.Env}, {"header", refs.Headers}} {
		for _, name := range sortedNames(part.refs) {
			ref := part.refs[name]
			res := CheckResult{Name: part.kind + ":" + name, Backend: ref.Backend(), OK: true}
			if v, err := r.one(ctx, ref); err != nil {
				res.OK, res.Reason = false, err.Error()
			} else {
				r.scrub.Add(v)
			}
			if ref.Env != nil {
				res.Reason = strings.TrimSpace(res.Reason + " (from aimux's environment, not your shell's)")
			}
			out = append(out, res)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Resolver) one(ctx context.Context, ref Ref) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	switch {
	case ref.Keychain != nil:
		return r.keychain(ctx, *ref.Keychain)
	case ref.File != nil:
		return r.file(*ref.File)
	case ref.Cmd != nil:
		return r.cmd(ctx, *ref.Cmd)
	default:
		v, ok := r.opt.Getenv(ref.Env.Name)
		if !ok || v == "" {
			return "", fmt.Errorf("%s is not set in aimux's environment (launchd's, not your shell's)", ref.Env.Name)
		}
		return v, nil
	}
}

func (r *Resolver) keychain(ctx context.Context, k KeychainRef) (string, error) {
	raw, err := r.opt.Keychain(ctx, k.Item)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", fmt.Errorf("keychain item %s not found", k.Item)
		}
		return "", fmt.Errorf("keychain item %s: cannot read it", k.Item)
	}
	if k.Field == "" {
		return strings.TrimSpace(raw), nil
	}
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", fmt.Errorf("keychain item %s is not JSON, so it has no field %s", k.Item, k.Field)
	}
	v, ok := jsonPath(doc, k.Field)
	if !ok {
		return "", fmt.Errorf("keychain item %s has no string field %s", k.Item, k.Field)
	}
	return v, nil
}

// jsonPath walks a dotted path whose keys may themselves hold dots: at each
// object the longest key that matches a prefix of the rest is taken.
func jsonPath(doc any, path string) (string, bool) {
	if path == "" {
		s, ok := doc.(string)
		return s, ok
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return "", false
	}
	parts := strings.Split(path, ".")
	for i := len(parts); i >= 1; i-- {
		key := strings.Join(parts[:i], ".")
		if next, ok := m[key]; ok {
			if v, ok := jsonPath(next, strings.Join(parts[i:], ".")); ok {
				return v, true
			}
		}
	}
	return "", false
}

func sortedNames(m map[string]Ref) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
