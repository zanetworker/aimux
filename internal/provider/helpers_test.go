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

func TestGeminiDedup_SameSession(t *testing.T) {
	origPPID := getProcessPPID
	defer func() { getProcessPPID = origPPID }()

	// All three node processes share PID 100 as ancestor.
	getProcessPPID = func(pid int) int {
		switch pid {
		case 101, 102:
			return 100
		case 100:
			return 1
		}
		return 0
	}

	g := &Gemini{}
	agents := []agent.Agent{
		{PID: 100, WorkingDir: "/proj", MemoryMB: 50},
		{PID: 101, WorkingDir: "/proj", MemoryMB: 200},
		{PID: 102, WorkingDir: "/proj", MemoryMB: 100},
	}

	result := g.dedup(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0].GroupCount != 3 {
		t.Errorf("GroupCount = %d, want 3", result[0].GroupCount)
	}
	// Should keep the process with most memory (PID 101)
	if result[0].PID != 101 {
		t.Errorf("PID = %d, want 101 (highest memory)", result[0].PID)
	}
}

func TestGeminiDedup_TwoSessionsSameDir(t *testing.T) {
	origPPID := getProcessPPID
	defer func() { getProcessPPID = origPPID }()

	// Session 1: PID 100 -> 101
	// Session 2: PID 200 -> 201
	// Both in the same WorkingDir — should remain separate.
	getProcessPPID = func(pid int) int {
		switch pid {
		case 101:
			return 100
		case 100:
			return 1
		case 201:
			return 200
		case 200:
			return 2
		}
		return 0
	}

	g := &Gemini{}
	agents := []agent.Agent{
		{PID: 100, WorkingDir: "/proj", MemoryMB: 50},
		{PID: 101, WorkingDir: "/proj", MemoryMB: 100},
		{PID: 200, WorkingDir: "/proj", MemoryMB: 60},
		{PID: 201, WorkingDir: "/proj", MemoryMB: 120},
	}

	result := g.dedup(agents)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (separate sessions), got %d", len(result))
	}
	if result[0].GroupCount != 2 || result[1].GroupCount != 2 {
		t.Errorf("GroupCounts = (%d, %d), want (2, 2)",
			result[0].GroupCount, result[1].GroupCount)
	}
}

func TestGeminiDedup_SingleAgent(t *testing.T) {
	g := &Gemini{}
	agents := []agent.Agent{
		{PID: 100, WorkingDir: "/proj"},
	}
	result := g.dedup(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
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
