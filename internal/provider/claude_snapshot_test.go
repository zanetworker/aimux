package provider

import (
	"os"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/discovery"
)

// With a snapshot, discovery takes start times and parents from it and
// runs no per-process ps.
func TestClaudeDiscover_SnapshotFactsInsteadOfPs(t *testing.T) {
	savedStart, savedParent := getProcessStartTime, discovery.GetParentPID
	defer func() { getProcessStartTime, discovery.GetParentPID = savedStart, savedParent }()
	getProcessStartTime = func(pid int) time.Time {
		t.Errorf("ps asked for the start time of %d", pid)
		return time.Time{}
	}
	discovery.GetParentPID = func(pid int) int {
		t.Errorf("ps asked for the parent of %d", pid)
		return 0
	}

	me := os.Getpid()
	start := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	// 4000 is a subagent of me; 5000 is unrelated (its parent is 1).
	snap := &discovery.Snapshot{Procs: map[int]discovery.ProcFacts{
		me:   {PPID: 1, Start: start},
		4000: {PPID: me, Start: start.Add(time.Minute)},
		5000: {PPID: 1, Start: start.Add(2 * time.Minute)},
	}}
	c := &Claude{RegistryDir: t.TempDir(), ProjectsDir: t.TempDir()}
	agents, err := c.discoverFromAgents([]agent.Agent{{PID: me}, {PID: 4000}, {PID: 5000}}, nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	byPID := map[int]agent.Agent{}
	for _, a := range agents {
		byPID[a.PID] = a
	}
	if got := byPID[me].StartTime; !got.Equal(start) {
		t.Errorf("start time = %v, want the snapshot's %v", got, start)
	}
	if got := byPID[4000].ParentPID; got != me {
		t.Errorf("subagent's parent = %d, want %d", got, me)
	}
	if got := byPID[5000].ParentPID; got != 0 {
		t.Errorf("unrelated process tagged with parent %d", got)
	}
	wd, _ := os.Getwd()
	if got := byPID[me].WorkingDir; got == "" {
		t.Errorf("working dir not resolved (want about %q)", wd)
	}
}
