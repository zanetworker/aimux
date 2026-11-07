package mcpproxy

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// ServerState is what a configured MCP server is doing, as the proxy sees it.
type ServerState string

const (
	StateNotStarted  ServerState = "not_started"  // never started
	StateStarting    ServerState = "starting"     // a start is in progress
	StateRunning     ServerState = "running"      // started and not reaped
	StateIdleStopped ServerState = "idle_stopped" // stopped (reaped idle or closed) after a good run
	StateFailed      ServerState = "failed"       // last start failed, or it broke, and no start since
)

// FailureReason is how a server last failed, as a fixed category. Unlike
// LastError it carries no text from the server, so it is safe to show to a
// model.
type FailureReason string

const (
	ReasonNone           FailureReason = ""
	ReasonStartFailed    FailureReason = "start_failed"    // the start returned an error
	ReasonTimedOut       FailureReason = "timed_out"       // a start or a call ran past its timeout
	ReasonExited         FailureReason = "exited"          // the process exited on its own
	ReasonTransportError FailureReason = "transport_error" // a call broke the connection
)

// stderrTailLines is how many lines of a failed start's stderr are kept.
const stderrTailLines = 5

// ServerStatus is one server's health. It never holds env values or tool
// arguments: only names, counts, times and the server's own error text.
type ServerStatus struct {
	Server    string      `json:"server"`
	State     ServerState `json:"state"`
	Instances int         `json:"instances"`     // >1 for per-session servers
	PID       int         `json:"pid,omitempty"` // 0 unless exactly one instance runs
	StartedAt time.Time   `json:"startedAt,omitzero"`
	LastCall  time.Time   `json:"lastCall,omitzero"`
	// Calls counts every tool call routed to the server. Failures counts
	// the calls that failed because of it (start failed, timed out, broken
	// transport), so Failures <= Calls: N callers waiting on one failed
	// start count N failures. A process that exits between calls marks the
	// server failed without counting a failure.
	Calls       int           `json:"calls"`
	Failures    int           `json:"failures"`
	LastError   string        `json:"lastError,omitempty"` // first line only, secrets scrubbed by the backend
	Reason      FailureReason `json:"reason,omitempty"`    // how LastError happened; set and kept with it
	LastErrorAt time.Time     `json:"lastErrorAt,omitzero"`
	StderrTail  []string      `json:"stderrTail,omitempty"` // last 5 lines of a failed start, scrubbed
}

// serverStats is the per-server history the Pool keeps under its lock. It
// lives outside the slots, so it survives a slot being reaped or dropped.
type serverStats struct {
	started    bool // some start succeeded
	failed     bool // the last start failed, or the backend broke after it
	startedAt  time.Time
	lastCall   time.Time
	calls      int
	failures   int
	lastError  string
	reason     FailureReason
	lastErrAt  time.Time
	stderrTail []string
}

// stats returns the server's stats, creating them. The caller holds p.mu.
func (p *Pool) stats(server string) *serverStats {
	st := p.health[server]
	if st == nil {
		st = &serverStats{}
		p.health[server] = st
	}
	return st
}

// markFailed records that the server is broken until its next good start.
// The caller holds p.mu.
func (st *serverStats) markFailed(msg string, reason FailureReason, at time.Time) {
	st.failed = true
	st.lastError = firstLine(msg)
	st.reason = reason
	st.lastErrAt = at
}

// Status reports every configured server, sorted by name. A server the
// pool has never seen is not_started.
func (p *Pool) Status(servers []string) []ServerStatus {
	names := append([]string(nil), servers...)
	sort.Strings(names)
	type live struct {
		instances int
		starting  bool
		backend   Backend // the running one, when there is exactly one
	}
	out := make([]ServerStatus, 0, len(names))
	lives := make([]live, 0, len(names))
	p.mu.Lock()
	byServer := map[string]*live{}
	for k, s := range p.slots {
		l := byServer[k.server]
		if l == nil {
			l = &live{}
			byServer[k.server] = l
		}
		select {
		case <-s.ready:
			if s.err == nil {
				l.instances++
				l.backend = s.backend
			}
		default:
			l.starting = true
		}
	}
	for i, name := range names {
		if i > 0 && name == names[i-1] {
			continue
		}
		st := ServerStatus{Server: name}
		h := p.health[name]
		if h != nil {
			st.StartedAt, st.LastCall = h.startedAt, h.lastCall
			st.Calls, st.Failures = h.calls, h.failures
			st.LastError, st.LastErrorAt, st.Reason = h.lastError, h.lastErrAt, h.reason
			st.StderrTail = append([]string(nil), h.stderrTail...)
		}
		var l live
		if bl := byServer[name]; bl != nil {
			l = *bl
		}
		switch {
		case l.instances > 0:
			st.State = StateRunning
		case l.starting:
			st.State = StateStarting
		case h != nil && h.failed:
			st.State = StateFailed
		case h != nil && h.started:
			st.State = StateIdleStopped
		default:
			st.State = StateNotStarted
		}
		st.Instances = l.instances
		out = append(out, st)
		lives = append(lives, l)
	}
	p.mu.Unlock()
	// Ask the backend outside the lock.
	for i, l := range lives {
		if l.instances != 1 {
			continue
		}
		if b, ok := l.backend.(interface{ PID() int }); ok {
			out[i].PID = b.PID()
		}
	}
	return out
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimRight(line, "\r")
}

// stderrTailOf returns the last lines of stderr a failed start carries.
func stderrTailOf(err error) []string {
	var t interface{ StderrTail() []string }
	if !errors.As(err, &t) {
		return nil
	}
	lines := t.StderrTail()
	if len(lines) > stderrTailLines {
		lines = lines[len(lines)-stderrTailLines:]
	}
	return append([]string(nil), lines...)
}
