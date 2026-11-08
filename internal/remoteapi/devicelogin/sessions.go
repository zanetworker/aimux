package devicelogin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

// Login limits: a session ends after IdleLimit without use and in any case
// AbsoluteLimit after it began.
const (
	IdleLimit     = time.Hour
	AbsoluteLimit = 12 * time.Hour
)

// Session is a live login of one paired device.
type Session struct {
	DeviceID string
	Login    string // the Tailscale login it is valid for
	Created  time.Time
	LastUsed time.Time
	done     chan struct{}
}

// Done is closed when the session ends (logout, revoke, idle or absolute
// limit), so an open event stream can end with it.
func (s Session) Done() <-chan struct{} { return s.done }

// Sessions is the registry of live sessions, in memory only: a server
// restart ends every one. Tokens are kept only as their SHA-256.
type Sessions struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[[32]byte]*Session
}

// NewSessions returns an empty registry reading the time from now.
func NewSessions(now func() time.Time) *Sessions {
	return &Sessions{now: now, m: map[[32]byte]*Session{}}
}

// Create starts a session for deviceID, valid only with login, and returns
// its token (32 random bytes, base64url).
func (s *Sessions) Create(deviceID, login string) string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand, Go 1.24+)
	token := base64.RawURLEncoding.EncodeToString(b[:])
	t := s.now()
	s.mu.Lock()
	s.m[sha256.Sum256([]byte(token))] = &Session{DeviceID: deviceID, Login: login, Created: t, LastUsed: t, done: make(chan struct{})}
	s.mu.Unlock()
	return token
}

// Check returns the live session for token if it belongs to login. A
// session past a limit ends here. interaction (something the person did)
// moves the idle clock; polls and streams do not.
func (s *Sessions) Check(token, login string, interaction bool) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[key]
	if !ok {
		return Session{}, false
	}
	if expired(e, now) {
		s.end(key)
		return Session{}, false
	}
	if e.Login != login {
		return Session{}, false
	}
	if interaction {
		e.LastUsed = now
	}
	return *e, true
}

func expired(e *Session, now time.Time) bool {
	return now.Sub(e.LastUsed) >= IdleLimit || now.Sub(e.Created) >= AbsoluteLimit
}

// End ends the session for token, if any.
func (s *Sessions) End(token string) {
	s.mu.Lock()
	s.end(sha256.Sum256([]byte(token)))
	s.mu.Unlock()
}

// EndDevice ends every session of deviceID and returns how many there were.
func (s *Sessions) EndDevice(deviceID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, e := range s.m {
		if e.DeviceID == deviceID {
			s.end(k)
			n++
		}
	}
	return n
}

// Sweep ends the sessions past a limit, closing their Done channels.
func (s *Sessions) Sweep() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.m {
		if expired(e, now) {
			s.end(k)
		}
	}
}

// Done returns the channel closed when token's session ends; for a token
// with no session it is already closed.
func (s *Sessions) Done(token string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[sha256.Sum256([]byte(token))]; ok {
		return e.done
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s *Sessions) end(key [32]byte) { // under mu
	if e, ok := s.m[key]; ok {
		close(e.done)
		delete(s.m, key)
	}
}
