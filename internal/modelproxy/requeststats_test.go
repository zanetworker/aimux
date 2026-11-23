package modelproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var statsNow = time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)

func writeHistory(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func historyLine(ago time.Duration, outcome string, durationMs, prompt int) string {
	return fmt.Sprintf(`{"at":%q,"outcome":%q,"status":200,"ttfb_ms":%d,"duration_ms":%d,"input_tokens":%d,"cached_input_tokens":0,"output_tokens":10}`,
		statsNow.Add(-ago).Format(time.RFC3339), outcome, durationMs/2, durationMs, prompt)
}

// A single slow request must not become the 95th percentile of twenty.
func TestRequestStatsPercentileIsZeroBased(t *testing.T) {
	lines := make([]string, 0, 20)
	for i := range 19 {
		lines = append(lines, historyLine(time.Duration(i)*time.Minute, "hit_continuation", 1000, 5000))
	}
	lines = append(lines, historyLine(20*time.Minute, "hit_continuation", 90000, 5000))
	stats, err := ReadRequestStats(writeHistory(t, lines...), time.Hour, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Outcomes[0].P95; got != time.Second {
		t.Errorf("p95 of nineteen 1s requests and one 90s request = %s, want 1s", got)
	}
}

// A turn paused at a tool call reports no prompt size; counting it as zero halves the average.
func TestRequestStatsAveragesOnlyReportedPrompts(t *testing.T) {
	path := writeHistory(t,
		historyLine(time.Minute, "hit_continuation", 2000, 10000),
		historyLine(2*time.Minute, "hit_continuation", 2000, 0),
	)
	stats, err := ReadRequestStats(path, time.Hour, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	outcome := stats.Outcomes[0]
	if outcome.MeanPrompt != 10000 || outcome.PromptSamples != 1 {
		t.Errorf("mean prompt = %d over %d samples, want 10000 over 1", outcome.MeanPrompt, outcome.PromptSamples)
	}
}

// A line too long for the scanner hides every request after it, which must not pass for a complete summary.
func TestRequestStatsReportsAnUnreadableTail(t *testing.T) {
	huge := `{"at":"2026-11-22T20:59:00Z","outcome":"hit_idle","detail":"` + strings.Repeat("x", 2<<20) + `"}`
	path := writeHistory(t, historyLine(time.Minute, "hit_continuation", 1000, 100), huge,
		historyLine(30*time.Second, "hit_idle", 1000, 100))
	stats, err := ReadRequestStats(path, time.Hour, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Incomplete {
		t.Error("a scan that stopped early was reported as a complete summary")
	}
}

func TestRequestStatsCountsFailuresAndDetails(t *testing.T) {
	path := writeHistory(t,
		`{"at":"2026-11-22T20:55:00Z","outcome":"unmatched_tool_result","detail":"pending=2 tail=[user:tool_result]","status":200,"duration_ms":36000,"input_tokens":1,"cached_input_tokens":0}`,
		`{"at":"2026-11-22T20:56:00Z","outcome":"stateless","status":502,"error":"app_server_exited","duration_ms":900}`,
	)
	stats, err := ReadRequestStats(path, time.Hour, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 2 || stats.Outcomes[0].Outcome != "unmatched_tool_result" {
		t.Fatalf("stats = %+v, want the slowest outcome first", stats)
	}
	if stats.Outcomes[0].Details["pending=2 tail=[user:tool_result]"] != 1 {
		t.Errorf("fallback detail was not counted: %+v", stats.Outcomes[0].Details)
	}
	if stats.Outcomes[1].Failed != 1 {
		t.Errorf("a 502 was not counted as failed: %+v", stats.Outcomes[1])
	}
}

func TestRequestStatsNeedsAHistory(t *testing.T) {
	if _, err := ReadRequestStats(filepath.Join(t.TempDir(), "requests.jsonl"), time.Hour, statsNow); err == nil {
		t.Fatal("a missing history should be reported, not summarised as empty")
	}
}

// A paused turn reports the last known prompt size, marked estimated; averaging it in invents a measurement.
func TestRequestStatsSkipsEstimatedPrompts(t *testing.T) {
	path := writeHistory(t,
		`{"at":"2026-11-22T20:58:00Z","outcome":"hit_continuation","status":200,"duration_ms":1000,"input_tokens":200,"cached_input_tokens":99800}`,
		`{"at":"2026-11-22T20:59:00Z","outcome":"hit_continuation","status":200,"duration_ms":1000,"input_tokens":0,"cached_input_tokens":100000,"estimated":true}`,
	)
	stats, err := ReadRequestStats(path, time.Hour, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Outcomes[0]; got.PromptSamples != 1 || got.MeanPrompt != 100000 {
		t.Errorf("mean prompt = %d over %d samples, want one measured sample", got.MeanPrompt, got.PromptSamples)
	}
}
