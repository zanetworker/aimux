package harness

// Holder is a running harness process that holds a session open: its pid,
// the session it holds and the status the harness reports for it ("busy",
// "idle" or "").
type Holder struct {
	PID       int
	SessionID string
	Status    string
}

// LiveSessions reports the sessions a harness has open right now: every
// live holder, one per process, so a caller can leave out processes of its
// own before keying by session (OpenSessions). The error is for sources that
// can fail; Claude Code's (discovery.LiveCache) always returns nil, and
// callers read an error as no session open. It stays so a strict source
// (acprun's lookup, step 4) can share the interface.
type LiveSessions interface {
	Live() ([]Holder, error)
}

// OpenSessions keys holders by session, leaving out the processes exclude
// reports (nil: none). It is the rule that makes a session open in a
// terminal: some live holder that is not the caller's own. When two
// processes hold one session, the highest pid (the newest, as a rule) wins,
// whatever the order of hs.
func OpenSessions(hs []Holder, exclude func(pid int) bool) map[string]Holder {
	open := make(map[string]Holder, len(hs))
	for _, h := range hs {
		if exclude != nil && exclude(h.PID) {
			continue
		}
		if prev, ok := open[h.SessionID]; !ok || h.PID > prev.PID {
			open[h.SessionID] = h
		}
	}
	return open
}
