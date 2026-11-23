package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/modelproxy"
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
	if err := modelProxyStats(path, time.Hour, "", &out, func() time.Time { return now }); err != nil {
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
	if err := modelProxyStats(path, time.Hour, "", &out, func() time.Time { return now }); err != nil {
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
	if err := modelProxyStats(path, time.Hour, "", &out, func() time.Time { return now }); err != nil {
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
	if err := modelProxyStats(path, time.Hour, "", &out, func() time.Time { return now }); err != nil {
		t.Fatalf("a damaged line should not stop the summary: %v", err)
	}
	if !strings.Contains(out.String(), "hit_continuation") {
		t.Errorf("the good line was dropped too:\n%s", out.String())
	}
}

func sessionRecord(at time.Time, outcome, session string, durationMs int) string {
	return fmt.Sprintf(`{"at":%q,"outcome":%q,"session":%q,"status":200,"ttfb_ms":100,"duration_ms":%d,"input_tokens":10,"cached_input_tokens":0,"output_tokens":5}`,
		at.UTC().Format(time.RFC3339), outcome, session, durationMs)
}

func TestModelProxyStatsShowsConversations(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	path := writeRequestLog(t,
		sessionRecord(now.Add(-5*time.Minute), "unmatched_tool_result", "aaaa1111", 40000),
		sessionRecord(now.Add(-4*time.Minute), "hit_continuation", "aaaa1111", 5000),
		sessionRecord(now.Add(-3*time.Minute), "hit_continuation", "bbbb2222", 4000),
		sessionRecord(now.Add(-2*time.Minute), "stateless", modelproxy.NoSession, 9000),
		sessionRecord(now.Add(-1*time.Minute), "hit_continuation", "", 3000),
	)
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, "", &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"SESSION", "aaaa1111", "bbbb2222", "no session id", "before session tags"} {
		if !strings.Contains(text, want) {
			t.Errorf("output does not mention %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "aaaa1111") > strings.Index(text, "bbbb2222") {
		t.Errorf("the conversation costing the most time should come first:\n%s", text)
	}
}

// One conversation only: the tag comes from hashing a session id the owner already has.
func TestModelProxyStatsFiltersBySessionID(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	wanted := modelproxy.SessionTag("146917eb-1666-4076-9ba6-9c7cf9ccbf7a")
	path := writeRequestLog(t,
		sessionRecord(now.Add(-5*time.Minute), "hit_continuation", wanted, 5000),
		sessionRecord(now.Add(-4*time.Minute), "hit_continuation", "bbbb2222", 4000),
	)
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, "146917eb-1666-4076-9ba6-9c7cf9ccbf7a", &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 requests") || strings.Contains(out.String(), "bbbb2222") {
		t.Errorf("filtering by session id did not keep only that conversation:\n%s", out.String())
	}
}

func TestModelProxyStatsAcceptsATagAsWellAsAnID(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	path := writeRequestLog(t, sessionRecord(now.Add(-time.Minute), "hit_continuation", "aaaa1111", 5000))
	var out bytes.Buffer
	if err := modelProxyStats(path, time.Hour, "aaaa1111", &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 requests") {
		t.Errorf("a tag passed straight through should work:\n%s", out.String())
	}
}
