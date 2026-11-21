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
	// Admit, if set, must accept a profile before a new turn of its
	// session starts (Send, which queued messages also go through): a
	// profile whose roles stopped checking gets no new turn. Check and
	// Cancel never call it, so a running turn can still be stopped.
	Admit func(profile string) error
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
	e, m, profiled, err := r.engineMeta(session)
	if err != nil {
		return "", err
	}
	if !profiled {
		return r.Fallback(ctx, session, text, onEvent)
	}
	if r.Admit != nil {
		if err := r.Admit(m.Profile); err != nil {
			return "", fmt.Errorf("session %s: %w", session, err)
		}
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
	e, _, profiled, err = r.engineMeta(session)
	return e, profiled, err
}

// engineMeta is engine with the Meta it routed by, so admission checks the
// same profile the turn is routed with (one store read).
func (r *Router) engineMeta(session string) (e Engine, m Meta, profiled bool, err error) {
	if r.Store == nil {
		return nil, Meta{}, false, nil
	}
	m, ok, err := r.Store.lookup(session)
	if err := r.storeError(err); err != nil {
		return nil, Meta{}, false, err
	}
	if !ok {
		return nil, Meta{}, false, nil
	}
	if r.Runs == nil {
		return nil, m, true, fmt.Errorf("session %s: profile %q cannot be resolved", session, m.Profile)
	}
	runs, err := r.Runs(m.Profile)
	if err != nil {
		return nil, m, true, fmt.Errorf("session %s: profile %q: %w", session, m.Profile, err)
	}
	e = r.Engines[runs]
	if e == nil {
		return nil, m, true, fmt.Errorf("session %s: no engine for profile %q (runs %s)", session, m.Profile, runs)
	}
	return e, m, true, nil
}

// modelKey carries a turn's model choice in its context.
type modelKey struct{}

// WithModel returns ctx carrying the turn's model choice (a harmless input
// a client may set per turn); "" keeps the profile's default.
func WithModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, modelKey{}, model)
}

// priorKey carries a turn's earlier questions in its context.
type priorKey struct{}

type earlierKey struct{}

// Exchange is one earlier question on the page and the answer it got.
type Exchange struct{ Question, Answer string }

// WithEarlier carries the page's earlier questions and answers, oldest
// first, for a run profile's follow-up (its stages start fresh each turn).
func WithEarlier(ctx context.Context, earlier []Exchange) context.Context {
	return context.WithValue(ctx, earlierKey{}, earlier)
}

// EarlierFrom is the turn's earlier exchanges in ctx, nil when none.
func EarlierFrom(ctx context.Context) []Exchange {
	e, _ := ctx.Value(earlierKey{}).([]Exchange)
	return e
}

// WithPrior returns ctx carrying the user's earlier questions on the page
// (asked in another mode, oldest first) for the turn's prompt.
func WithPrior(ctx context.Context, prior []string) context.Context {
	return context.WithValue(ctx, priorKey{}, prior)
}

// PriorFrom is the turn's earlier questions in ctx, nil when none.
func PriorFrom(ctx context.Context) []string {
	p, _ := ctx.Value(priorKey{}).([]string)
	return p
}

// ModelFrom is the turn's model choice in ctx, "" when none.
func ModelFrom(ctx context.Context) string {
	m, _ := ctx.Value(modelKey{}).(string)
	return m
}

// noWebKey marks a turn that must not reach the web.
type noWebKey struct{}

// WithNoWeb returns ctx for a turn that must not reach the web (a run's
// planner stage): the sandbox engine refuses it when its profile compiles
// with web, whatever the profile was when the run was admitted.
func WithNoWeb(ctx context.Context) context.Context {
	return context.WithValue(ctx, noWebKey{}, true)
}

// NoWebFrom reports whether ctx forbids the web.
func NoWebFrom(ctx context.Context) bool {
	v, _ := ctx.Value(noWebKey{}).(bool)
	return v
}

// callerKey carries the client that asked for a turn.
type callerKey struct{}

// WithCaller returns ctx carrying the authenticated client that asked for
// the turn, which may differ from the session's creator (Meta.Client).
func WithCaller(ctx context.Context, client string) context.Context {
	return context.WithValue(ctx, callerKey{}, client)
}

// CallerFrom is the client that asked for the turn, "" when not known (a
// queued message).
func CallerFrom(ctx context.Context) string {
	c, _ := ctx.Value(callerKey{}).(string)
	return c
}

// imageKey carries a turn's attached image in its context.
type imageKey struct{}

// WithImage returns ctx carrying the image the user attached to the turn's
// question (checked by the remote API), sent after the prompt text.
func WithImage(ctx context.Context, img *acprun.Image) context.Context {
	return context.WithValue(ctx, imageKey{}, img)
}

// ImageFrom is the turn's attached image in ctx, nil when none.
func ImageFrom(ctx context.Context) *acprun.Image {
	img, _ := ctx.Value(imageKey{}).(*acprun.Image)
	return img
}
