package controller

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestPartitionByArchive_SplitsCorrectly(t *testing.T) {
	now := time.Now()
	threshold := 30 * time.Minute

	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, LastActivity: now.Add(-1 * time.Hour)},
		{PID: 2, Status: agent.StatusIdle, LastActivity: now.Add(-1 * time.Hour)},   // idle > threshold
		{PID: 3, Status: agent.StatusIdle, LastActivity: now.Add(-5 * time.Minute)},  // idle < threshold
		{PID: 4, Status: agent.StatusError, LastActivity: now.Add(-2 * time.Hour)},   // error > threshold
		{PID: 5, Status: agent.StatusWaitingPermission, LastActivity: now.Add(-1 * time.Hour)},
	}

	active, archived := PartitionByArchive(agents, threshold)

	if len(active) != 3 {
		t.Errorf("active count = %d, want 3", len(active))
	}
	if len(archived) != 2 {
		t.Errorf("archived count = %d, want 2", len(archived))
	}

	// Verify the right PIDs ended up in each list
	activePIDs := map[int]bool{}
	for _, a := range active {
		activePIDs[a.PID] = true
	}
	archivedPIDs := map[int]bool{}
	for _, a := range archived {
		archivedPIDs[a.PID] = true
	}

	// PID 1 (Active) stays active regardless of LastActivity
	if !activePIDs[1] {
		t.Error("PID 1 (Active) should be in active list")
	}
	// PID 2 (Idle, stale) should be archived
	if !archivedPIDs[2] {
		t.Error("PID 2 (Idle, stale) should be archived")
	}
	// PID 3 (Idle, recent) stays active
	if !activePIDs[3] {
		t.Error("PID 3 (Idle, recent) should be in active list")
	}
	// PID 4 (Error, stale) should be archived
	if !archivedPIDs[4] {
		t.Error("PID 4 (Error, stale) should be archived")
	}
	// PID 5 (WaitingPermission) stays active regardless of LastActivity
	if !activePIDs[5] {
		t.Error("PID 5 (WaitingPermission) should be in active list")
	}
}

func TestPartitionByArchive_ActiveNeverArchived(t *testing.T) {
	now := time.Now()
	threshold := 10 * time.Minute

	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, LastActivity: now.Add(-24 * time.Hour)},
	}

	active, archived := PartitionByArchive(agents, threshold)

	if len(active) != 1 {
		t.Errorf("active count = %d, want 1", len(active))
	}
	if len(archived) != 0 {
		t.Errorf("archived count = %d, want 0", len(archived))
	}
}

func TestPartitionByArchive_WaitingNeverArchived(t *testing.T) {
	now := time.Now()
	threshold := 10 * time.Minute

	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusWaitingPermission, LastActivity: now.Add(-24 * time.Hour)},
	}

	active, archived := PartitionByArchive(agents, threshold)

	if len(active) != 1 {
		t.Errorf("active count = %d, want 1", len(active))
	}
	if len(archived) != 0 {
		t.Errorf("archived count = %d, want 0", len(archived))
	}
}

func TestPartitionByArchive_ZeroThreshold(t *testing.T) {
	now := time.Now()

	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle, LastActivity: now.Add(-24 * time.Hour)},
		{PID: 2, Status: agent.StatusError, LastActivity: now.Add(-24 * time.Hour)},
		{PID: 3, Status: agent.StatusUnknown, LastActivity: now.Add(-24 * time.Hour)},
	}

	active, archived := PartitionByArchive(agents, 0)

	if len(active) != 3 {
		t.Errorf("active count = %d, want 3 (all agents when threshold is zero)", len(active))
	}
	if archived != nil {
		t.Errorf("archived should be nil when threshold is zero, got %d items", len(archived))
	}
}

func TestPartitionByArchive_ZeroLastActivity(t *testing.T) {
	// Agents with zero LastActivity should never be archived even if status
	// is archivable, because we cannot determine how long they have been idle.
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle},
		{PID: 2, Status: agent.StatusError},
		{PID: 3, Status: agent.StatusUnknown},
	}

	active, archived := PartitionByArchive(agents, 10*time.Minute)

	if len(active) != 3 {
		t.Errorf("active count = %d, want 3 (agents with zero LastActivity)", len(active))
	}
	if len(archived) != 0 {
		t.Errorf("archived count = %d, want 0", len(archived))
	}
}

func TestPartitionByArchive_UnknownStatusArchived(t *testing.T) {
	now := time.Now()
	threshold := 15 * time.Minute

	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusUnknown, LastActivity: now.Add(-1 * time.Hour)},
	}

	active, archived := PartitionByArchive(agents, threshold)

	if len(active) != 0 {
		t.Errorf("active count = %d, want 0", len(active))
	}
	if len(archived) != 1 {
		t.Errorf("archived count = %d, want 1", len(archived))
	}
}

func TestPartitionByArchive_EmptyInput(t *testing.T) {
	active, archived := PartitionByArchive(nil, 30*time.Minute)

	if active != nil {
		t.Errorf("active should be nil for nil input, got %d items", len(active))
	}
	if archived != nil {
		t.Errorf("archived should be nil for nil input, got %d items", len(archived))
	}
}
