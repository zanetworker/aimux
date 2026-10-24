package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// Backend is one running MCP server the proxy forwards calls to.
type Backend interface {
	ListToolsRaw(ctx context.Context) ([]json.RawMessage, error)
	CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	Close() error
}

// Starter starts the named backend.
type Starter func(ctx context.Context, server string) (Backend, error)

// PoolOptions configures a Pool. Zero values give the defaults.
type PoolOptions struct {
	// CallTimeout bounds each tool call. Default 2 minutes.
	CallTimeout time.Duration
	// StartTimeout bounds starting and initializing a backend, so a server
	// that never answers cannot hold its callers. Default 2 minutes (npx and
	// uvx may download the server first).
	StartTimeout time.Duration
	// PerSession names servers that get one backend per client session
	// instead of one shared backend (for example browser automation).
	PerSession map[string]bool
	// OnStart runs after a backend starts, e.g. to refresh the catalog.
	OnStart func(server string, b Backend)
	// Now is the clock, for tests.
	Now func() time.Time
}

type slotKey struct{ server, session string }

type slot struct {
	ready    chan struct{} // closed once the start attempt finished
	backend  Backend
	err      error
	inflight int
	lastUsed time.Time
	closed   bool // removed while starting; the starter closes it on arrival
}

// Pool starts backends on first use, shares them, and closes idle ones.
type Pool struct {
	start  Starter
	opt    PoolOptions
	mu     sync.Mutex
	slots  map[slotKey]*slot
	closed bool
}

var (
	errPoolClosed    = errors.New("MCP proxy is shutting down")
	errSessionClosed = errors.New("client session ended")
)

// NewPool returns a Pool that starts backends with start.
func NewPool(start Starter, opt PoolOptions) *Pool {
	if opt.CallTimeout <= 0 {
		opt.CallTimeout = 2 * time.Minute
	}
	if opt.StartTimeout <= 0 {
		opt.StartTimeout = 2 * time.Minute
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Pool{start: start, opt: opt, slots: map[slotKey]*slot{}}
}

func (p *Pool) key(server, session string) slotKey {
	if !p.opt.PerSession[server] {
		session = ""
	}
	return slotKey{server, session}
}

// acquire returns a started backend for the key and marks a call in flight.
// Concurrent first calls share one start attempt; a failed start is not
// cached, so the next call tries again.
func (p *Pool) acquire(ctx context.Context, k slotKey) (*slot, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}
	s, ok := p.slots[k]
	if !ok {
		s = &slot{ready: make(chan struct{})}
		p.slots[k] = s
	}
	s.inflight++
	p.mu.Unlock()

	if !ok {
		b, err := p.start(ctx, k.server)
		p.mu.Lock()
		s.backend, s.err = b, err
		switch {
		case err != nil:
			if p.slots[k] == s {
				delete(p.slots, k)
			}
		case p.closed:
			s.err = errPoolClosed
			_ = b.Close()
		case s.closed:
			s.err = errSessionClosed
			_ = b.Close()
		default:
			s.lastUsed = p.opt.Now()
		}
		close(s.ready)
		p.mu.Unlock()
		if err == nil && s.err == nil && p.opt.OnStart != nil {
			p.opt.OnStart(k.server, b)
		}
	} else {
		select {
		case <-s.ready:
		case <-ctx.Done():
			p.release(s)
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		p.release(s)
		return nil, s.err
	}
	return s, nil
}

func (p *Pool) release(s *slot) {
	p.mu.Lock()
	s.inflight--
	s.lastUsed = p.opt.Now()
	p.mu.Unlock()
}

// drop removes a backend that misbehaved so the next call starts a new one.
func (p *Pool) drop(k slotKey, s *slot) {
	p.mu.Lock()
	if p.slots[k] == s {
		delete(p.slots, k)
	}
	p.mu.Unlock()
	_ = s.backend.Close()
}

// Call forwards a tool call to the server's backend, starting it if needed.
// Failures come back as error results naming the server, never as Go
// errors, so one broken backend cannot break the proxy's other tools.
func (p *Pool) Call(ctx context.Context, server, session string, req mcp.CallToolRequest) *mcp.CallToolResult {
	k := p.key(server, session)
	actx, cancelAcquire := context.WithTimeout(ctx, p.opt.StartTimeout)
	s, err := p.acquire(actx, k)
	cancelAcquire()
	if err != nil {
		return mcp.NewToolResultErrorf("%s: could not start MCP server: %v", server, err)
	}
	defer p.release(s)

	cctx, cancel := context.WithTimeout(ctx, p.opt.CallTimeout)
	defer cancel()
	res, err := s.backend.CallTool(cctx, req)
	if err != nil {
		// A transport error or timeout may leave the server wedged; restart
		// it on the next call. Tool-level failures arrive as IsError results
		// and keep the backend.
		p.drop(k, s)
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return mcp.NewToolResultErrorf("%s: %s timed out after %v", server, req.Params.Name, p.opt.CallTimeout)
		}
		return mcp.NewToolResultErrorf("%s: %s failed: %v", server, req.Params.Name, err)
	}
	if res == nil {
		return mcp.NewToolResultErrorf("%s: %s returned no result", server, req.Params.Name)
	}
	return res
}

// Reap closes started backends with no call in flight that have been idle
// longer than idle, and returns how many it closed.
func (p *Pool) Reap(idle time.Duration) int {
	now := p.opt.Now()
	var victims []Backend
	p.mu.Lock()
	for k, s := range p.slots {
		select {
		case <-s.ready:
		default:
			continue // still starting
		}
		if s.err == nil && s.inflight == 0 && now.Sub(s.lastUsed) > idle {
			delete(p.slots, k)
			victims = append(victims, s.backend)
		}
	}
	p.mu.Unlock()
	for _, b := range victims {
		_ = b.Close()
	}
	return len(victims)
}

// CloseSession closes the per-session backends of one client session.
func (p *Pool) CloseSession(session string) {
	if session == "" {
		return
	}
	p.closeWhere(func(k slotKey) bool { return k.session == session })
}

// Close closes every backend and rejects further calls.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.closeWhere(func(slotKey) bool { return true })
}

func (p *Pool) closeWhere(match func(slotKey) bool) {
	var victims []Backend
	p.mu.Lock()
	for k, s := range p.slots {
		if !match(k) {
			continue
		}
		select {
		case <-s.ready:
			if s.err == nil {
				victims = append(victims, s.backend)
			}
			delete(p.slots, k)
		default:
			// Still starting: acquire closes it when the start finishes.
			s.closed = true
			delete(p.slots, k)
		}
	}
	p.mu.Unlock()
	for _, b := range victims {
		_ = b.Close()
	}
}
