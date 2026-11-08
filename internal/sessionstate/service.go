package sessionstate

import "github.com/zanetworker/aimux/internal/harness"

// BusyReader reports aimux's own running turns (the remote API's Broker).
type BusyReader interface {
	Busy(id string) bool
}

// ApprovalReader reports a pending permission request of aimux's own run.
type ApprovalReader interface {
	AwaitingApproval(id string) bool
}

// Service answers Of and All from its inputs. Every input is optional (nil:
// the source says nothing) and must be cheap: memory or cached reads, since
// the TUI asks on every refresh and the remote API on every list.
type Service struct {
	// Holders is the sessions a live process that is not aimux's own holds,
	// one holder each (catalog.Open, harness.OpenSessions).
	Holders func() map[string]harness.Holder
	// Busy and Approvals are aimux's own runs; nil in processes that run
	// none (TUI, dashboard).
	Busy      BusyReader
	Approvals ApprovalReader
	// Markers is the live permission markers by session (waiting.Tracker).
	Markers func() map[string]Wait
	// Fallback is each harness's transcript guess, for sessions whose
	// harness has no registry status.
	Fallback func() map[string]Activity
}

// Snapshot is one read of the map-shaped inputs; Of on it asks only the
// per-session readers (Busy, Approvals), which are memory lookups.
type Snapshot struct {
	s        *Service
	holders  map[string]harness.Holder
	markers  map[string]Wait
	fallback map[string]Activity
}

// Read takes a Snapshot: each map input is read once.
func (s *Service) Read() Snapshot {
	snap := Snapshot{s: s}
	if s.Holders != nil {
		snap.holders = s.Holders()
	}
	if s.Markers != nil {
		snap.markers = s.Markers()
	}
	if s.Fallback != nil {
		snap.fallback = s.Fallback()
	}
	return snap
}

// Facts is everything the inputs say about id.
func (n Snapshot) Facts(id string) Facts {
	h, held := n.holders[id]
	f := Facts{Held: held, RegistryStatus: h.Status, Fallback: n.fallback[id]}
	if w, ok := n.markers[id]; ok {
		f.Waiting = &w
	}
	if n.s.Busy != nil {
		f.Background = n.s.Busy.Busy(id)
	}
	if n.s.Approvals != nil {
		f.Approval = n.s.Approvals.AwaitingApproval(id)
	}
	return f
}

// Of is id's State in this snapshot (Decide over Facts).
func (n Snapshot) Of(id string) State { return Decide(n.Facts(id)) }

// All is the State of every session a map input knows (held, marked or
// guessed). A session only aimux's run knows is answered by Of, not
// listed: the Busy reader cannot be enumerated.
func (n Snapshot) All() map[string]State {
	out := map[string]State{}
	for _, m := range []map[string]struct{}{keys(n.holders), keys(n.markers), keys(n.fallback)} {
		for id := range m {
			if _, done := out[id]; !done {
				out[id] = n.Of(id)
			}
		}
	}
	return out
}

func keys[V any](m map[string]V) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// Of is id's State now; sessions no input knows are gone.
func (s *Service) Of(id string) State { return s.Read().Of(id) }

// All is Read().All().
func (s *Service) All() map[string]State { return s.Read().All() }

// Holders is this snapshot's holders read, in a map of the caller's own:
// the remote list derives state and working from it, so they cannot
// disagree with the activity.
func (n Snapshot) Holders() map[string]harness.Holder {
	out := make(map[string]harness.Holder, len(n.holders))
	for id, h := range n.holders {
		out[id] = h
	}
	return out
}
