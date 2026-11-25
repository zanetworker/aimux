package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/ledger"
)

func costTestReport() controller.MeterReport {
	return controller.MeterReport{
		Rows: []controller.MeterRow{
			{Key: "0b61f1d5-first-session", Requests: 212, Failed: 3, InputTokens: 41200, CachedTokens: 1400000, OutputTokens: 18050,
				Models: []string{"codex:gpt-6.2-sol"}, Asked: []string{"claude-sonnet-5-5", "codex:gpt-6.2-sol"}},
			{Key: "220431f5-second-session", Requests: 5, InputTokens: 900, OutputTokens: 185, Estimated: true,
				Models: []string{"codex:gpt-6.2-sol"}, Asked: []string{"codex:gpt-6.2-sol"}},
		},
		Total: controller.MeterRow{Requests: 217, Failed: 3, InputTokens: 42100, CachedTokens: 1400000, OutputTokens: 18235, Estimated: true},
	}
}

func TestCost_Table(t *testing.T) {
	var out bytes.Buffer
	if err := writeCost(&out, costTestReport(), controller.MeterBySession, "24h", false); err != nil {
		t.Fatal(err)
	}
	want := `Model calls in the last 24h

SESSION   ASKED AS           SERVED             CALLS  FAILED  IN      CACHED  OUT     COST
0b61f1d5  claude-sonnet-5-5  codex:gpt-6.2-sol  212    3       41.2k   1.4M    18.1k   unpriced
220431f5  -                  codex:gpt-6.2-sol  5      0       ~900    0       ~185    unpriced
total                                           217    3       ~42.1k  ~1.4M   ~18.2k  unpriced

~ usage was estimated for some of these calls
`
	if out.String() != want {
		t.Fatalf("table:\n%s\nwant:\n%s", out.String(), want)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > 100 {
			t.Fatalf("line of %d columns: %q", len(line), line)
		}
	}
}

// By model the key is the served model, so there is no SERVED column.
func TestCost_ByModelAndByDay(t *testing.T) {
	report := controller.MeterReport{Rows: []controller.MeterRow{{Key: "codex:gpt-6.2-sol", Requests: 2, InputTokens: 10, OutputTokens: 5, Asked: []string{"claude-haiku-5-5"}}},
		Total: controller.MeterRow{Requests: 2, InputTokens: 10, OutputTokens: 5}}
	var out bytes.Buffer
	_ = writeCost(&out, report, controller.MeterByModel, "7d", false)
	if got := out.String(); !strings.Contains(got, "MODEL              ASKED AS") || strings.Contains(got, "SERVED") || !strings.Contains(got, "claude-haiku-5-5") || !strings.Contains(got, "in the last 7d") {
		t.Fatalf("by model:\n%s", got)
	}
	// By model the engine that served each model is named, where the other views name the served models.
	report.Rows[0].Engines = []string{"codex"}
	out.Reset()
	_ = writeCost(&out, report, controller.MeterByModel, "7d", false)
	if got := out.String(); !strings.Contains(got, "ASKED AS          ENGINE  CALLS") || !strings.Contains(got, "claude-haiku-5-5  codex ") {
		t.Fatalf("by model, with engines:\n%s", got)
	}
	report.Rows[0].Engines = nil
	out.Reset()
	report.Rows[0].Key, report.Rows[0].Models = "2026-11-25", []string{"codex:gpt-6.2-sol"}
	_ = writeCost(&out, report, controller.MeterByDay, "all", false)
	if got := out.String(); !strings.Contains(got, "DAY         ASKED AS") || !strings.Contains(got, "2026-11-25") || !strings.Contains(got, "Model calls, all recorded") {
		t.Fatalf("by day:\n%s", got)
	}
}

// What the ledger could not show is said under the table.
func TestCost_NotesWhatIsMissing(t *testing.T) {
	report := costTestReport()
	report.Unmatched, report.Skipped, report.Torn, report.Incomplete = 2, 1, 1, true
	report.Gaps = map[string][]ledger.Gap{"model-proxy": {{From: 4, To: 6}, {From: 9, To: 9}}}
	var out bytes.Buffer
	_ = writeCost(&out, report, controller.MeterBySession, "24h", false)
	for _, want := range []string{
		"2 calls with no session (counted in no row and not in the total)",
		"1 line skipped: written by another version of aimux",
		"1 line not whole",
		"4 lines missing from model-proxy (line 4 to 6, line 9)",
		"totals are incomplete",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCost_EmptyLedgerSaysWhatToDo(t *testing.T) {
	var out bytes.Buffer
	_ = writeCost(&out, controller.MeterReport{Rows: []controller.MeterRow{}}, controller.MeterBySession, "24h", false)
	if got := out.String(); !strings.Contains(got, "No model calls recorded in the last 24h") || !strings.Contains(got, "aimux upgrade model-proxy") {
		t.Fatalf("empty ledger:\n%s", got)
	}
	// A ledger that exists and had a quiet day needs no upgrade.
	out.Reset()
	_ = writeCost(&out, controller.MeterReport{Rows: []controller.MeterRow{}, Files: 1}, controller.MeterBySession, "24h", false)
	if got := out.String(); got != "No model calls recorded in the last 24h.\n" {
		t.Fatalf("a quiet day:\n%s", got)
	}
}

func TestCost_JSONCarriesTheReport(t *testing.T) {
	var out bytes.Buffer
	if err := writeCost(&out, costTestReport(), controller.MeterBySession, "24h", true); err != nil {
		t.Fatal(err)
	}
	var got controller.MeterReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(got.Rows) != 2 || got.Rows[0].Key != "0b61f1d5-first-session" || got.Total.Requests != 217 {
		t.Fatalf("report %+v", got)
	}
}

func TestCostOptions(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for since, want := range map[string]time.Time{"24h": now.Add(-24 * time.Hour), "7d": now.Add(-7 * 24 * time.Hour), "90m": now.Add(-90 * time.Minute), "all": {}} {
		opts, err := costOptions(since, "session", "", now)
		if err != nil || !opts.Since.Equal(want) {
			t.Errorf("--since %s: %v, %v; want %v", since, opts.Since, err, want)
		}
	}
	if _, err := costOptions("24h", "week", "", now); err == nil || !strings.Contains(err.Error(), "session, model or day") {
		t.Fatalf("--by week: %v", err)
	}
	for _, bad := range []string{"soon", "-3h", "0", "99999999d"} {
		if _, err := costOptions(bad, "session", "", now); err == nil || !strings.Contains(err.Error(), "--since") {
			t.Fatalf("--since %s: %v", bad, err)
		}
	}
	if opts, _ := costOptions("24h", "day", "0b61f1d5", now); opts.By != controller.MeterByDay || opts.Session != "0b61f1d5" {
		t.Fatalf("options %+v", opts)
	}
}

func TestCostCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1.0k", 41200: "41.2k", 999949: "999.9k", 1400000: "1.4M", 12345678: "12.3M"} {
		if got := costCount(n); got != want {
			t.Errorf("costCount(%d) = %q, want %q", n, got, want)
		}
	}
}
