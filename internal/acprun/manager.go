package acprun

import (
	"context"
	"errors"
	"fmt"
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

// StartNewFunc starts a run for a new session in cwd and returns its id.
type StartNewFunc func(ctx context.Context, cwd string) (Runner, string, error)

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
	// Fingerprint identifies the session's on-disk state (for example the
	// session file's size and modification time). When it differs from the
	// one recorded after aimux's last turn, the session was continued
	// elsewhere and the cached adapter is replaced so the session reloads.
	// Nil or "" disables the check.
	Fingerprint func(sessionID string) string
	// Reserve claims the session for this process before an adapter starts
	// and returns its release, called when the adapter is gone for good. It
	// keeps separate aimux processes from driving one session (SessionLock).
	// Nil disables it.
	Reserve func(sessionID string) (release func(), err error)
	// StartNew starts a new session (New). Nil: new sessions are not offered.
	StartNew StartNewFunc
	// StartTimeout bounds starting an adapter and loading the session.
	// Default 3 minutes (the first npx run downloads the adapter).
	StartTimeout time.Duration
	// ParentPID returns a process's parent, for telling aimux's own adapter
	// processes from the user's terminals. Default discovery.GetParentPID.
	ParentPID func(pid int) int
}

type runEntry struct {
	retired         sync.Once
	release         func()             // gives up the cross-process reservation
	prompting       bool               // a prompt is with the agent: cancel goes to the agent
	cancelRequested bool               // cancelled after the turn was accepted, before its prompt
	fingerprint     string             // session state after aimux's last turn
	run             Runner             // nil while starting; written under m.mu and runMu
	runMu           sync.Mutex         // lets retire, which runs without m.mu, read run
	cancelStart     context.CancelFunc // aborts a start in progress
	busy            bool
	firstPending    bool // a new session waiting for its first prompt: its run is its only state
	lastUsed        time.Time
}

// retire ends an entry that has left the map: its adapter and reservation.
// Close and a failing Send can both retire one entry at shutdown; only the
// first does anything.
func (e *runEntry) retire() {
	e.retired.Do(func() {
		if r := e.getRun(); r != nil {
			_ = r.Close()
		}
		if e.release != nil {
			e.release()
		}
	})
}

func (e *runEntry) getRun() Runner {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.run
}

func (e *runEntry) setRun(r Runner) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.run = r
}

// Manager keeps at most one run per session and decides when to start, reuse
// and close them.
type Manager struct {
	opt      ManagerOptions
	mu       sync.Mutex
	runs     map[string]*runEntry
	closed   bool
	starting sync.WaitGroup               // adapter starts in progress; Close waits for them
	newStart map[*context.CancelFunc]bool // new-session starts in progress; Close cancels them
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
		evicted.retire()
	}
	if err != nil {
		return "", err
	}
	if e.run != nil && m.opt.Fingerprint != nil {
		if fp := m.opt.Fingerprint(sessionID); fp != "" && fp != e.fingerprint {
			m.mu.Lock()
			if m.closed || m.runs[sessionID] != e {
				m.mu.Unlock()
				return "", errManagerDone // Close took the entry and retires it
			}
			stale := e.run
			e.setRun(nil) // start a fresh adapter below, which reloads the session
			m.mu.Unlock()
			_ = stale.Close()
		}
	}
	if e.run == nil {
		if e.release == nil && m.opt.Reserve != nil {
			release, err := m.opt.Reserve(sessionID)
			if err != nil {
				m.mu.Lock()
				if m.runs[sessionID] == e {
					delete(m.runs, sessionID)
				}
				m.mu.Unlock()
				return "", err
			}
			e.release = release
		}
		sctx, cancel := context.WithTimeout(ctx, m.opt.StartTimeout)
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			cancel()
			e.retire()
			return "", errManagerDone
		}
		e.cancelStart = cancel
		m.starting.Add(1)
		m.mu.Unlock()
		defer m.starting.Done() // after the late runner, if any, is closed below
		r, err := m.opt.Start(sctx, sessionID, cwd)
		if err == nil && sctx.Err() != nil {
			ctx = sctx // cancelled or timed out just as the start finished
		}
		cancel()
		m.mu.Lock()
		e.cancelStart = nil             // spent: a later cancel during a reload must not find it
		owned := m.runs[sessionID] == e // false if Close or eviction removed it meanwhile
		if err == nil && !m.closed && owned && ctx.Err() == nil {
			e.setRun(r)
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
				e.setRun(r)
				err = errManagerDone
				if !closed && ctx.Err() != nil {
					err = ctx.Err()
				}
			}
			e.retire()
			return "", err
		}
	}

	m.mu.Lock()
	if ctx.Err() != nil || e.cancelRequested {
		// Cancelled after the turn was accepted but before its prompt: keep
		// the healthy adapter, send nothing.
		e.busy = false
		e.cancelRequested = false
		e.lastUsed = m.opt.Now()
		m.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", context.Canceled
	}
	e.prompting = true
	e.firstPending = false // the first prompt is going out; from now on the session is on disk
	m.mu.Unlock()
	// The prompt itself is not cut off by ctx: stopping a turn mid-prompt is
	// the agent's job (Cancel), so its session state stays consistent.
	stop, err := e.run.Prompt(context.WithoutCancel(ctx), text, onEvent)
	fp := ""
	if err == nil && m.opt.Fingerprint != nil {
		fp = m.opt.Fingerprint(sessionID)
	}
	m.mu.Lock()
	e.busy = false
	e.prompting = false
	e.lastUsed = m.opt.Now()
	e.fingerprint = fp
	if err != nil && m.runs[sessionID] == e {
		delete(m.runs, sessionID)
	}
	m.mu.Unlock()
	if err != nil {
		e.retire()
	}
	return stop, err
}

// New starts a new session in cwd and keeps its run for the session's
// messages, which go through Send as usual. The run counts against MaxRuns.
func (m *Manager) New(ctx context.Context, cwd string) (string, error) {
	if m.opt.StartNew == nil {
		return "", errors.New("starting new sessions is not supported")
	}
	sctx, cancel := context.WithTimeout(ctx, m.opt.StartTimeout)
	defer cancel()
	// Like a load in Send: Close cancels this start and waits for it.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", errManagerDone
	}
	// Take a slot before launching anything: evict an idle run if all are
	// in use, or fail now when every run (or new session starting) is busy.
	var evicted *runEntry
	if len(m.runs)+len(m.newStart) >= m.opt.MaxRuns {
		victim := m.idleVictimLocked()
		if victim == "" {
			m.mu.Unlock()
			return "", ErrTooManyRuns
		}
		evicted = m.runs[victim]
		delete(m.runs, victim)
	}
	if m.newStart == nil {
		m.newStart = map[*context.CancelFunc]bool{}
	}
	m.newStart[&cancel] = true
	m.starting.Add(1)
	m.mu.Unlock()
	defer m.starting.Done()
	if evicted != nil {
		evicted.retire()
	}
	r, id, err := m.opt.StartNew(sctx, cwd)
	if err == nil && sctx.Err() != nil {
		_ = r.Close() // ready just as the caller gave up or the start timed out
		err = sctx.Err()
	}
	if err != nil {
		m.dropNewStart(&cancel)
		return "", err
	}
	var release func()
	if m.opt.Reserve != nil {
		if release, err = m.opt.Reserve(id); err != nil {
			m.dropNewStart(&cancel)
			_ = r.Close()
			return "", err
		}
	}
	// Turn the reserved slot into the session's run in one step, so no
	// other request can take the slot in between.
	m.mu.Lock()
	delete(m.newStart, &cancel)
	closed, taken := m.closed, m.runs[id] != nil
	if !closed && !taken {
		e := &runEntry{release: release, lastUsed: m.opt.Now(), firstPending: true}
		e.setRun(r)
		m.runs[id] = e
	}
	m.mu.Unlock()
	if closed || taken {
		_ = r.Close() // it finished starting after shutdown began, or the id is in use
		if release != nil {
			release()
		}
		if closed {
			return "", errManagerDone
		}
		return "", fmt.Errorf("new session %s: %w", id, ErrBusy)
	}
	return id, nil
}

// dropNewStart gives up a new session's reserved slot.
func (m *Manager) dropNewStart(cancel *context.CancelFunc) {
	m.mu.Lock()
	delete(m.newStart, cancel)
	m.mu.Unlock()
}

// idleVictimLocked picks the least recently used idle run to make room, or
// "" if every run is busy or starting.
func (m *Manager) idleVictimLocked() string {
	victim := ""
	for id, e := range m.runs {
		if e.busy || e.run == nil || e.firstPending {
			continue
		}
		if victim == "" || e.lastUsed.Before(m.runs[victim].lastUsed) {
			victim = id
		}
	}
	return victim
}

// claim marks the session's run busy, creating an entry (and evicting the
// least recently used idle run when full) if needed.
func (m *Manager) claim(sessionID string) (*runEntry, *runEntry, error) {
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
		e.cancelRequested = false
		return e, nil, nil
	}
	var evicted *runEntry
	// New sessions still starting hold their slots too.
	if len(m.runs)+len(m.newStart) >= m.opt.MaxRuns {
		victim := m.idleVictimLocked()
		if victim == "" {
			return nil, nil, ErrTooManyRuns
		}
		evicted = m.runs[victim]
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

// Cancel stops the session's current turn wherever it is: starting the
// adapter, between accepting the turn and prompting, or in the prompt (then
// the agent is asked to stop). With no turn it returns ErrNotRunning and the
// adapter is left alone, since a cancel it kept would stop the next turn.
func (m *Manager) Cancel(sessionID string) error {
	m.mu.Lock()
	e, ok := m.runs[sessionID]
	switch {
	case !ok || !e.busy:
		m.mu.Unlock()
		return ErrNotRunning
	case e.run == nil && e.cancelStart != nil:
		cancel := e.cancelStart
		m.mu.Unlock()
		cancel()
	case e.prompting:
		r := e.run
		m.mu.Unlock()
		r.Cancel()
	default:
		e.cancelRequested = true
		m.mu.Unlock()
	}
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
	var victims []*runEntry
	m.mu.Lock()
	for id, e := range m.runs {
		if !e.busy && e.run != nil && now.Sub(e.lastUsed) > m.opt.IdleTimeout {
			victims = append(victims, e)
			delete(m.runs, id)
		}
	}
	m.mu.Unlock()
	for _, e := range victims {
		e.retire()
	}
	return len(victims)
}

// Close closes every run, cancels adapters still starting and waits for
// them, and refuses further messages.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	var all []*runEntry
	for id, e := range m.runs {
		if e.run != nil {
			all = append(all, e)
		} else if e.cancelStart != nil {
			e.cancelStart() // its Send retires it once the start returns
		}
		delete(m.runs, id)
	}
	for cancel := range m.newStart {
		(*cancel)() // its New closes the adapter once the start returns
	}
	m.mu.Unlock()
	for _, e := range all {
		e.retire()
	}
	m.starting.Wait()
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

// ProbeReserved reports ErrInUseElsewhere when another process holds the
// session's reservation. It holds nothing; sessions this manager already
// runs are not probed (it holds their reservation itself).
func (m *Manager) ProbeReserved(sessionID string) error {
	if m.opt.Reserve == nil || m.Running(sessionID) {
		return nil
	}
	release, err := m.opt.Reserve(sessionID)
	if err != nil {
		return err
	}
	release()
	return nil
}
