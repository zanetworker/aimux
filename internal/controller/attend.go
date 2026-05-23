package controller

import (
	"github.com/zanetworker/aimux/internal/agent"
)

// attendPriority returns the urgency tier for a given status.
// Lower values = higher priority. Returns -1 for statuses that
// should be skipped (Active agents don't need attention).
func attendPriority(s agent.Status) int {
	switch s {
	case agent.StatusWaitingPermission:
		return 0
	case agent.StatusError:
		return 1
	case agent.StatusIdle:
		return 2
	case agent.StatusUnknown:
		return 3
	case agent.StatusActive:
		return -1 // skip
	default:
		return -1 // skip unknown future statuses
	}
}

// NextAttend returns the index of the next agent needing attention,
// prioritized by urgency: WaitingPermission first, then Error, then
// Idle, then Unknown. Active agents are always skipped.
//
// Within the highest-priority tier, it cycles through candidates
// starting after currentIdx, wrapping around. Returns -1 if no
// agent needs attention.
func NextAttend(agents []agent.Agent, currentIdx int) int {
	if len(agents) == 0 {
		return -1
	}

	// Find the highest-priority tier (lowest number) that has candidates.
	bestTier := -1
	for _, a := range agents {
		p := attendPriority(a.Status)
		if p < 0 {
			continue // skip Active
		}
		if bestTier < 0 || p < bestTier {
			bestTier = p
		}
	}
	if bestTier < 0 {
		return -1 // all agents are Active
	}

	// Cycle through agents of that tier starting after currentIdx,
	// wrapping around.
	n := len(agents)
	for i := 1; i <= n; i++ {
		idx := (currentIdx + i) % n
		if attendPriority(agents[idx].Status) == bestTier {
			return idx
		}
	}

	return -1
}
