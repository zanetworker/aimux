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
	"github.com/zanetworker/aimux/internal/harness/claude"
)

// LiveTTL is how long LiveCache serves one read of the registry.
const LiveTTL = time.Second

// LiveCache is Claude Code's harness.LiveSessions, both modes over one
// read (the table on harness.Mode): the registry entries (readClaudeRegistry)
// whose process is still the one that wrote them, by IsLiveHolder's rule,
// and for Strict also the live claude processes that name a session on
// their command line (claude.CommandLineSessions) and have no live entry of
// their own. A List read serves every List caller for TTL, concurrent
// callers during a read wait for that read, and a read checks every start
// time with one ps run instead of one per entry. A Strict read is never
// served from the cache: it takes only a read that began after the call (a
// running one if it started later, else one of its own), since a send must
// see a terminal opened a moment ago or two processes write one session.
// Strict callers arriving together still share one read. The command lines
// (one `ps -axo`) are read only by a Strict read; a List read never reads
// them, and a Strict read serves List callers for TTL. Safe for concurrent
// use.
type LiveCache struct {
	Dir string        // ~/.claude/sessions
	TTL time.Duration // 0: read on every call
	// CommandLines lists processes as `ps -axo pid=,args=` prints them, for
	// Strict reads. Nil: run that ps. Set it to test with canned output.
	CommandLines func() ([]byte, error)

	// Seams for tests; NewLiveCache sets the real ones.
	now   func() time.Time
	read  func(dir string) (map[int]ClaudeRegistryEntry, error)
	alive func(pid int) bool
	ps    func(pids []int) ([]byte, error)

	mu       sync.Mutex
	at       time.Time // when the read of snap began
	snap     liveRead
	have     bool          // snap is from a read
	reads    int           // reads finished, so a waiter knows its read is in
	started  int           // reads begun; snap.seq numbers them
	inflight chan struct{} // closed when the running read ends
}

// liveRead is one read of the registry and, for Strict, the command lines.
type liveRead struct {
	reg     map[int]ClaudeRegistryEntry // every decodable entry, live or not (Registry)
	holders []harness.Holder            // registry holders by IsLiveHolder's rule
	regErr  error                       // the registry could not be fully read
	strict  bool                        // cmd was looked for
	cmd     []harness.Holder            // command-line holders without a live entry
	seq     int                         // which read this is (LiveCache.started when it began)
}

// servesStrict reports whether r answers a Strict call made when callSeq
// reads had begun: a Strict read that began after the call.
func (r *liveRead) servesStrict(callSeq int) bool { return r.strict && r.seq > callSeq }

var _ harness.LiveSessions = (*LiveCache)(nil)

// NewLiveCache reads the registry in dir, serving each read for ttl.
func NewLiveCache(dir string, ttl time.Duration) *LiveCache {
	return &LiveCache{Dir: dir, TTL: ttl, now: time.Now, read: readClaudeRegistry, alive: pidAlive, ps: psStartTimes}
}

// Holders returns every live holder by mode's rule, in a slice of the
// caller's own. A List read never fails; a Strict read fails when the
// registry could not be fully read.
func (c *LiveCache) Holders(mode harness.Mode) ([]harness.Holder, error) {
	strict := mode == harness.Strict
	c.mu.Lock()
	callSeq := c.started // a Strict call takes only a read begun after this
	for {
		if c.have && (strict && c.snap.servesStrict(callSeq) || !strict && c.now().Sub(c.at) < c.TTL) {
			return c.resultUnlock(mode)
		}
		if c.inflight == nil {
			break
		}
		wait, reads := c.inflight, c.reads
		c.mu.Unlock()
		<-wait
		c.mu.Lock()
		// The read waited for is in: List takes it, Strict only if it
		// began after the call.
		if c.reads != reads && (!strict || c.snap.servesStrict(callSeq)) {
			return c.resultUnlock(mode)
		}
	}
	done := make(chan struct{})
	c.started++
	seq := c.started
	c.inflight = done
	c.mu.Unlock()
	defer func() { // also on a panic, so waiters never hang
		c.mu.Lock()
		c.inflight = nil
		c.mu.Unlock()
		close(done)
	}()
	began := c.now() // a read is as old as its start: the registry may change during it
	r := c.load(strict)
	r.seq = seq
	c.mu.Lock()
	c.snap, c.at, c.have = r, began, true
	c.reads++
	return c.resultUnlock(mode)
}

// Registry is every entry of the registry read List would serve now
// (ReadClaudeRegistry's answer, from the same cached read), in a map of the
// caller's own: Claude's discovery reads it, so a refresh that also asks
// for holders reads the registry and runs ps once.
func (c *LiveCache) Registry() map[int]ClaudeRegistryEntry {
	_, _ = c.Holders(harness.List) // never fails; makes the cached read current
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int]ClaudeRegistryEntry, len(c.snap.reg))
	for pid, e := range c.snap.reg {
		out[pid] = e
	}
	return out
}

// load reads the registry and, when strict and the registry read fully, the
// command lines. A failed command-line read finds no extra holders, as the
// registry alone would.
func (c *LiveCache) load(strict bool) liveRead {
	reg, err := c.read(c.Dir)
	r := liveRead{reg: reg, holders: liveHolders(reg, c.alive, c.ps), regErr: err, strict: strict}
	if !strict || err != nil {
		return r
	}
	lines := c.CommandLines
	if lines == nil {
		lines = psCommandLines
	}
	if out, err := lines(); err == nil {
		r.cmd = commandLineHolders(out, r.holders, c.alive)
	}
	return r
}

// resultUnlock answers mode from snap and releases c.mu, which the caller
// holds.
func (c *LiveCache) resultUnlock(mode harness.Mode) ([]harness.Holder, error) {
	defer c.mu.Unlock()
	hs := append([]harness.Holder(nil), c.snap.holders...)
	if mode != harness.Strict {
		return hs, nil
	}
	if c.snap.regErr != nil {
		return nil, c.snap.regErr
	}
	return append(hs, c.snap.cmd...), nil
}

// commandLineHolders reads `ps -axo pid=,args=` output: a live claude
// process holds each session id its command line names, unless it is one
// of the registry's live holders, whose entry says which session it holds
// now (after /clear or a fork, not the one it was started with). One holder
// per process and session, sorted by pid and session.
func commandLineHolders(out []byte, registry []harness.Holder, alive func(int) bool) []harness.Holder {
	inRegistry := make(map[int]bool, len(registry))
	for _, h := range registry {
		inRegistry[h.PID] = true
	}
	type key struct {
		pid int
		id  string
	}
	seen := map[key]bool{}
	var hs []harness.Holder
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || inRegistry[pid] {
			continue
		}
		ids := claude.CommandLineSessions(strings.Join(fields[1:], " "))
		if len(ids) == 0 || !alive(pid) {
			continue
		}
		for _, id := range ids {
			if !ValidSessionID(id) || seen[key{pid, id}] {
				continue
			}
			seen[key{pid, id}] = true
			hs = append(hs, harness.Holder{PID: pid, SessionID: id})
		}
	}
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].PID != hs[j].PID {
			return hs[i].PID < hs[j].PID
		}
		return hs[i].SessionID < hs[j].SessionID
	})
	return hs
}

// psCommandLines runs one ps for every process's command line.
func psCommandLines() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ps", "-axo", "pid=,args=").Output()
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
