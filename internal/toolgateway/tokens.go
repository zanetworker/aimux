package toolgateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
	"sync"

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

	mu       sync.RWMutex
	sessions map[string]*issued // by session
}

type issued struct {
	token []byte
	allow map[string][]string
	at    toolreg.Snapshot
}

// Issue makes a new token for session allowing allow (server to tool names)
// under snapshot at, and retires the session's earlier token. The token is
// 32 random bytes in hex. allow is copied.
func (t *Tokens) Issue(session string, allow map[string][]string, at toolreg.Snapshot) (string, error) {
	if session == "" {
		return "", errors.New("a token needs a session")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = map[string]*issued{}
	}
	t.sessions[session] = &issued{token: []byte(tok), allow: copyAllow(allow), at: at}
	return tok, nil
}

// Revoke retires session's token. An unknown session is ignored.
func (t *Tokens) Revoke(session string) {
	t.mu.Lock()
	delete(t.sessions, session)
	t.mu.Unlock()
}

// Lookup finds the session a token was issued to, with a copy of its
// allowed tools, less those that changed since its snapshot (see Current).
// Every issued token is compared in constant time, so the time taken does
// not reveal how much of a guess matched.
func (t *Tokens) Lookup(token string) (session string, allow map[string][]string, ok bool) {
	session, allow, at, ok := t.find(token)
	if !ok || t.Current == nil {
		return session, allow, ok
	}
	if cur := t.Current(); at.Version() != cur.Version() {
		allow = samePins(allow, at, cur)
	}
	return session, allow, true
}

// samePins is allow less each tool not pinned in both snapshots with the
// same hash and source. A server keeps its entry, possibly empty.
func samePins(allow map[string][]string, a, b toolreg.Snapshot) map[string][]string {
	out := make(map[string][]string, len(allow))
	for s, tools := range allow {
		kept := []string{}
		for _, tool := range tools {
			pa, okA := a.Pin(s, tool)
			pb, okB := b.Pin(s, tool)
			if okA && okB && pa.Hash == pb.Hash && pa.Source == pb.Source {
				kept = append(kept, tool)
			}
		}
		out[s] = kept
	}
	return out
}

func (t *Tokens) find(token string) (session string, allow map[string][]string, at toolreg.Snapshot, ok bool) {
	if token == "" {
		return "", nil, toolreg.Snapshot{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	found := 0
	var hit *issued
	for s, is := range t.sessions {
		if subtle.ConstantTimeCompare(is.token, []byte(token)) == 1 {
			found, session, hit = 1, s, is
		}
	}
	if found == 0 {
		return "", nil, toolreg.Snapshot{}, false
	}
	return session, copyAllow(hit.allow), hit.at, true
}

func copyAllow(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}
