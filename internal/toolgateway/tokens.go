package toolgateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
	"sync"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

var _ mcpproxy.TokenAuthorizer = (*Tokens)(nil)

// Tokens issues bearer tokens bound to one sandbox session. A token carries
// the tools that session may use per server, so a token leaked from one
// sandbox cannot be replayed from another with different rules. The zero
// value is ready to use.
type Tokens struct {
	mu       sync.RWMutex
	sessions map[string]*issued // by session
}

type issued struct {
	token []byte
	allow map[string][]string
}

// Issue makes a new token for session allowing allow (server to tool names)
// and retires the session's earlier token. The token is 32 random bytes in
// hex. allow is copied.
func (t *Tokens) Issue(session string, allow map[string][]string) (string, error) {
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
	t.sessions[session] = &issued{token: []byte(tok), allow: copyAllow(allow)}
	return tok, nil
}

// Revoke retires session's token. An unknown session is ignored.
func (t *Tokens) Revoke(session string) {
	t.mu.Lock()
	delete(t.sessions, session)
	t.mu.Unlock()
}

// Lookup finds the session a token was issued to, with a copy of its
// allowed tools. Every issued token is compared in constant time, so the
// time taken does not reveal how much of a guess matched.
func (t *Tokens) Lookup(token string) (session string, allow map[string][]string, ok bool) {
	if token == "" {
		return "", nil, false
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
		return "", nil, false
	}
	return session, copyAllow(hit.allow), true
}

func copyAllow(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}
