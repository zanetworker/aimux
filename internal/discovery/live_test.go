package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/harness"
)

// fakeClock is a settable clock for the cache's TTL.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingCache is a LiveCache over a fixed registry whose reads and ps runs
// are counted; it never runs ps or reads ~/.claude.
func countingCache(reg map[int]ClaudeRegistryEntry, psOut string) (*LiveCache, *fakeClock, *atomic.Int32, *atomic.Int32) {
	clock := &fakeClock{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	var reads, pss atomic.Int32
	c := NewLiveCache(filepath.Join(os.TempDir(), "unused"), time.Second)
	c.now = clock.now
	c.read = func(string) map[int]ClaudeRegistryEntry { reads.Add(1); return reg }
	c.alive = func(int) bool { return true }
	c.ps = func([]int) ([]byte, error) { pss.Add(1); return []byte(psOut), nil }
	return c, clock, &reads, &pss
}

func TestParseStartTimes(t *testing.T) {
	out := "    1 Wed Oct  7 19:00:00 2026    \n94865 Sun Nov  1 09:18:45 2026\n\ngarbage line\nx Sun Nov  1 09:18:45 2026\n7 not a date\n"
	got := parseStartTimes([]byte(out))
	if len(got) != 2 {
		t.Fatalf("parseStartTimes = %v, want pids 1 and 94865 only (blank, garbage and bad dates skipped)", got)
	}
	want := time.Date(2026, 11, 1, 9, 18, 45, 0, time.Local).UTC()
	if !got[94865].Equal(want) {
		t.Fatalf("pid 94865 started %v, want %v (ps prints local time, padded days)", got[94865], want)
	}
	if got := parseStartTimes(nil); len(got) != 0 {
		t.Fatalf("empty output: %v", got)
	}
}

// startLine is the line ps -o pid=,lstart= prints for pid started at t.
func startLine(pid string, t time.Time) string {
	return pid + " " + t.Local().Format("Mon Jan _2 15:04:05 2006") + "\n"
}

func TestLiveHolders_SameRuleAsIsLiveHolder(t *testing.T) {
	recorded := time.Date(2026, 10, 23, 15, 40, 46, 0, time.UTC)
	proc := recorded.Format(time.ANSIC)
	reg := map[int]ClaudeRegistryEntry{
		10: {PID: 10, SessionID: "match", Status: "busy", ProcStart: proc}, // started within 2 s
		11: {PID: 11, SessionID: "reused", ProcStart: proc},                // pid reused: started hours later
		12: {PID: 12, SessionID: "gone", ProcStart: proc},                  // process vanished
		13: {PID: 13, SessionID: "nostart"},                                // no recorded start: no ps check
		14: {PID: 14, SessionID: "unlisted", ProcStart: proc},              // alive but ps printed nothing for it
	}
	alive := func(pid int) bool { return pid != 12 }
	var asked []int
	ps := func(pids []int) ([]byte, error) {
		asked = pids
		// ps exits 1 when some pid is missing; its output still counts.
		return []byte(startLine("10", recorded.Add(time.Second)) + startLine("11", recorded.Add(3*time.Hour))), errors.New("exit status 1")
	}
	got := map[string]harness.Holder{}
	for _, h := range liveHolders(reg, alive, ps) {
		got[h.SessionID] = h
	}
	for _, id := range []string{"match", "nostart", "unlisted"} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s not live; IsLiveHolder counts it", id)
		}
	}
	for _, id := range []string{"reused", "gone"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s live; IsLiveHolder does not count it", id)
		}
	}
	if h := got["match"]; h.PID != 10 || h.Status != "busy" {
		t.Errorf("match = %+v, want pid 10 busy", h)
	}
	if len(asked) != 3 || asked[0] != 10 || asked[1] != 11 || asked[2] != 14 {
		t.Errorf("ps asked for %v, want [10 11 14]: live pids with a recorded start, sorted, in one run", asked)
	}
}

func TestLiveHolders_NoPSWithoutRecordedStarts(t *testing.T) {
	reg := map[int]ClaudeRegistryEntry{5: {PID: 5, SessionID: "a"}, 0: {PID: 0, SessionID: "zero"}}
	ran := false
	hs := liveHolders(reg, func(int) bool { return true }, func([]int) ([]byte, error) { ran = true; return nil, nil })
	if ran {
		t.Error("ps ran though no entry has a recorded start")
	}
	if len(hs) != 1 || hs[0].SessionID != "a" {
		t.Errorf("holders = %+v, want only a (pid 0 is never live)", hs)
	}
}

func TestLiveHolders_PSFailureKeepsLiveHolders(t *testing.T) {
	proc := time.Date(2026, 10, 23, 15, 40, 46, 0, time.UTC).Format(time.ANSIC)
	reg := map[int]ClaudeRegistryEntry{7: {PID: 7, SessionID: "a", ProcStart: proc}}
	hs := liveHolders(reg, func(int) bool { return true }, func([]int) ([]byte, error) { return nil, errors.New("no ps") })
	if len(hs) != 1 {
		t.Fatalf("holders = %+v; an unknown start time must not hide a live holder (IsLiveHolder)", hs)
	}
}

func TestLiveCache_TTL(t *testing.T) {
	c, clock, reads, pss := countingCache(map[int]ClaudeRegistryEntry{
		9: {PID: 9, SessionID: "s", ProcStart: "Fri Oct 23 15:40:46 2026"},
	}, "")
	for i := 0; i < 3; i++ {
		hs, err := c.Live()
		if err != nil || len(hs) != 1 {
			t.Fatalf("Live() = %v, %v", hs, err)
		}
	}
	clock.add(999 * time.Millisecond)
	_, _ = c.Live()
	if reads.Load() != 1 || pss.Load() != 1 {
		t.Fatalf("within 1 s: %d registry reads, %d ps runs; want 1 and 1", reads.Load(), pss.Load())
	}
	clock.add(time.Millisecond)
	_, _ = c.Live()
	if reads.Load() != 2 || pss.Load() != 2 {
		t.Fatalf("after 1 s: %d registry reads, %d ps runs; want 2 and 2", reads.Load(), pss.Load())
	}
}

func TestLiveCache_ZeroTTLReadsEveryTime(t *testing.T) {
	c, _, reads, _ := countingCache(map[int]ClaudeRegistryEntry{}, "")
	c.TTL = 0
	_, _ = c.Live()
	_, _ = c.Live()
	if reads.Load() != 2 {
		t.Fatalf("TTL 0: %d reads, want 2", reads.Load())
	}
}

func TestLiveCache_CallersGetTheirOwnSlice(t *testing.T) {
	c, _, _, _ := countingCache(map[int]ClaudeRegistryEntry{1: {PID: 1, SessionID: "a"}}, "")
	hs, _ := c.Live()
	hs[0].SessionID = "changed"
	again, _ := c.Live()
	if again[0].SessionID != "a" {
		t.Fatal("a caller's change reached the cache")
	}
}

// TestLiveCache_SingleFlight is the measured effect: 100 consumers within
// one second cost one registry scan and one ps run, also while the scan is
// still running when they arrive.
func TestLiveCache_SingleFlight(t *testing.T) {
	c, _, reads, pss := countingCache(map[int]ClaudeRegistryEntry{
		9: {PID: 9, SessionID: "s", ProcStart: "Fri Oct 23 15:40:46 2026"},
	}, "")
	release := make(chan struct{})
	read := c.read
	c.read = func(dir string) map[int]ClaudeRegistryEntry { <-release; return read(dir) }
	var wg sync.WaitGroup
	var started sync.WaitGroup
	results := make([]int, 100)
	for i := range results {
		wg.Add(1)
		started.Add(1)
		go func(i int) {
			defer wg.Done()
			started.Done()
			hs, _ := c.Live()
			results[i] = len(hs)
		}(i)
	}
	started.Wait()
	time.Sleep(10 * time.Millisecond) // let the callers queue behind the first read
	close(release)
	wg.Wait()
	if reads.Load() != 1 || pss.Load() != 1 {
		t.Fatalf("100 concurrent consumers: %d registry scans, %d ps runs; want 1 and 1", reads.Load(), pss.Load())
	}
	for i, n := range results {
		if n != 1 {
			t.Fatalf("consumer %d saw %d holders, want 1", i, n)
		}
	}
}

func TestLiveCache_ReadsTheRegistryDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`,"sessionId":"me","status":"idle"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2.json"), []byte(`{"pid":999999999,"sessionId":"dead"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hs, err := NewLiveCache(dir, time.Second).Live()
	if err != nil || len(hs) != 1 || hs[0].SessionID != "me" || hs[0].PID != os.Getpid() || hs[0].Status != "idle" {
		t.Fatalf("Live() = %+v, %v; want only this process's session", hs, err)
	}
}

func BenchmarkLiveCache_Live(b *testing.B) {
	c, _, reads, _ := countingCache(map[int]ClaudeRegistryEntry{
		1: {PID: 1, SessionID: "a"}, 2: {PID: 2, SessionID: "b"},
	}, "")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Live()
		}
	})
	b.ReportMetric(float64(reads.Load()), "scans")
}

// TestLiveCache_StampsBeforeTheRead: a read's age counts from when it
// started, so a slow read is not served for longer than TTL after it began.
func TestLiveCache_StampsBeforeTheRead(t *testing.T) {
	c, clock, reads, _ := countingCache(map[int]ClaudeRegistryEntry{}, "")
	read := c.read
	c.read = func(dir string) map[int]ClaudeRegistryEntry { clock.add(600 * time.Millisecond); return read(dir) }
	_, _ = c.Live() // starts at 0, ends at 600 ms
	clock.add(500 * time.Millisecond)
	_, _ = c.Live() // 1.1 s after the first read began
	if reads.Load() != 2 {
		t.Fatalf("%d reads, want 2: the first read began over 1 s ago", reads.Load())
	}
}

// TestLiveCache_PanickingLeaderReleasesWaiters: when the read a caller leads
// panics, the callers waiting on it read again instead of hanging.
func TestLiveCache_PanickingLeaderReleasesWaiters(t *testing.T) {
	c, _, _, _ := countingCache(map[int]ClaudeRegistryEntry{1: {PID: 1, SessionID: "a"}}, "")
	read := c.read
	release := make(chan struct{})
	var calls atomic.Int32
	c.read = func(dir string) map[int]ClaudeRegistryEntry {
		if calls.Add(1) == 1 {
			<-release
			panic("registry read failed")
		}
		return read(dir)
	}
	leaderDone := make(chan any)
	go func() {
		defer func() { leaderDone <- recover() }()
		_, _ = c.Live()
	}()
	for calls.Load() == 0 { // the leader is inside its read
		time.Sleep(time.Millisecond)
	}
	results := make(chan int, 5)
	for i := 0; i < 5; i++ {
		go func() { hs, _ := c.Live(); results <- len(hs) }()
	}
	time.Sleep(10 * time.Millisecond) // let them wait on the leader
	close(release)
	if r := <-leaderDone; r == nil {
		t.Fatal("the leader's panic was swallowed")
	}
	for i := 0; i < 5; i++ {
		select {
		case n := <-results:
			if n != 1 {
				t.Fatalf("waiter saw %d holders, want 1 from its own read", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a waiter hung after the leader panicked")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("%d reads, want 2: the failed one and one re-read shared by the waiters", calls.Load())
	}
}

// TestLiveHolders_PSListsOneOfTwo: ps asked for two pids prints only one
// (the other exited between the liveness check and ps). The printed one is
// checked; the missing one has an unknown start and counts, as IsLiveHolder
// counts a pid whose ps fails.
func TestLiveHolders_PSListsOneOfTwo(t *testing.T) {
	recorded := time.Date(2026, 10, 23, 15, 40, 46, 0, time.UTC)
	proc := recorded.Format(time.ANSIC)
	reg := map[int]ClaudeRegistryEntry{
		20: {PID: 20, SessionID: "reused", ProcStart: proc},
		21: {PID: 21, SessionID: "missing", ProcStart: proc},
	}
	ps := func([]int) ([]byte, error) { return []byte(startLine("20", recorded.Add(time.Hour))), nil }
	hs := liveHolders(reg, func(int) bool { return true }, ps)
	if len(hs) != 1 || hs[0].SessionID != "missing" {
		t.Fatalf("holders = %+v, want only missing (20 started an hour late: pid reused)", hs)
	}
}
