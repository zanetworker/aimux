package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// Backend is one running MCP server the proxy forwards calls to.
type Backend interface {
	ListToolsRaw(ctx context.Context) ([]json.RawMessage, error)
	CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	Close() error
}

// RequestError is a backend's JSON-RPC error answer to a call (unknown
// tool, invalid params). The server is healthy, so the pool keeps it.
type RequestError struct {
	Err  error
	Code int // the JSON-RPC error code when the server answered with one; 0 otherwise
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// DefaultCallTimeout is Claude Code's default MCP_TOOL_TIMEOUT (1e8 ms in
// Claude Code 2.1.319): how long it lets a direct stdio tool call run.
const DefaultCallTimeout = 100_000_000 * time.Millisecond

// Starter starts the named backend.
type Starter func(ctx context.Context, server string) (Backend, error)

// PoolOptions configures a Pool. Zero values give the defaults.
type PoolOptions struct {
	// CallTimeout bounds each tool call. Default DefaultCallTimeout, the
	// limit Claude Code puts on a direct stdio call, so a slow tool that
	// works direct works through the proxy too.
	CallTimeout time.Duration
	// ListTimeout bounds a tools/list on a leased backend (probes, catalog
	// refreshes). Default 2 minutes.
	ListTimeout time.Duration
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
	closed   bool  // removed while starting; the starter closes it on arrival
	closeErr error // what callers waiting on that start are told
	retired  bool  // removed with calls in flight; the last release closes it
	// listStuck: a lease's tools/list outlived its caller and has not
	// returned; no other lease lists until it does or its watchdog fires.
	listStuck bool
}

// Pool starts backends on first use, shares them, and closes idle ones.
type Pool struct {
	start  Starter
	opt    PoolOptions
	mu     sync.Mutex
	slots  map[slotKey]*slot
	health map[string]*serverStats // per server; outlives its slots
	closed bool
	// calls counts each client session's calls in flight, on any server.
	calls map[string]int
}

var (
	errPoolClosed    = errors.New("MCP proxy is shutting down")
	errSessionClosed = errors.New("client session ended")
	errReconfigured  = errors.New("server reconfigured, retry")
)

// NewPool returns a Pool that starts backends with start.
func NewPool(start Starter, opt PoolOptions) *Pool {
	if opt.CallTimeout <= 0 {
		opt.CallTimeout = DefaultCallTimeout
	}
	if opt.ListTimeout <= 0 {
		opt.ListTimeout = 2 * time.Minute
	}
	if opt.StartTimeout <= 0 {
		opt.StartTimeout = 2 * time.Minute
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Pool{start: start, opt: opt, slots: map[slotKey]*slot{}, health: map[string]*serverStats{}, calls: map[string]int{}}
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
		// One start serves every caller waiting for it, so it is not tied to
		// the first caller's context; StartTimeout bounds it.
		go p.startSlot(k, s) // #nosec G118 -- on purpose: the start outlives the request that triggered it
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		p.release(s)
		return nil, ctx.Err()
	}
	if s.err != nil {
		p.release(s)
		return nil, s.err
	}
	return s, nil
}

func (p *Pool) release(s *slot) { p.releaseUse(s, true) }

// releaseUse is release; used false leaves lastUsed alone, for a lease
// whose only use was a listing that never answered.
func (p *Pool) releaseUse(s *slot, used bool) {
	p.mu.Lock()
	s.inflight--
	if used {
		s.lastUsed = p.opt.Now()
	}
	last := s.retired && s.inflight == 0
	p.mu.Unlock()
	if last {
		_ = s.backend.Close()
	}
}

// drop removes a backend that misbehaved so the next call starts a new one,
// and marks the server failed until then. msg is the first error seen.
func (p *Pool) drop(k slotKey, s *slot, msg string, reason FailureReason) {
	p.markBroken(k, s, msg, reason)
	_ = s.backend.Close()
}

// markBroken removes the slot, if it is still the live one, and marks the
// server failed; the caller closes the backend.
func (p *Pool) markBroken(k slotKey, s *slot, msg string, reason FailureReason) {
	p.mu.Lock()
	if p.slots[k] == s {
		delete(p.slots, k)
		p.stats(k.server).markFailed(msg, reason, p.opt.Now())
	}
	p.mu.Unlock()
}

// startSlot starts the backend for a new slot and records the outcome for
// everyone waiting on s.ready.
func (p *Pool) startSlot(k slotKey, s *slot) {
	ctx, cancel := context.WithTimeout(context.Background(), p.opt.StartTimeout)
	defer cancel()
	// The outcome is published at StartTimeout even if the starter ignores
	// its context; a backend it delivers after that is closed on arrival.
	type started struct {
		b   Backend
		err error
	}
	result := make(chan started, 1)
	go func() {
		b, err := p.start(ctx, k.server)
		result <- started{b, err}
	}()
	var b Backend
	var err error
	select {
	case r := <-result:
		b, err = r.b, r.err
	case <-ctx.Done():
		err = ctx.Err()
		go func() {
			if r := <-result; r.err == nil && r.b != nil {
				_ = r.b.Close()
			}
		}()
	}
	var abandoned Backend // closed after unlocking: shutdown can take seconds
	p.mu.Lock()
	s.backend, s.err = b, err
	// A start nobody waits for any more (its session closed, or the pool
	// shut down) says nothing about the server now: it must not clear a
	// newer failure or stderr tail.
	st := &serverStats{}
	if !s.closed && !p.closed {
		st = p.stats(k.server)
	}
	if err != nil {
		reason := ReasonStartFailed
		if errors.Is(err, context.DeadlineExceeded) {
			reason = ReasonTimedOut
		}
		st.markFailed(err.Error(), reason, p.opt.Now())
		st.stderrTail = stderrTailOf(err)
	} else {
		st.started, st.failed = true, false
		st.startedAt = p.opt.Now()
		st.stderrTail = nil
	}
	switch {
	case err != nil:
		if p.slots[k] == s {
			delete(p.slots, k)
		}
	case p.closed:
		s.err = errPoolClosed
		abandoned = b
	case s.closed:
		s.err = s.closeErr
		abandoned = b
	default:
		s.lastUsed = p.opt.Now()
	}
	close(s.ready)
	p.mu.Unlock()
	if abandoned != nil {
		_ = abandoned.Close()
	}
	if err != nil || s.err != nil {
		return
	}
	// A server that exits on its own is failed until the next start. drop
	// marks it only while this slot is still the live one, so a backend the
	// pool closed itself (reaped, dropped, session ended) never counts.
	if d, ok := b.(interface{ Done() <-chan struct{} }); ok {
		if done := d.Done(); done != nil {
			go func() { <-done; p.drop(k, s, "process exited", ReasonExited) }()
		}
	}
	if p.opt.OnStart != nil {
		p.opt.OnStart(k.server, b)
	}
}

// Call forwards a tool call to the server's backend, starting it if needed.
// Failures come back as error results naming the server, never as Go
// errors, so one broken backend cannot break the proxy's other tools.
func (p *Pool) Call(ctx context.Context, server, session string, req mcp.CallToolRequest) *mcp.CallToolResult {
	k := p.key(server, session)
	p.mu.Lock()
	st := p.stats(server)
	st.calls++
	st.lastCall = p.opt.Now()
	if session != "" {
		p.calls[session]++
	}
	p.mu.Unlock()
	if session != "" {
		defer func() {
			p.mu.Lock()
			if p.calls[session]--; p.calls[session] <= 0 {
				delete(p.calls, session)
			}
			p.mu.Unlock()
		}()
	}
	// No deadline of our own: the shared start is bounded by StartTimeout
	// and reports its outcome, so a retry never joins an attempt that is
	// still winding down after its deadline.
	s, err := p.acquire(ctx, k)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, errPoolClosed) && !errors.Is(err, errSessionClosed) &&
			!errors.Is(err, errReconfigured) {
			p.countFailure(server)
		}
		return mcp.NewToolResultErrorf("%s: could not start MCP server: %v", server, err)
	}
	defer p.release(s)

	cctx, cancel := context.WithTimeout(ctx, p.opt.CallTimeout)
	defer cancel()
	// A stdio write to a server that stopped reading ignores the deadline,
	// and the caller's cancel too; closing the backend when our own timeout
	// fires is what unblocks it. A call that returns in time stops the timer.
	timedOut := fmt.Sprintf("%s timed out after %v", req.Params.Name, p.opt.CallTimeout)
	watchdog := time.AfterFunc(p.opt.CallTimeout, func() { p.drop(k, s, timedOut, ReasonTimedOut) })
	res, err := s.backend.CallTool(cctx, req)
	fired := !watchdog.Stop()
	if err != nil {
		if ctx.Err() != nil {
			// The caller went away; the backend is fine and may be serving
			// other clients' calls.
			return mcp.NewToolResultErrorf("%s: %s cancelled: %v", server, req.Params.Name, ctx.Err())
		}
		var requestErr *RequestError
		if errors.As(err, &requestErr) {
			return mcp.NewToolResultErrorf("%s: %s failed: %v", server, req.Params.Name, err)
		}
		// A transport error or our timeout may leave the server wedged;
		// restart it on the next call. Tool-level failures arrive as IsError
		// results and keep the backend.
		p.countFailure(server)
		if fired || errors.Is(cctx.Err(), context.DeadlineExceeded) {
			p.drop(k, s, timedOut, ReasonTimedOut)
			return mcp.NewToolResultErrorf("%s: %s", server, timedOut)
		}
		p.drop(k, s, err.Error(), ReasonTransportError)
		return mcp.NewToolResultErrorf("%s: %s failed: %v", server, req.Params.Name, err)
	}
	if res == nil {
		return mcp.NewToolResultErrorf("%s: %s returned no result", server, req.Params.Name)
	}
	return res
}

// countFailure counts a call that failed because of the server: it could
// not start, timed out or broke the transport. A caller's cancel, a JSON-RPC
// error answer and a tool's own error result are not the server's fault.
func (p *Pool) countFailure(server string) {
	p.mu.Lock()
	p.stats(server).failures++
	p.mu.Unlock()
}

// Reap closes started shared backends with no call in flight that have been idle
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
		// A per-session backend ends with its session (CloseSession), as a
		// direct server ends with its Claude Code process.
		if k.session == "" && s.err == nil && s.inflight == 0 && now.Sub(s.lastUsed) > idle {
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

// CloseSession closes the per-session backends of one client session. A
// backend with a call still running is closed when that call returns.
// SessionBusy reports whether a call of session is in flight on any server.
func (p *Pool) SessionBusy(session string) bool {
	if session == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[session] > 0
}

func (p *Pool) CloseSession(session string) {
	if session == "" {
		return
	}
	p.closeWhere(func(k slotKey) bool { return k.session == session }, true, errSessionClosed)
}

// CloseServer closes every backend of server, shared and per-session. One
// with a call in flight is retired instead and closed when its last call
// returns; the next call starts a new backend. A call waiting on a start
// under way is told to retry.
func (p *Pool) CloseServer(server string) {
	p.RetireServer(server)()
}

// RetireServer takes every backend of server out of use at once, so the
// next call starts a new backend, and returns the function that closes the
// retired ones (one with a call in flight closes when its last call
// returns). A call waiting on a start under way is told to retry.
func (p *Pool) RetireServer(server string) (closeRetired func()) {
	victims := p.removeWhere(func(k slotKey) bool { return k.server == server }, true, errReconfigured)
	return func() {
		for _, b := range victims {
			_ = b.Close()
		}
	}
}

// Close closes every backend and rejects further calls.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.closeWhere(func(slotKey) bool { return true }, false, errPoolClosed)
}

// closeWhere closes matching backends. With waitForCalls, one that has calls
// in flight is retired instead and closed by its last release. Callers
// waiting on a matching start get reason.
func (p *Pool) closeWhere(match func(slotKey) bool, waitForCalls bool, reason error) {
	for _, b := range p.removeWhere(match, waitForCalls, reason) {
		_ = b.Close()
	}
}

// closeLater removes matching backends now, so they are no longer counted
// or reused, and closes them in the background: a server that ignores
// SIGTERM can take seconds to stop.
func (p *Pool) closeLater(match func(slotKey) bool) {
	victims := p.removeWhere(match, true, errSessionClosed)
	if len(victims) == 0 {
		return
	}
	go func() {
		for _, b := range victims {
			_ = b.Close()
		}
	}()
}

// removeWhere removes matching slots and returns the backends to close.
// With waitForCalls, one that has calls in flight is retired instead and
// closed by its last release.
func (p *Pool) removeWhere(match func(slotKey) bool, waitForCalls bool, reason error) []Backend {
	var victims []Backend
	p.mu.Lock()
	for k, s := range p.slots {
		if !match(k) {
			continue
		}
		select {
		case <-s.ready:
			switch {
			case s.err != nil:
			case waitForCalls && s.inflight > 0:
				s.retired = true
			default:
				victims = append(victims, s.backend)
			}
			delete(p.slots, k)
		default:
			// Still starting: startSlot closes it when the start finishes.
			s.closed = true
			s.closeErr = reason
			delete(p.slots, k)
		}
	}
	p.mu.Unlock()
	return victims
}

// leaseSeq numbers leases, so each lease of a per-session server runs in a
// session of its own.
var leaseSeq atomic.Uint64

// Lease starts the server through the pool as a tool call would, sharing a
// running backend, but counts no call. Closing the lease releases it; a
// per-session server's backend started for the lease is stopped then.
func (p *Pool) Lease(ctx context.Context, server string) (Backend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // starts are not tied to ctx, so never begin one after it ends
	}
	k := p.key(server, fmt.Sprintf("aimux-lease-%d", leaseSeq.Add(1)))
	s, err := p.acquire(ctx, k)
	if err != nil {
		if k.session != "" {
			// No one else uses this session: a start still under way is
			// closed when it finishes instead of waiting for the reaper.
			p.closeLater(func(sk slotKey) bool { return sk == k })
		}
		return nil, err
	}
	return &lease{p: p, k: k, s: s}, nil
}

type lease struct {
	p         *Pool
	k         slotKey
	s         *slot
	once      sync.Once
	abandoned atomic.Bool // a listing of this lease never answered in time
}

type listed struct {
	tools []json.RawMessage
	err   error
}

// errListingStuck refuses a listing while an earlier one on the same
// backend has not returned.
var errListingStuck = errors.New("an earlier tools/list to this server has not returned")

// ListToolsRaw lists the backend's tools, returning at ctx's end even if
// the backend does not: a stdio write blocks while a stuck call holds the
// transport, or when the server stopped reading. A listing abandoned that
// way is watched: until it returns, other leases get errListingStuck
// instead of queueing another write, and if it has not returned after
// ListTimeout the backend is dropped as a timed-out call would be. A lease
// whose listing was abandoned does not count as use when it closes, so
// repeated probes cannot keep a stuck backend from being reaped. A
// JSON-RPC error answer keeps the backend, as in Call; another failure
// that is not the caller's timeout or cancel marks the server broken.
func (l *lease) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
	l.p.mu.Lock()
	stuck := l.s.listStuck
	l.p.mu.Unlock()
	if stuck {
		l.abandoned.Store(true)
		return nil, fmt.Errorf("%s: %w", l.k.server, errListingStuck)
	}
	done := make(chan listed, 1)
	finished := make(chan struct{}) // closed when the listing returns, whoever waits
	go func() {
		tools, err := l.s.backend.ListToolsRaw(ctx)
		done <- listed{tools, err}
		close(finished)
	}()
	select {
	case r := <-done:
		var rpcErr *RequestError
		if r.err != nil && ctx.Err() == nil && !errors.As(r.err, &rpcErr) {
			// Broken transport: mark it now, close it off the probe's path.
			l.p.markBroken(l.k, l.s, r.err.Error(), ReasonTransportError)
			go func() { _ = l.s.backend.Close() }()
		}
		return r.tools, r.err
	case <-ctx.Done():
		l.abandoned.Store(true)
		l.p.watchListing(l.k, l.s, finished)
		return nil, ctx.Err()
	}
}

// watchListing marks s's listing stuck until done delivers, and drops the
// backend if that takes longer than ListTimeout.
func (p *Pool) watchListing(k slotKey, s *slot, done <-chan struct{}) {
	p.mu.Lock()
	s.listStuck = true
	p.mu.Unlock()
	timedOut := fmt.Sprintf("tools/list timed out after %v", p.opt.ListTimeout)
	go func() {
		timer := time.NewTimer(p.opt.ListTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			p.drop(k, s, timedOut, ReasonTimedOut) // closing it ends the blocked write
		}
		p.mu.Lock()
		s.listStuck = false
		p.mu.Unlock()
	}()
}

func (l *lease) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return l.s.backend.CallTool(ctx, req)
}

func (l *lease) Close() error {
	l.once.Do(func() {
		l.p.releaseUse(l.s, !l.abandoned.Load())
		if l.k.session != "" {
			l.p.closeLater(func(k slotKey) bool { return k == l.k })
		}
	})
	return nil
}
