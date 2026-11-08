package discovery

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProcFacts are what discovery asks ps about a process besides ps aux:
// its parent (the subagent walk) and its start time (registry checks and
// session matching).
type ProcFacts struct {
	PPID  int
	Start time.Time // UTC, second precision as ps reports it
}

// readProcFacts runs one ps for every process; nil when it fails.
func readProcFacts() map[int]ProcFacts {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,lstart=").Output()
	if err != nil {
		return nil
	}
	return parseProcFacts(out)
}

// parseProcFacts reads "pid ppid lstart" lines (lstart in local time, days
// padded, as ReadProcessStartTime reads it); lines it cannot read are skipped.
func parseProcFacts(out []byte) map[int]ProcFacts {
	procs := map[int]ProcFacts{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[2:], " "), time.Local)
		if err != nil {
			continue
		}
		procs[pid] = ProcFacts{PPID: ppid, Start: t.UTC()}
	}
	return procs
}

// ParentPID is GetParentPID from the snapshot; a process it does not have
// (started since, or no snapshot) is asked of ps as before.
func (s *Snapshot) ParentPID(pid int) int {
	if s != nil {
		if f, ok := s.Procs[pid]; ok {
			return f.PPID
		}
	}
	return GetParentPID(pid)
}

// StartTime is the process's start time from the snapshot; false when the
// snapshot does not have it.
func (s *Snapshot) StartTime(pid int) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	f, ok := s.Procs[pid]
	return f.Start, ok
}

// GetProcessCwds is GetProcessCwd for many processes with one lsof: the
// processes it cannot read (gone, not ours) are left out, as GetProcessCwd
// would fail for them.
func GetProcessCwds(pids []int) map[int]string {
	if len(pids) == 0 {
		return map[int]string{}
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// lsof exits 1 when any pid is missing but still lists the others.
	out, _ := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-p", strings.Join(ids, ","), "-Fn").Output() // #nosec G204 -- pids are ints
	return parseCwds(out)
}

// parseCwds reads lsof -Fn output: a "p<pid>" line, then the cwd's
// "n<path>" line.
func parseCwds(out []byte) map[int]string {
	cwds := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n/") && pid > 0:
			cwds[pid] = line[1:]
		}
	}
	return cwds
}
