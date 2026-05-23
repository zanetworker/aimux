package controller

import (
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

// PartitionByArchive splits agents into active and archived based on idle
// duration. Only Idle, Error, and Unknown agents can be archived. Active and
// WaitingPermission agents are never archived. A zero threshold disables
// archiving (all agents returned in the active list).
func PartitionByArchive(agents []agent.Agent, threshold time.Duration) (active, archived []agent.Agent) {
	if threshold <= 0 {
		return agents, nil
	}

	now := time.Now()

	for _, ag := range agents {
		if shouldArchive(ag, threshold, now) {
			archived = append(archived, ag)
		} else {
			active = append(active, ag)
		}
	}
	return active, archived
}

// shouldArchive returns true if the agent is in an archivable status and has
// been idle for longer than the threshold.
func shouldArchive(ag agent.Agent, threshold time.Duration, now time.Time) bool {
	switch ag.Status {
	case agent.StatusActive, agent.StatusWaitingPermission:
		return false
	case agent.StatusIdle, agent.StatusError, agent.StatusUnknown:
		// Only archive if we have a LastActivity timestamp and it's old enough.
		if ag.LastActivity.IsZero() {
			return false
		}
		return now.Sub(ag.LastActivity) > threshold
	default:
		return false
	}
}
