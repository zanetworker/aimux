package mcpproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Session lifetime. A direct stdio server lives as long as the Claude Code
// process that started it. Through the proxy a session's per-session
// backends (browsers) follow the same rule: the proxy finds the process on
// the other end of the session's connection and ends the session when that
// process exits. Only a session whose owner cannot be found falls back to
// an idle limit, and never while one of its calls is in flight.

// sessionOwners tracks which process owns each client session.
type sessionOwners struct {
	idle   time.Duration      // fallback for sessions with no known owner
	now    func() time.Time   //
	lookup func() map[int]int // client port -> pid, for connections to the proxy
	// identity is a running process's start time; false when pid runs no
	// process. A pid with another start time is another process (pid reuse).
	identity func(pid int) (int64, bool)
	// open is the id of the connection now open on a client port; false
	// when none is. A port names a process only while the connection that
	// carried the session's request is still open: once it closes, the port
	// may be another client's.
	open func(port int) (uint64, bool)
	busy func(session string) bool // a call of the session is in flight

	mu sync.Mutex
	m  map[string]*owner
}

// endedSession is a session whose owner exited, or that idled out with no
// owner found, and the endpoint it was opened on.
type endedSession struct{ ID, Endpoint string }

type owner struct {
	endpoint string         // the server endpoint the session belongs to
	ports    map[int]uint64 // client port -> connection id, seen since the last sweep
	pid      int            // 0 until found
	start    int64          // the owner's start time (identity), with pid
	last     time.Time      // last request
}

// seen records a request of session, on endpoint, from remote (host:port)
// over connection conn.
func (o *sessionOwners) seen(session, endpoint, remote string, conn uint64) {
	if session == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m == nil {
		o.m = map[string]*owner{}
	}
	w := o.m[session]
	if w == nil {
		w = &owner{ports: map[int]uint64{}}
		o.m[session] = w
	}
	w.last = o.now()
	w.endpoint = endpoint
	if _, port, err := net.SplitHostPort(remote); err == nil {
		if p, err := strconv.Atoi(port); err == nil {
			w.ports[p] = conn
		}
	}
}

// sessions lists the tracked sessions (tests).
func (o *sessionOwners) sessions() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for s := range o.m {
		out = append(out, s)
	}
	return out
}

// sweep finds owners not known yet (one lookup for all of them) and returns
// the sessions that end now, which it stops tracking: those whose owner
// exited, and those with no known owner idle past the limit and not busy.
func (o *sessionOwners) sweep() []endedSession {
	o.mu.Lock()
	unknown := false
	for _, w := range o.m {
		if w.pid == 0 && o.liveConn(w) {
			unknown = true
			break
		}
	}
	o.mu.Unlock()

	var peers map[int]int
	if unknown {
		peers = o.lookup() // outside the lock: it runs lsof
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	var ended []endedSession
	for s, w := range o.m {
		if w.pid == 0 {
			for p, conn := range w.ports {
				// The connection must have stayed open through the lookup, so
				// lsof's answer for the port is about that same connection.
				if id, ok := o.open(p); !ok || id != conn {
					continue
				}
				if pid := peers[p]; pid != 0 {
					if st, ok := o.identity(pid); ok {
						w.pid, w.start = pid, st
					}
					break
				}
			}
			// Keep a port while its connection stays open, so a missed lookup
			// is retried next sweep; drop it once that connection closed or
			// was replaced (its port may now be another client's).
			for p, conn := range w.ports {
				if id, ok := o.open(p); !ok || id != conn {
					delete(w.ports, p)
				}
			}
		}
		switch {
		case w.pid != 0:
			if st, ok := o.identity(w.pid); ok && st == w.start {
				continue
			}
		case o.busy(s):
			w.last = now // a call in flight is activity: idle time starts when it ends
			continue
		case now.Sub(w.last) <= o.idle:
			continue
		}
		ended = append(ended, endedSession{ID: s, Endpoint: w.endpoint})
		delete(o.m, s)
	}
	return ended
}

// lsofPeers maps each client port connected to the proxy on 127.0.0.1:port
// to its process, with one lsof run.
func lsofPeers(port int) map[int]int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP@127.0.0.1:"+strconv.Itoa(port), "-sTCP:ESTABLISHED", "-Fpn").Output() // #nosec G204 -- fixed program, numeric port
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) { // lsof exits 1 when nothing matches
		return nil
	}
	return parseLsofPeers(string(out), port)
}

// parseLsofPeers reads lsof -Fpn output: a "p<pid>" line, then "n<local>-><remote>"
// lines for its sockets. A client's line is "n127.0.0.1:<client>->127.0.0.1:<port>".
func parseLsofPeers(out string, port int) map[int]int {
	peers := map[int]int{}
	suffix := "->127.0.0.1:" + strconv.Itoa(port)
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && strings.HasSuffix(line, suffix) && pid != 0:
			local := strings.TrimSuffix(line[1:], suffix)
			if _, p, err := net.SplitHostPort(local); err == nil {
				if n, err := strconv.Atoi(p); err == nil {
					peers[n] = pid
				}
			}
		}
	}
	return peers
}

// liveConn reports whether one of w's ports still has the connection that
// carried its request.
func (o *sessionOwners) liveConn(w *owner) bool {
	for p, conn := range w.ports {
		if id, ok := o.open(p); ok && id == conn {
			return true
		}
	}
	return false
}

// connTracker gives each client connection an id and knows which one is
// open on each client port (http.Server ConnState and ConnContext).
type connTracker struct {
	mu     sync.Mutex
	next   uint64
	byConn map[net.Conn]uint64
	byPort map[int]uint64
}

type connKey struct{}

func newConnTracker() *connTracker {
	return &connTracker{byConn: map[net.Conn]uint64{}, byPort: map[int]uint64{}}
}

func connPort(c net.Conn) int {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// state is the http.Server ConnState hook.
func (t *connTracker) state(c net.Conn, st http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch st {
	case http.StateNew:
		t.next++
		t.byConn[c] = t.next
		t.byPort[connPort(c)] = t.next
	case http.StateClosed, http.StateHijacked:
		if id, ok := t.byConn[c]; ok {
			delete(t.byConn, c)
			if p := connPort(c); t.byPort[p] == id {
				delete(t.byPort, p)
			}
		}
	}
}

// context is the http.Server ConnContext hook: requests carry their conn.
func (t *connTracker) context(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// idOf is the id of the connection r came on, 0 when unknown.
func (t *connTracker) idOf(r *http.Request) uint64 {
	c, ok := r.Context().Value(connKey{}).(net.Conn)
	if !ok {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byConn[c]
}

// open is the id of the connection open on port.
func (t *connTracker) open(port int) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id, ok := t.byPort[port]
	return id, ok
}
