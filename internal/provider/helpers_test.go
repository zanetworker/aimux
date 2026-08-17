package provider

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestFindProcessRoots_SingleProcess(t *testing.T) {
	origPPID := getProcessPPID
	defer func() { getProcessPPID = origPPID }()

	// PID 100 has no parent in the set.
	getProcessPPID = func(pid int) int { return 1 }

	roots := findProcessRoots([]int{100})
	if roots[100] != 100 {
		t.Errorf("root of 100 = %d, want 100", roots[100])
	}
}

func TestFindProcessRoots_ParentChild(t *testing.T) {
	origPPID := getProcessPPID
	defer func() { getProcessPPID = origPPID }()

	// PID 200's parent is PID 100 (both in set).
	// PID 100's parent is PID 1 (not in set).
	getProcessPPID = func(pid int) int {
		switch pid {
		case 200:
			return 100
		case 100:
			return 1
		}
		return 0
	}

	roots := findProcessRoots([]int{100, 200})
	if roots[100] != 100 {
		t.Errorf("root of 100 = %d, want 100", roots[100])
	}
	if roots[200] != 100 {
		t.Errorf("root of 200 = %d, want 100 (parent)", roots[200])
	}
}

func TestFindProcessRoots_TwoSessions(t *testing.T) {
	origPPID := getProcessPPID
	defer func() { getProcessPPID = origPPID }()

	// Session 1: PIDs 100 -> 101 -> 102
	// Session 2: PIDs 200 -> 201
	getProcessPPID = func(pid int) int {
		switch pid {
		case 102:
			return 101
		case 101:
			return 100
		case 100:
			return 1 // shell, not in set
		case 201:
			return 200
		case 200:
			return 2 // different shell, not in set
		}
		return 0
	}

	roots := findProcessRoots([]int{100, 101, 102, 200, 201})

	// Session 1: all root to 100
	for _, pid := range []int{100, 101, 102} {
		if roots[pid] != 100 {
			t.Errorf("root of %d = %d, want 100", pid, roots[pid])
		}
	}
	// Session 2: all root to 200
	for _, pid := range []int{200, 201} {
		if roots[pid] != 200 {
			t.Errorf("root of %d = %d, want 200", pid, roots[pid])
		}
	}
}

func TestFindProcessRoots_Empty(t *testing.T) {
	roots := findProcessRoots(nil)
	if len(roots) != 0 {
		t.Errorf("expected empty map, got %v", roots)
	}
}

func TestCodexDedup_TwoSessionsSameDir(t *testing.T) {
	origPPID := getProcessPPID
	defer func() { getProcessPPID = origPPID }()

	// Session 1: PID 100, Session 2: PID 200. No parent relationship.
	getProcessPPID = func(pid int) int {
		switch pid {
		case 100:
			return 1
		case 200:
			return 2
		}
		return 0
	}

	c := &Codex{}
	agents := []agent.Agent{
		{PID: 100, WorkingDir: "/proj", Model: "o3"},
		{PID: 200, WorkingDir: "/proj", Model: "o3"},
	}

	result := c.dedup(agents)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (separate sessions), got %d", len(result))
	}
}

func TestKillLocalAgent_NoTmux(t *testing.T) {
	a := agent.Agent{PID: 999999}
	err := KillLocalAgent(a)
	if err == nil {
		t.Log("no error for non-existent PID (expected on some OSes)")
	}
}

func TestKillLocalAgent_WithTmuxSession(t *testing.T) {
	a := agent.Agent{PID: 999999, TMuxSession: "aimux-test-nonexistent"}
	err := KillLocalAgent(a)
	// tmux kill-session on a non-existent session is a no-op (error ignored)
	if err == nil {
		t.Log("no error for non-existent PID (expected on some OSes)")
	}
}
