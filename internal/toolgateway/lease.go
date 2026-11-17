package toolgateway

import (
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/zanetworker/aimux/internal/toolreg"
)

// ErrUnknownLease is a Renew of a session with no live lease: never put,
// deleted, expired, or lost when the gateway worker restarted.
var ErrUnknownLease = errors.New("unknown lease")

// Lease is one turn's token as remote serve hands it to the gateway worker:
// the tools the session may use, the snapshot version they were compiled
// under, each allowed tool's pin there (so the worker can drop tools whose
// pins differ in the snapshot it serves) and when it expires.
type Lease struct {
	Session string                            `json:"session"`
	Token   string                            `json:"token"`
	Allow   map[string][]string               `json:"allow"`
	Version string                            `json:"version"`
	Pins    map[string]map[string]toolreg.Pin `json:"pins"`
	Expires time.Time                         `json:"expires"`
}

// Put stores l, replacing the session's earlier lease. It needs a session,
// a token and an expiry in the future. The maps are copied.
func (t *Tokens) Put(l Lease) error {
	if l.Session == "" || l.Token == "" {
		return errors.New("a lease needs a session and a token")
	}
	if !l.Expires.After(t.now()) {
		return fmt.Errorf("lease for session %s expires in the past (%s)", l.Session, l.Expires.Format(time.RFC3339))
	}
	pins := make(map[string]map[string]toolreg.Pin, len(l.Pins))
	for s, m := range l.Pins {
		pins[s] = maps.Clone(m)
	}
	t.store(l.Session, &issued{token: []byte(l.Token), allow: copyAllow(l.Allow), version: l.Version, pins: pins, expires: l.Expires})
	return nil
}

// Renew moves the session's lease expiry to until. ErrUnknownLease when the
// session has no live lease.
func (t *Tokens) Renew(session string, until time.Time) error {
	now := t.now()
	if !until.After(now) {
		return fmt.Errorf("renew of session %s to %s: the new expiry must be in the future", session, until.Format(time.RFC3339))
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	is, ok := t.sessions[session]
	if !ok || (!is.expires.IsZero() && !now.Before(is.expires)) {
		delete(t.sessions, session)
		return fmt.Errorf("%w: session %s", ErrUnknownLease, session)
	}
	if !is.expires.IsZero() {
		is.expires = until
	}
	return nil
}
