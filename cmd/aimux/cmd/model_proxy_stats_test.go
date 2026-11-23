package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRequestLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func record(at time.Time, outcome string, ttfb, duration int, detail string) string {
	return fmt.Sprintf(`{"at":%q,"outcome":%q,"detail":%q,"model":"codex:gpt-6.2-sol","effort":"medium",`+
		`"status":200,"ttfb_ms":%d,"duration_ms":%d,"input_tokens":40,"cached_input_tokens":5504,"output_tokens":150}`,
		at.UTC().Format(time.RFC3339), outcome, detail, ttfb, duration)
}

func TestModelProxyStatsSummarisesEachOutcome(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	path := writeRequestLog(t,
		record(now.Add(-30*time.Minute), "hit_continuation", 1200, 4800, ""),
		record(now.Add(-20*time.Minute), "hit_continuation", 1400, 5200, ""),
		record(now.Add(-10*time.Minute), "unmatched_tool_result", 30000, 36500, "pending=2 tail=[user:tool_result user:text]"),
	)
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"hit_continuation", "unmatched_tool_result", "2", "36.5s", "pending=2"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary does not mention %q:\n%s", want, text)
		}
	}
}

func TestModelProxyStatsHonoursTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	path := writeRequestLog(t,
		record(now.Add(-26*time.Hour), "hit_idle", 1000, 2000, ""),
		record(now.Add(-5*time.Minute), "hit_continuation", 1000, 2000, ""),
	)
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "hit_idle") {
		t.Errorf("a request older than the window was counted:\n%s", out.String())
	}
}

func TestModelProxyStatsReportsAnEmptyWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	path := writeRequestLog(t, record(now.Add(-26*time.Hour), "hit_idle", 1000, 2000, ""))
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No requests") {
		t.Errorf("an empty window should say so plainly:\n%s", out.String())
	}
}

func TestModelProxyStatsIgnoresADamagedLine(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	path := writeRequestLog(t, "{not json", record(now.Add(-time.Minute), "hit_continuation", 1000, 2000, ""))
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, &out, func() time.Time { return now }); err != nil {
		t.Fatalf("a damaged line should not stop the summary: %v", err)
	}
	if !strings.Contains(out.String(), "hit_continuation") {
		t.Errorf("the good line was dropped too:\n%s", out.String())
	}
}
