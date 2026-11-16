package mcpproxy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
)

// TokenAuthorizer maps a bearer token to the sandbox session it was issued
// to and the tools that session may use per server. toolgateway.Tokens is
// the implementation; it lives there because toolgateway imports this
// package.
type TokenAuthorizer interface {
	Lookup(token string) (session string, allow map[string][]string, ok bool)
}

// tokenAccess is what a request's session token allows; the guard puts it in
// the request context for the tool list and the call guard.
type tokenAccess struct {
	session string
	allow   map[string][]string
}

type tokenAccessKey struct{}

func withTokenAccess(ctx context.Context, a tokenAccess) context.Context {
	return context.WithValue(ctx, tokenAccessKey{}, a)
}

// tokenAccessFrom is the access the request's session token grants; ok is
// false for a request that came without one (stdio, the admin Token).
func tokenAccessFrom(ctx context.Context) (tokenAccess, bool) {
	a, ok := ctx.Value(tokenAccessKey{}).(tokenAccess)
	return a, ok
}

// permits reports whether the token allows tool on server.
func (a tokenAccess) permits(server, tool string) bool {
	for _, t := range a.allow[server] {
		if t == tool {
			return true
		}
	}
	return false
}

// tokenGuard admits a request bearing admin (when non-empty) or a token
// tokens issued. An issued token may only reach /mcp/<server> for a server
// in its allow map; anything else, admin routes included, is 403. The
// token is never written to a log or an error.
func tokenGuard(admin string, tokens TokenAuthorizer, binds *sessionBinds, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearerOf(r.Header.Get("Authorization"))
		if ok && admin != "" && subtle.ConstantTimeCompare([]byte(got), []byte(admin)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		var (
			session string
			allow   map[string][]string
		)
		if ok {
			session, allow, ok = tokens.Lookup(got)
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		name, isMCP := lookupName(r)
		if _, permitted := allow[name]; !isMCP || !permitted {
			http.Error(w, "this token does not reach that endpoint", http.StatusForbidden)
			return
		}
		// An MCP session belongs to the token that created it. An id that
		// this token did not get from its own initialize (unrecorded,
		// made up, or another token's, a revoked token's, or the admin's)
		// is answered as an unknown session: the client starts a new one,
		// and nothing is forwarded or stored.
		digest := sha256.Sum256([]byte(got))
		sid := r.Header.Get("Mcp-Session-Id")
		if sid != "" && !binds.owns(sid, digest) {
			http.NotFound(w, r)
			return
		}
		bw := &bindOnSession{ResponseWriter: w, binds: binds, digest: digest, create: sid == ""}
		w = bw
		if sid != "" && r.Method == http.MethodDelete {
			defer func() {
				if bw.status < 400 {
					binds.forget(sid)
				}
			}()
		}
		next.ServeHTTP(w, r.WithContext(withTokenAccess(r.Context(), tokenAccess{session: session, allow: allow})))
	})
}

func bearerOf(h string) (string, bool) {
	const scheme = "bearer "
	if len(h) < len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(scheme):]), true
}

// sessionBinds records which token (by digest, so the token itself is never
// kept) created each MCP session. A revoked token's sessions stay bound to
// it, so no other token can use them.
type sessionBinds struct {
	mu    sync.Mutex
	owner map[string][32]byte
}

// owns reports whether the token with this digest created session sid.
func (b *sessionBinds) owns(sid string, digest [32]byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.owner[sid]
	return ok && o == digest
}

// forget drops the binding of an ended session.
func (b *sessionBinds) forget(sid string) {
	b.mu.Lock()
	delete(b.owner, sid)
	b.mu.Unlock()
}

func newSessionBinds() *sessionBinds { return &sessionBinds{owner: map[string][32]byte{}} }

func (b *sessionBinds) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.owner)
}

func (b *sessionBinds) bind(sid string, digest [32]byte) {
	b.mu.Lock()
	b.owner[sid] = digest
	b.mu.Unlock()
}

// bindOnSession records the session id the transport assigns to a request
// that came without one (initialize), before the response is sent.
type bindOnSession struct {
	http.ResponseWriter
	binds  *sessionBinds
	digest [32]byte
	create bool // the request had no session id, so one may be assigned
	done   bool
	status int
}

func (w *bindOnSession) record() {
	if w.done {
		return
	}
	w.done = true
	if !w.create {
		return
	}
	if sid := w.Header().Get("Mcp-Session-Id"); sid != "" {
		w.binds.bind(sid, w.digest)
	}
}

func (w *bindOnSession) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.record()
	w.ResponseWriter.WriteHeader(code)
}
func (w *bindOnSession) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.record()
	return w.ResponseWriter.Write(p)
}
func (w *bindOnSession) Flush() {
	w.record()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *bindOnSession) Unwrap() http.ResponseWriter { return w.ResponseWriter }
