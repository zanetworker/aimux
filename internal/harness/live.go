package harness

// Holder is a running harness process that holds a session open: its pid,
// the session it holds and the status the harness reports for it ("busy",
// "idle" or "").
type Holder struct {
	PID       int
	SessionID string
	Status    string
}

// Mode picks the rule a LiveSessions read applies. There is one source of
// holders and two rules, so the session list and the router can differ only
// where this table says (Claude Code's, discovery.LiveCache):
//
//	case                                         List            Strict
//	registry holder live (pid alive, start ok)    holder          holder
//	registry stale (pid gone or reused)           none            none
//	registry unreadable (dir, a file, a decode)   readable ones   error
//	found only by command line (claude <id>)      none            holder
//	command line names id, live registry another  the other       the other
//	aimux-owned adapter                           holder          holder
//
// Both modes return aimux's own adapter processes; callers leave them out
// (OpenSessions' exclude, the router's Manager.Owns), so an owned holder is
// never open in a terminal under either rule.
type Mode int

const (
	// List is the session list's rule (open_in_terminal, terminal input,
	// the queue, waiting): it never fails, and a registry entry it cannot
	// read counts as no holder.
	List Mode = iota
	// Strict is the router's rule before an agent writes to a session: an
	// unreadable registry is an error, since the entry aimux cannot read may
	// be the terminal holding the session, and a live process with the
	// session on its command line holds it too.
	Strict
)

// LiveSessions reports the sessions a harness has open right now: every
// live holder by mode's rule, one per process and session, so a caller can
// leave out processes of its own before keying by session (OpenSessions).
// The error is Strict's; a List read never fails, and callers read an error
// as no session open.
type LiveSessions interface {
	Holders(mode Mode) ([]Holder, error)
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
