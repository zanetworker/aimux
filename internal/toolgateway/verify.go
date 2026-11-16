package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrToolChanged means the backend's live tool no longer matches its pin,
	// or the backend no longer lists it.
	ErrToolChanged = errors.New("tool changed since it was pinned")
	// ErrToolNotPinned means the tool has no stored pin.
	ErrToolNotPinned = errors.New("tool is not pinned")
	// ErrVerifyFailed means the live tool list could not be read, so the
	// tool could not be checked. It wraps the listing error.
	ErrVerifyFailed = errors.New("could not verify tool")
)

// DefaultMaxAge is how long a listing is trusted.
const DefaultMaxAge = 60 * time.Second

// Verifier checks a tool against its pin using the backend's live listing.
// A listing is reused for MaxAge; a failed listing is not kept, so the next
// Check lists again.
type Verifier struct {
	Pins   Pins
	List   func(ctx context.Context, server string) ([]json.RawMessage, error)
	MaxAge time.Duration    // 0: DefaultMaxAge
	Now    func() time.Time // nil: time.Now

	mu      sync.Mutex
	servers map[string]*serverState
}

type serverState struct {
	mu   sync.Mutex // held while listing, so concurrent checks share one listing
	at   time.Time
	pins map[string]string // live pin by tool name; nil: nothing cached
}

// Check returns nil when tool on server is pinned and its live pin equals
// the stored one.
func (v *Verifier) Check(ctx context.Context, server, tool string) error {
	want, ok := v.Pins[server][tool]
	if !ok {
		return fmt.Errorf("%s/%s: %w", server, tool, ErrToolNotPinned)
	}
	live, err := v.live(ctx, server)
	if err != nil {
		return err
	}
	got, ok := live[tool]
	if !ok {
		return fmt.Errorf("%s/%s: %w (no longer listed)", server, tool, ErrToolChanged)
	}
	if got != want {
		return fmt.Errorf("%s/%s: %w", server, tool, ErrToolChanged)
	}
	return nil
}

// Invalidate drops the cached listing for server, so the next Check lists it.
func (v *Verifier) Invalidate(server string) {
	st := v.state(server)
	st.mu.Lock()
	st.pins = nil
	st.mu.Unlock()
}

func (v *Verifier) state(server string) *serverState {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.servers == nil {
		v.servers = map[string]*serverState{}
	}
	st := v.servers[server]
	if st == nil {
		st = &serverState{}
		v.servers[server] = st
	}
	return st
}

func (v *Verifier) live(ctx context.Context, server string) (map[string]string, error) {
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	maxAge := v.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	st := v.state(server)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pins != nil && now().Sub(st.at) < maxAge {
		return st.pins, nil
	}
	raws, err := v.List(ctx, server)
	if err != nil {
		st.pins = nil
		return nil, fmt.Errorf("%s: %w: %w", server, ErrVerifyFailed, err)
	}
	pins := make(map[string]string, len(raws))
	for _, raw := range raws {
		if name, pin, ok := nameAndPin(raw); ok {
			pins[name] = pin
		}
	}
	st.at, st.pins = now(), pins
	return pins, nil
}

// Guard returns a call guard over these pins that verifies against list. It
// is what mcpproxy.ToolPins needs, so mcpproxy does not import this package.
func (p Pins) Guard(list func(ctx context.Context, server string) ([]json.RawMessage, error)) func(ctx context.Context, server, tool string) error {
	v := &Verifier{Pins: p, List: list}
	return v.Check
}
