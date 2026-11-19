package toolgateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

var _ mcpproxy.TokenAuthorizer = (*Tokens)(nil)

// Tokens issues bearer tokens bound to one sandbox session. A token carries
// the tools that session may use per server, so a token leaked from one
// sandbox cannot be replayed from another with different rules. A token
// also carries the registry and pins snapshot its session was compiled
// with. The zero value is ready to use.
type Tokens struct {
	// Current, if set, is the snapshot the gateway serves now (set before
	// the first Lookup). A token issued under another snapshot, older or
	// newer, loses at each Lookup every tool whose pin hash or source is
	// not the same in both (changed, re-stamped, added or removed): a newer
	// token reaches servers still built for the older pins while a refresh
	// is pending, so either direction fails closed. Tools whose pins did
	// not change stay, so an unrelated edit does not break a running
	// session.
	Current func() toolreg.Snapshot
	// Now is the clock leases expire by (nil: time.Now).
	Now func() time.Time
	// Grace: for Grace after StartGrace (a worker that just started knows
	// no leases yet), WaitLookup holds a request with an unknown token until
	// its lease is put; MaxWaiting bounds how many it holds (0: 64). Grace 0
	// never holds.
	Grace      time.Duration
	MaxWaiting int

	mu       sync.Mutex
	sessions map[string]*issued // by session
	changed  chan struct{}      // closed and replaced on every store
	waiting  atomic.Int32
	graceAt  time.Time // under mu: when the grace window (re)started
}

// StartGrace (re)starts the grace window at at: call it when the worker
// starts, and again once it can take leases.
func (t *Tokens) StartGrace(at time.Time) {
	t.mu.Lock()
	t.graceAt = at
	t.mu.Unlock()
}

type issued struct {
	token   []byte
	allow   map[string][]string
	version string
	pins    map[string]map[string]toolreg.Pin
	expires time.Time // zero: never (Issue)
}

func (t *Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Issue makes a new token for session allowing allow (server to tool names)
// under snapshot at, and retires the session's earlier token. The token is
// 32 random bytes in hex. allow is copied. An issued token does not expire:
// its in-process caller revokes it; a lease from another process is Put.
func (t *Tokens) Issue(session string, allow map[string][]string, at toolreg.Snapshot) (string, error) {
	if session == "" {
		return "", errors.New("a token needs a session")
	}
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	t.store(session, &issued{token: []byte(tok), allow: copyAllow(allow), version: at.Version(), pins: PinsFor(at, allow)})
	return tok, nil
}

func (t *Tokens) store(session string, is *issued) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = map[string]*issued{}
	}
	t.sessions[session] = is
	if t.changed != nil {
		close(t.changed) // wake requests waiting for a token
		t.changed = nil
	}
}

// changes is a channel closed at the next store.
func (t *Tokens) changes() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.changed == nil {
		t.changed = make(chan struct{})
	}
	return t.changed
}

// WaitLookup is Lookup that, inside the startup grace window, holds a
// request whose token is unknown until its lease is put, the window ends
// or ctx ends. Outside the window it answers at once, as Lookup does.
func (t *Tokens) WaitLookup(ctx context.Context, token string) (string, map[string][]string, bool, error) {
	if s, a, ok := t.Lookup(token); ok || token == "" {
		return s, a, ok, nil
	}
	t.mu.Lock()
	end := t.graceAt.Add(t.Grace)
	opened := !t.graceAt.IsZero()
	t.mu.Unlock()
	if !opened {
		return "", nil, false, nil
	}
	if t.Grace <= 0 || !t.now().Before(end) {
		return "", nil, false, nil
	}
	limit := t.MaxWaiting
	if limit <= 0 {
		limit = 64
	}
	if int(t.waiting.Add(1)) > limit {
		t.waiting.Add(-1)
		return "", nil, false, mcpproxy.ErrTokenWaitBusy
	}
	defer t.waiting.Add(-1)
	timer := time.NewTimer(time.Until(end))
	defer timer.Stop()
	for {
		changed := t.changes()
		if s, a, ok := t.Lookup(token); ok {
			return s, a, true, nil
		}
		select {
		case <-changed:
		case <-timer.C:
			s, a, ok := t.Lookup(token)
			return s, a, ok, nil
		case <-ctx.Done():
			return "", nil, false, nil
		}
	}
}

// Revoke retires session's token. An unknown session is ignored.
func (t *Tokens) Revoke(session string) {
	t.mu.Lock()
	delete(t.sessions, session)
	t.mu.Unlock()
}

// Lookup finds the session a token was issued to, with a copy of its
// allowed tools, less those that changed since its snapshot (see Current).
// An expired lease is absent and dropped. Every token is compared in
// constant time, so the time taken does not reveal how much of a guess
// matched.
func (t *Tokens) Lookup(token string) (session string, allow map[string][]string, ok bool) {
	session, is, ok := t.find(token)
	if !ok {
		return "", nil, false
	}
	allow = copyAllow(is.allow)
	if t.Current == nil {
		return session, allow, true
	}
	if cur := t.Current(); is.version != cur.Version() {
		allow = samePins(allow, is.pins, cur)
	}
	return session, allow, true
}

// samePins is allow less each tool not pinned in both with the same hash
// and source, both sources read by b's registry (so a URL pin and the
// identity it stands for are the same source, across an approve upgrade
// or aimux servers migrate). A server keeps its entry, possibly empty.
func samePins(allow map[string][]string, a map[string]map[string]toolreg.Pin, b toolreg.Snapshot) map[string][]string {
	out := make(map[string][]string, len(allow))
	for s, tools := range allow {
		kept := []string{}
		for _, tool := range tools {
			pa, okA := a[s][tool]
			pb, okB := b.Pin(s, tool)
			if okA && okB && pa.Hash == pb.Hash && resolvedSource(b, s, pa) == resolvedSource(b, s, pb) {
				kept = append(kept, tool)
			}
		}
		out[s] = kept
	}
	return out
}

// resolvedSource is p's recorded source as snap's registry reads it: a pin
// that recorded the server's URL, which the registry accepts for a server
// with an identity, stands for that identity. So approve rewriting a URL
// pin to the identity it already matched is no change for a lease, while
// a real identity change still is. A v1 pin (no source) stays unknown.
func resolvedSource(snap toolreg.Snapshot, server string, p toolreg.Pin) string {
	if p.Source == "" || strings.HasPrefix(p.Source, "id:") || snap.SourceChangedFrom(server, p.Source) {
		return p.Source
	}
	return snap.PinSource(server)
}

// PinsFor is snap's pin of each tool in allow, the Pins a Lease carries,
// each source resolved as snap reads it (see resolvedSource).
func PinsFor(snap toolreg.Snapshot, allow map[string][]string) map[string]map[string]toolreg.Pin {
	out := map[string]map[string]toolreg.Pin{}
	for s, tools := range allow {
		for _, tool := range tools {
			if p, ok := snap.Pin(s, tool); ok {
				p.Source = resolvedSource(snap, s, p)
				if out[s] == nil {
					out[s] = map[string]toolreg.Pin{}
				}
				out[s][tool] = p
			}
		}
	}
	return out
}

func (t *Tokens) find(token string) (string, *issued, bool) {
	if token == "" {
		return "", nil, false
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	session, hit := "", (*issued)(nil)
	for s, is := range t.sessions {
		// Expired leases are dropped on every scan, so leases whose owner
		// crashed before deleting them do not pile up.
		if !is.expires.IsZero() && !now.Before(is.expires) {
			delete(t.sessions, s)
			continue
		}
		if subtle.ConstantTimeCompare(is.token, []byte(token)) == 1 {
			session, hit = s, is
		}
	}
	return session, hit, hit != nil
}

func copyAllow(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}

// Len is the number of live tokens and leases; expired leases are dropped.
func (t *Tokens) Len() int {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for s, is := range t.sessions {
		if !is.expires.IsZero() && !now.Before(is.expires) {
			delete(t.sessions, s)
		}
	}
	return len(t.sessions)
}
