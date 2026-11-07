package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProcFacts(t *testing.T) {
	out := []byte(`    1     0 Mon Oct  6 09:01:02 2026
  4242   1 Sun Nov  1 22:03:04 2026
garbage line
  77  4242 Tue Nov  3 01:02:03 2026
  88  notanumber Tue Nov  3 01:02:03 2026
  99  1 not a date
`)
	got := parseProcFacts(out)
	want := map[int]ProcFacts{
		1:    {PPID: 0, Start: time.Date(2026, 10, 6, 9, 1, 2, 0, time.Local).UTC()},
		4242: {PPID: 1, Start: time.Date(2026, 11, 1, 22, 3, 4, 0, time.Local).UTC()},
		77:   {PPID: 4242, Start: time.Date(2026, 11, 3, 1, 2, 3, 0, time.Local).UTC()},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d processes, want %d: %+v", len(got), len(want), got)
	}
	for pid, w := range want {
		if g, ok := got[pid]; !ok || g.PPID != w.PPID || !g.Start.Equal(w.Start) {
			t.Errorf("pid %d = %+v, want %+v", pid, g, w)
		}
	}
	if len(parseProcFacts(nil)) != 0 {
		t.Error("empty output gave processes")
	}
}

func TestSnapshotParentPIDAndStart(t *testing.T) {
	start := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
	snap := &Snapshot{Procs: map[int]ProcFacts{10: {PPID: 5, Start: start}}}
	if got := snap.ParentPID(10); got != 5 {
		t.Errorf("ParentPID(10) = %d, want 5", got)
	}
	if got, ok := snap.StartTime(10); !ok || !got.Equal(start) {
		t.Errorf("StartTime(10) = %v %v", got, ok)
	}
	if _, ok := snap.StartTime(11); ok {
		t.Error("StartTime of a process not in the snapshot reported ok")
	}

	// A process missing from the snapshot (started since) asks ps.
	saved := GetParentPID
	defer func() { GetParentPID = saved }()
	GetParentPID = func(pid int) int { return pid + 1000 }
	if got := snap.ParentPID(11); got != 1011 {
		t.Errorf("ParentPID of an unknown pid = %d, want the ps answer 1011", got)
	}
	var none *Snapshot
	if got := none.ParentPID(3); got != 1003 {
		t.Errorf("nil snapshot ParentPID = %d, want the ps answer", got)
	}
	if _, ok := none.StartTime(3); ok {
		t.Error("nil snapshot StartTime reported ok")
	}
}

func TestTakeSnapshot_HasThisProcess(t *testing.T) {
	snap := TakeSnapshot()
	f, ok := snap.Procs[os.Getpid()]
	if !ok {
		t.Fatal("snapshot has no entry for this process")
	}
	if f.PPID != os.Getppid() {
		t.Errorf("PPID = %d, want %d", f.PPID, os.Getppid())
	}
	if want := ReadProcessStartTime(os.Getpid()); !f.Start.Equal(want) {
		t.Errorf("Start = %v, want %v (ps -o lstart)", f.Start, want)
	}
}

func TestParseCwds(t *testing.T) {
	out := []byte("p123\nfcwd\nn/Users/me/repo\np456\nfcwd\nn/tmp/with space\np789\nfcwd\n")
	got := parseCwds(out)
	if got[123] != "/Users/me/repo" || got[456] != "/tmp/with space" {
		t.Errorf("parseCwds = %v", got)
	}
	if _, ok := got[789]; ok {
		t.Error("a process without an n line got a cwd")
	}
}

func TestGetProcessCwds_MatchesPerProcessLookup(t *testing.T) {
	me := os.Getpid()
	got := GetProcessCwds([]int{me, 999999})
	want, err := GetProcessCwd(me)
	if err != nil {
		t.Skipf("lsof unavailable: %v", err)
	}
	if got[me] != want {
		t.Errorf("batched cwd = %q, per-process = %q", got[me], want)
	}
	wd, _ := os.Getwd()
	if real, _ := filepath.EvalSymlinks(wd); got[me] != wd && got[me] != real {
		t.Errorf("cwd = %q, want %q", got[me], wd)
	}
	if _, ok := got[999999]; ok {
		t.Error("a missing process got a cwd")
	}
	if len(GetProcessCwds(nil)) != 0 {
		t.Error("no pids gave cwds")
	}
}
