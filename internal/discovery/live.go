package discovery

import (
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/harness"
)

// LiveTTL is how long LiveCache serves one read of the registry.
const LiveTTL = time.Second

// LiveCache is Claude Code's harness.LiveSessions: the registry entries
// (ReadClaudeRegistry) whose process is still the one that wrote them, by
// IsLiveHolder's rule. One read serves every caller for TTL, concurrent
// callers during a read wait for that read, and a read checks every start
// time with one ps run instead of one per entry. Safe for concurrent use.
type LiveCache struct {
	Dir string        // ~/.claude/sessions
	TTL time.Duration // 0: read on every call

	// Seams for tests; NewLiveCache sets the real ones.
	now   func() time.Time
	read  func(dir string) map[int]ClaudeRegistryEntry
	alive func(pid int) bool
	ps    func(pids []int) ([]byte, error)

	mu       sync.Mutex
	at       time.Time // when the read of holders began
	holders  []harness.Holder
	have     bool          // holders is from a read
	reads    int           // reads finished, so a waiter knows its read is in
	inflight chan struct{} // closed when the running read ends
}

var _ harness.LiveSessions = (*LiveCache)(nil)

// NewLiveCache reads the registry in dir, serving each read for ttl.
func NewLiveCache(dir string, ttl time.Duration) *LiveCache {
	return &LiveCache{Dir: dir, TTL: ttl, now: time.Now, read: ReadClaudeRegistry, alive: pidAlive, ps: psStartTimes}
}

// Live returns every live holder, one per process, in a slice of the
// caller's own. The registry is optional, so it never fails.
func (c *LiveCache) Live() ([]harness.Holder, error) {
	c.mu.Lock()
	for {
		if c.have && c.now().Sub(c.at) < c.TTL {
			return c.copyUnlock(), nil
		}
		if c.inflight == nil {
			break
		}
		wait, reads := c.inflight, c.reads
		c.mu.Unlock()
		<-wait
		c.mu.Lock()
		if c.reads != reads { // the read waited for is in
			return c.copyUnlock(), nil
		}
	}
	done := make(chan struct{})
	c.inflight = done
	c.mu.Unlock()
	defer func() { // also on a panic, so waiters never hang
		c.mu.Lock()
		c.inflight = nil
		c.mu.Unlock()
		close(done)
	}()
	began := c.now() // a read is as old as its start: the registry may change during it
	hs := liveHolders(c.read(c.Dir), c.alive, c.ps)
	c.mu.Lock()
	c.holders, c.at, c.have = hs, began, true
	c.reads++
	return c.copyUnlock(), nil
}

// copyUnlock copies the holders and releases c.mu, which the caller holds.
func (c *LiveCache) copyUnlock() []harness.Holder {
	hs := append([]harness.Holder(nil), c.holders...)
	c.mu.Unlock()
	return hs
}

// liveHolders applies IsLiveHolder to every entry with one ps run: an entry
// counts when its pid is alive and, when both start times are known, the
// process started within 2 s of the recorded start. Sorted by pid.
func liveHolders(reg map[int]ClaudeRegistryEntry, alive func(int) bool, ps func([]int) ([]byte, error)) []harness.Holder {
	var live []ClaudeRegistryEntry
	var check []int
	for _, e := range reg {
		if e.PID <= 0 || !alive(e.PID) {
			continue
		}
		live = append(live, e)
		if _, ok := e.StartedAt(); ok {
			check = append(check, e.PID)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].PID < live[j].PID })
	sort.Ints(check)
	var started map[int]time.Time
	if len(check) > 0 {
		// ps exits 1 when a pid has gone; the lines it printed still count,
		// and a pid it printed nothing for has an unknown start, as in
		// IsLiveHolder.
		out, _ := ps(check)
		started = parseStartTimes(out)
	}
	hs := make([]harness.Holder, 0, len(live))
	for _, e := range live {
		if recorded, ok := e.StartedAt(); ok && !startMatches(recorded, started[e.PID]) {
			continue
		}
		hs = append(hs, harness.Holder{PID: e.PID, SessionID: e.SessionID, Status: e.Status})
	}
	return hs
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// psStartTimes runs one ps for all pids: lines of "pid lstart".
func psStartTimes(pids []int) ([]byte, error) {
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ps", "-o", "pid=,lstart=", "-p", strings.Join(ids, ",")).Output() // #nosec G204 -- pids are ints
}

// parseStartTimes reads ps -o pid=,lstart= output (local time, days padded)
// into start times in UTC by pid; lines it cannot read are skipped.
func parseStartTimes(out []byte) map[int]time.Time {
	started := map[int]time.Time{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[1:], " "), time.Local)
		if err != nil {
			continue
		}
		started[pid] = t.UTC()
	}
	return started
}
