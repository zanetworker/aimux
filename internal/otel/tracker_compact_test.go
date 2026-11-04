package otel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readTracker(t *testing.T, dir, id string) TrackerData {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, id+".json")) // #nosec G304
	if err != nil {
		t.Fatalf("read tracker %s: %v", id, err)
	}
	var d TrackerData
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("unmarshal tracker %s: %v", id, err)
	}
	return d
}

// addTurn adds a tool call plus an api_request worth `tokens` and `cost`.
func addTurn(s *SpanStore, conv, tool, tuid string, tokens int64, cost float64) {
	if s.GetByConversation(conv) == nil {
		s.Add(convSpan(conv+"-root", conv, "user_prompt", nil))
	}
	s.Add(convSpan(tuid, conv, "tool_result", map[string]any{"tool_name": tool, "tool_use_id": tuid}))
	s.Add(convSpan(tuid+"-api", conv, "api_request", map[string]any{
		"input_tokens": tokens, "cost_usd": cost,
	}))
}

func TestTracker_CompactMatchesFlush(t *testing.T) {
	build := func() *SpanStore {
		s := NewSpanStore()
		addTurn(s, "s1", "Read", "tu1", 1000, 0.10)
		addTurn(s, "s1", "mcp__x__y", "tu2", 3000, 0.30)
		return s
	}
	flushDir, compactDir := t.TempDir(), t.TempDir()
	if err := NewTracker(flushDir).Flush(build()); err != nil {
		t.Fatal(err)
	}
	if err := NewTracker(compactDir).Compact(build(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want, got := readTracker(t, flushDir, "s1"), readTracker(t, compactDir, "s1")
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if string(wb) != string(gb) {
		t.Errorf("Compact wrote %s, Flush wrote %s", gb, wb)
	}
}

func TestTracker_ResumedSessionKeepsEarlierCounts(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0.10)
	addTurn(s, "s1", "Read", "tu2", 1000, 0.10)
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.HasData() {
		t.Fatal("Compact left the idle session in memory")
	}

	addTurn(s, "s1", "Bash", "tu3", 2000, 0.20)
	if err := tr.Flush(s); err != nil {
		t.Fatal(err)
	}

	d := readTracker(t, dir, "s1")
	if d.Calls != 3 || d.ByTool["Read"].Calls != 2 || d.ByTool["Bash"].Calls != 1 {
		t.Errorf("calls = %d %+v, want 3 (Read 2, Bash 1)", d.Calls, d.ByTool)
	}
	if d.Tools != 4000 {
		t.Errorf("tokens = %d, want 4000", d.Tools)
	}
	if d.CostUSD < 0.399 || d.CostUSD > 0.401 {
		t.Errorf("cost = %f, want 0.40", d.CostUSD)
	}
}

func TestTracker_RestartLoadsSavedState(t *testing.T) {
	dir := t.TempDir()
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := NewTracker(dir).Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// A new process: fresh tracker and store over the same directory.
	s2 := NewSpanStore()
	addTurn(s2, "s1", "Read", "tu2", 1000, 0)
	if err := NewTracker(dir).Flush(s2); err != nil {
		t.Fatal(err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 2 || d.Tools != 2000 {
		t.Errorf("after restart calls=%d tokens=%d, want 2 and 2000", d.Calls, d.Tools)
	}
}

func TestTracker_ToolUseIDCountedOnceAcrossCompaction(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The OTEL batch for tu1 arrives after the hook event was compacted.
	s.Add(convSpan("s1-root", "s1", "user_prompt", nil))
	s.Add(convSpan("late", "s1", "tool_result", map[string]any{"tool_name": "Read", "tool_use_id": "tu1"}))
	if err := tr.Flush(s); err != nil {
		t.Fatal(err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 1 {
		t.Errorf("calls = %d, want 1 (tu1 already counted)", d.Calls)
	}
}

func TestTracker_CorruptStateStartsFresh(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, stateDirName), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateDirName, "s1.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := NewTracker(dir).Flush(s); err != nil {
		t.Fatalf("Flush with corrupt state: %v", err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 1 {
		t.Errorf("calls = %d, want 1", d.Calls)
	}
}

func TestTracker_CompactKeepsActiveSessions(t *testing.T) {
	dir := t.TempDir()
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := NewTracker(dir).Compact(s, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.GetByConversation("s1") == nil {
		t.Error("active session was compacted")
	}
	if _, err := os.Stat(filepath.Join(dir, stateDirName, "s1.json")); !os.IsNotExist(err) {
		t.Error("state written for a session that was not compacted")
	}
}

func TestTracker_CompactWriteFailureKeepsTotals(t *testing.T) {
	dir := t.TempDir()
	// A file where the state directory should be makes every state write fail.
	blocker := filepath.Join(dir, stateDirName)
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(dir)
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	addTurn(s, "s1", "Read", "tu2", 1000, 0)

	if err := tr.Compact(s, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("Compact returned nil although the state could not be saved")
	}

	addTurn(s, "s1", "Bash", "tu3", 1000, 0)
	if err := tr.Flush(s); err != nil {
		t.Fatal(err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 3 {
		t.Errorf("calls after failed save = %d, want 3", d.Calls)
	}

	// Once the disk recovers, the next compaction saves everything.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Compact after recovery: %v", err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 3 {
		t.Errorf("calls after recovery = %d, want 3", d.Calls)
	}
	if err := NewTracker(dir).Flush(NewSpanStore()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(blocker, "s1.json")); err != nil {
		t.Errorf("state not saved after recovery: %v", err)
	}
}

func TestTracker_CompactRetriesFailedSaveWithoutNewActivity(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, stateDirName)
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(dir)
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected a save error")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}

	// No new events for s1: the store is empty, the save must still retry.
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("retry Compact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(blocker, "s1.json")); err != nil {
		t.Fatalf("failed save was not retried: %v", err)
	}
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Errorf("Compact after a successful retry: %v", err)
	}
}

func TestTracker_CompactCreatesMissingDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "yet")
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := NewTracker(dir).Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 1 {
		t.Errorf("calls = %d, want 1", d.Calls)
	}
}

func TestTracker_CompactRetriesFailedTrackerWrite(t *testing.T) {
	dir := t.TempDir()
	// A directory where s1.json should be makes the tracker write fail.
	blocker := filepath.Join(dir, "s1.json")
	if err := os.MkdirAll(blocker, 0o750); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(dir)
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("Compact returned nil although the tracker file could not be written")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := tr.Compact(s, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("retry Compact: %v", err)
	}
	if d := readTracker(t, dir, "s1"); d.Calls != 1 {
		t.Errorf("calls = %d, want 1", d.Calls)
	}
}

func TestTracker_PendingRetryDoesNotOverwriteResumedSession(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, stateDirName)
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(dir)
	s := NewSpanStore()
	addTurn(s, "s1", "Read", "tu1", 1000, 0)
	_ = tr.Compact(s, time.Now().Add(time.Hour)) // save fails, s1 pending

	addTurn(s, "s1", "Bash", "tu2", 1000, 0) // s1 resumes
	if err := tr.Flush(s); err != nil {
		t.Fatal(err)
	}
	_ = tr.Compact(s, time.Now().Add(-time.Hour)) // s1 active: not compacted
	if d := readTracker(t, dir, "s1"); d.Calls != 2 {
		t.Errorf("calls = %d after pending retry, want 2 (old + resumed)", d.Calls)
	}
}
