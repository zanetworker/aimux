package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestNextAttend_WaitingFirst(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive},
		{PID: 2, Status: agent.StatusWaitingPermission},
		{PID: 3, Status: agent.StatusIdle},
	}
	got := NextAttend(agents, -1)
	if got != 1 {
		t.Errorf("WaitingFirst: got %d, want 1", got)
	}
}

func TestNextAttend_SkipsActive(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive},
		{PID: 2, Status: agent.StatusActive},
	}
	got := NextAttend(agents, -1)
	if got != -1 {
		t.Errorf("SkipsActive: got %d, want -1", got)
	}
}

func TestNextAttend_CyclesFromCurrent(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle},
		{PID: 2, Status: agent.StatusIdle},
		{PID: 3, Status: agent.StatusActive},
	}
	got := NextAttend(agents, 0)
	if got != 1 {
		t.Errorf("CyclesFromCurrent: got %d, want 1", got)
	}
}

func TestNextAttend_WrapsAround(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle},
		{PID: 2, Status: agent.StatusActive},
		{PID: 3, Status: agent.StatusIdle},
	}
	got := NextAttend(agents, 2)
	if got != 0 {
		t.Errorf("WrapsAround: got %d, want 0", got)
	}
}

func TestNextAttend_PriorityOrder(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle},
		{PID: 2, Status: agent.StatusError},
		{PID: 3, Status: agent.StatusWaitingPermission},
	}
	got := NextAttend(agents, -1)
	if got != 2 {
		t.Errorf("PriorityOrder: got %d, want 2 (WaitingPermission)", got)
	}
}

func TestNextAttend_Empty(t *testing.T) {
	got := NextAttend(nil, -1)
	if got != -1 {
		t.Errorf("Empty: got %d, want -1", got)
	}
}

func TestNextAttend_SingleCandidate(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive},
		{PID: 2, Status: agent.StatusError},
		{PID: 3, Status: agent.StatusActive},
	}
	// From any position, should always find index 1.
	got := NextAttend(agents, 0)
	if got != 1 {
		t.Errorf("SingleCandidate from 0: got %d, want 1", got)
	}
	got = NextAttend(agents, 1)
	if got != 1 {
		t.Errorf("SingleCandidate from 1: got %d, want 1 (wraps back)", got)
	}
}

func TestNextAttend_ErrorBeforeIdle(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle},
		{PID: 2, Status: agent.StatusError},
	}
	got := NextAttend(agents, -1)
	if got != 1 {
		t.Errorf("ErrorBeforeIdle: got %d, want 1 (Error has higher priority)", got)
	}
}

func TestAttendPriority_AllStatuses(t *testing.T) {
	tests := []struct {
		status agent.Status
		want   int
	}{
		{agent.StatusWaitingPermission, 0},
		{agent.StatusError, 1},
		{agent.StatusIdle, 2},
		{agent.StatusUnknown, 3},
		{agent.StatusActive, -1},
	}
	for _, tt := range tests {
		got := attendPriority(tt.status)
		if got != tt.want {
			t.Errorf("attendPriority(%v): got %d, want %d", tt.status, got, tt.want)
		}
	}
}
