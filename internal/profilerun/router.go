package profilerun

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/zanetworker/aimux/internal/acprun"
)

// Engine runs the sessions of one kind of profile (Manifest.Runs).
type Engine interface {
	New(ctx context.Context, m Meta, page string) (session string, err error)
	Send(ctx context.Context, session, text string, onEvent func(acprun.Event)) (string, error)
	Cancel(session string) error
}

// Router is the remote API broker's Send, Cancel and Check. A session with
// Meta in Store goes to the engine for its profile's Manifest.Runs; a session
// without Meta goes to the fallbacks (acprun's router and manager), exactly
// as before profiles. A profile session never reaches the fallbacks: with no
// engine for it, or a store that cannot be read, it is an error.
type Router struct {
	Store   *Store
	Engines map[string]Engine // by Manifest.Runs: "acp", "sandbox"
	// Fallback, FallbackCheck and FallbackCancel serve sessions without Meta.
	Fallback       func(ctx context.Context, session, text string, onEvent func(acprun.Event)) (string, error)
	FallbackCheck  func(session string) error
	FallbackCancel func(session string) error
	// Runs resolves a profile name to its Manifest.Runs.
	Runs func(profile string) (string, error)
	// Log gets one line per distinct store failure; default slog.Default().
	Log *slog.Logger

	mu        sync.Mutex
	lastStore string // the store failure last logged
}

// Health reports whether the profile store can be read. While it cannot,
// every session (with or without a profile) fails closed: routing a profile
// session to the host path would be worse.
func (r *Router) Health() error {
	if r.Store == nil {
		return nil
	}
	_, _, err := r.Store.lookup("")
	return r.storeError(err)
}

// storeError turns a store read failure into an actionable error and logs
// it once per distinct failure; nil stays nil and clears the memory.
func (r *Router) storeError(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.lastStore = ""
		return nil
	}
	e := fmt.Errorf("profile sessions file %s cannot be read, so no session can be routed; move it aside (for example to %s.corrupt) to recover, which forgets which sessions ran under a profile: %w",
		r.Store.Path, r.Store.Path, err)
	if msg := e.Error(); msg != r.lastStore {
		r.lastStore = msg
		log := r.Log
		if log == nil {
			log = slog.Default()
		}
		log.Error("profile sessions: store unreadable", "path", r.Store.Path, "err", err)
	}
	return e
}

// Send continues session with text.
func (r *Router) Send(ctx context.Context, session, text string, onEvent func(acprun.Event)) (string, error) {
	e, profiled, err := r.engine(session)
	if err != nil {
		return "", err
	}
	if !profiled {
		return r.Fallback(ctx, session, text, onEvent)
	}
	return e.Send(ctx, session, text, onEvent)
}

// Cancel stops session's turn.
func (r *Router) Cancel(session string) error {
	e, profiled, err := r.engine(session)
	if err != nil {
		return err
	}
	if !profiled {
		return r.FallbackCancel(session)
	}
	return e.Cancel(session)
}

// Check reports whether Send would accept session, without sending.
func (r *Router) Check(session string) error {
	e, profiled, err := r.engine(session)
	if err != nil {
		return err
	}
	if !profiled {
		return r.FallbackCheck(session)
	}
	if c, ok := e.(Checker); ok {
		return c.Check(session)
	}
	return nil
}

// Checker is an Engine that can tell, before a turn, whether it can serve a
// session (the sandbox engine after a restart).
type Checker interface {
	Check(session string) error
}

// engine finds session's engine; profiled is false for a session without Meta.
func (r *Router) engine(session string) (e Engine, profiled bool, err error) {
	if r.Store == nil {
		return nil, false, nil
	}
	m, ok, err := r.Store.lookup(session)
	if err := r.storeError(err); err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if r.Runs == nil {
		return nil, true, fmt.Errorf("session %s: profile %q cannot be resolved", session, m.Profile)
	}
	runs, err := r.Runs(m.Profile)
	if err != nil {
		return nil, true, fmt.Errorf("session %s: profile %q: %w", session, m.Profile, err)
	}
	e = r.Engines[runs]
	if e == nil {
		return nil, true, fmt.Errorf("session %s: no engine for profile %q (runs %s)", session, m.Profile, runs)
	}
	return e, true, nil
}

// modelKey carries a turn's model choice in its context.
type modelKey struct{}

// WithModel returns ctx carrying the turn's model choice (a harmless input
// a client may set per turn); "" keeps the profile's default.
func WithModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, modelKey{}, model)
}

// ModelFrom is the turn's model choice in ctx, "" when none.
func ModelFrom(ctx context.Context) string {
	m, _ := ctx.Value(modelKey{}).(string)
	return m
}
