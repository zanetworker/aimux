package toolgateway

import (
	"crypto/subtle"
	"errors"
	"slices"
	"sync"
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

	mu       sync.Mutex
	sessions map[string]*issued // by session
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
// and source. A server keeps its entry, possibly empty.
func samePins(allow map[string][]string, a map[string]map[string]toolreg.Pin, b toolreg.Snapshot) map[string][]string {
	out := make(map[string][]string, len(allow))
	for s, tools := range allow {
		kept := []string{}
		for _, tool := range tools {
			pa, okA := a[s][tool]
			pb, okB := b.Pin(s, tool)
			if okA && okB && pa.Hash == pb.Hash && pa.Source == pb.Source {
				kept = append(kept, tool)
			}
		}
		out[s] = kept
	}
	return out
}

// PinsFor is snap's pin of each tool in allow, the Pins a Lease carries.
func PinsFor(snap toolreg.Snapshot, allow map[string][]string) map[string]map[string]toolreg.Pin {
	out := map[string]map[string]toolreg.Pin{}
	for s, tools := range allow {
		for _, tool := range tools {
			if p, ok := snap.Pin(s, tool); ok {
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
