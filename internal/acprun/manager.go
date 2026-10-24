package acprun

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/discovery"
)

// Runner is a started run; *Run implements it.
type Runner interface {
	PID() int // the adapter process
	Prompt(ctx context.Context, text string, onEvent func(Event)) (string, error)
	Cancel()
	Events(after int64) []Event
	Close() error
}

// StartFunc starts a run that continues sessionID in cwd.
type StartFunc func(ctx context.Context, sessionID, cwd string) (Runner, error)

// Errors returned by Manager.
var (
	ErrBusy        = errors.New("the session is already answering a message")
	ErrTooManyRuns = errors.New("too many sessions are busy; try again when one finishes")
	ErrNotRunning  = errors.New("the session is not running in aimux")
	errManagerDone = errors.New("aimux is shutting down")
)

// ManagerOptions configures a Manager. Zero values give the defaults.
type ManagerOptions struct {
	Start StartFunc
	// MaxRuns caps live adapters; each one is a full agent process. Default 2.
	MaxRuns int
	// IdleTimeout is how long an idle run is kept before Reap closes it.
	// Default 5 minutes.
	IdleTimeout time.Duration
	Now         func() time.Time
	// StartTimeout bounds starting an adapter and loading the session.
	// Default 3 minutes (the first npx run downloads the adapter).
	StartTimeout time.Duration
	// ParentPID returns a process's parent, for telling aimux's own adapter
	// processes from the user's terminals. Default discovery.GetParentPID.
	ParentPID func(pid int) int
}

type runEntry struct {
	run         Runner             // nil while starting
	cancelStart context.CancelFunc // aborts a start in progress
	busy        bool
	lastUsed    time.Time
}

// Manager keeps at most one run per session and decides when to start, reuse
// and close them.
type Manager struct {
	opt    ManagerOptions
	mu     sync.Mutex
	runs   map[string]*runEntry
	closed bool
}

// NewManager returns a Manager.
func NewManager(opt ManagerOptions) *Manager {
	if opt.MaxRuns <= 0 {
		opt.MaxRuns = 2
	}
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 5 * time.Minute
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.StartTimeout <= 0 {
		opt.StartTimeout = 3 * time.Minute
	}
	if opt.ParentPID == nil {
		opt.ParentPID = discovery.GetParentPID
	}
	return &Manager{opt: opt, runs: map[string]*runEntry{}}
}

// Send delivers text to the session, starting a run if there is none, and
// blocks until the agent's turn ends. It refuses with ErrBusy while the
// session is still answering, and with ErrTooManyRuns when every run slot
// is busy. A failed turn closes the run so the next message starts fresh.
func (m *Manager) Send(ctx context.Context, sessionID, cwd, text string, onEvent func(Event)) (string, error) {
	e, evicted, err := m.claim(sessionID)
	if evicted != nil {
		_ = evicted.Close()
	}
	if err != nil {
		return "", err
	}
	if e.run == nil {
		sctx, cancel := context.WithTimeout(ctx, m.opt.StartTimeout)
		m.mu.Lock()
		e.cancelStart = cancel
		m.mu.Unlock()
		r, err := m.opt.Start(sctx, sessionID, cwd)
		if err == nil && sctx.Err() != nil {
			ctx = sctx // cancelled or timed out just as the start finished
		}
		cancel()
		m.mu.Lock()
		owned := m.runs[sessionID] == e // false if Close or eviction removed it meanwhile
		if err == nil && !m.closed && owned && ctx.Err() == nil {
			e.run = r
			m.mu.Unlock()
		} else {
			if owned {
				delete(m.runs, sessionID)
			}
			closed := m.closed
			m.mu.Unlock()
			if err == nil {
				// It finished starting after an interrupt or shutdown: do not
				// prompt it, and do not leave it running untracked.
				_ = r.Close()
				err = errManagerDone
				if !closed && ctx.Err() != nil {
					err = ctx.Err()
				}
			}
			return "", err
		}
	}

	stop, err := e.run.Prompt(ctx, text, onEvent)
	m.mu.Lock()
	e.busy = false
	e.lastUsed = m.opt.Now()
	if err != nil && m.runs[sessionID] == e {
		delete(m.runs, sessionID)
	}
	m.mu.Unlock()
	if err != nil {
		_ = e.run.Close()
	}
	return stop, err
}

// claim marks the session's run busy, creating an entry (and evicting the
// least recently used idle run when full) if needed.
func (m *Manager) claim(sessionID string) (*runEntry, Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, errManagerDone
	}
	if e, ok := m.runs[sessionID]; ok {
		if e.busy {
			return nil, nil, ErrBusy
		}
		e.busy = true
		return e, nil, nil
	}
	var evicted Runner
	if len(m.runs) >= m.opt.MaxRuns {
		victim := ""
		for id, e := range m.runs {
			if e.busy || e.run == nil {
				continue
			}
			if victim == "" || e.lastUsed.Before(m.runs[victim].lastUsed) {
				victim = id
			}
		}
		if victim == "" {
			return nil, nil, ErrTooManyRuns
		}
		evicted = m.runs[victim].run
		delete(m.runs, victim)
	}
	e := &runEntry{busy: true}
	m.runs[sessionID] = e
	return e, evicted, nil
}

func (m *Manager) running(sessionID string) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.runs[sessionID]
	if !ok || e.run == nil {
		return nil, ErrNotRunning
	}
	return e.run, nil
}

// Cancel stops the session's current turn, or aborts starting its adapter.
func (m *Manager) Cancel(sessionID string) error {
	m.mu.Lock()
	e, ok := m.runs[sessionID]
	if ok && e.run == nil && e.cancelStart != nil {
		cancel := e.cancelStart
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.mu.Unlock()
	r, err := m.running(sessionID)
	if err != nil {
		return err
	}
	r.Cancel()
	return nil
}

// Events returns the session's run events numbered after seq.
func (m *Manager) Events(sessionID string, after int64) ([]Event, error) {
	r, err := m.running(sessionID)
	if err != nil {
		return nil, err
	}
	return r.Events(after), nil
}

// Active lists the sessions with a live run, sorted.
func (m *Manager) Active() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.runs))
	for id := range m.runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Reap closes runs idle longer than IdleTimeout and returns how many.
func (m *Manager) Reap() int {
	now := m.opt.Now()
	var victims []Runner
	m.mu.Lock()
	for id, e := range m.runs {
		if !e.busy && e.run != nil && now.Sub(e.lastUsed) > m.opt.IdleTimeout {
			victims = append(victims, e.run)
			delete(m.runs, id)
		}
	}
	m.mu.Unlock()
	for _, r := range victims {
		_ = r.Close()
	}
	return len(victims)
}

// Close closes every run and refuses further messages.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	var all []Runner
	for id, e := range m.runs {
		if e.run != nil {
			all = append(all, e.run)
		}
		delete(m.runs, id)
	}
	m.mu.Unlock()
	for _, r := range all {
		_ = r.Close()
	}
}

// Running reports whether aimux has a run (starting or started) for the session.
func (m *Manager) Running(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.runs[sessionID]
	return ok
}

// Owns reports whether pid is one of aimux's adapter processes or runs
// below one (the adapter starts the agent CLI as a child).
func (m *Manager) Owns(pid int) bool {
	if pid <= 1 {
		return false
	}
	m.mu.Lock()
	adapters := map[int]bool{}
	for _, e := range m.runs {
		if e.run != nil {
			adapters[e.run.PID()] = true
		}
	}
	m.mu.Unlock()
	if len(adapters) == 0 {
		return false
	}
	for p, depth := pid, 0; p > 1 && depth < 10; p, depth = m.opt.ParentPID(p), depth+1 {
		if adapters[p] {
			return true
		}
	}
	return false
}
