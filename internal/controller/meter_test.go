package controller

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/ledger"
)

// meterLedger writes lines through the real writer and returns the folder.
func meterLedger(t *testing.T, lines ...ledger.Line) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	w, err := ledger.OpenWriter(dir, "model-proxy")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		w.Write(l)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func modelCall(session string, at time.Time, asked, served string, input, cached, output int64, outcome string) ledger.Line {
	return ledger.Line{Kind: ledger.KindModelCall, Session: session, At: at, RequestModel: asked, ResponseModel: served,
		InputTokens: input, CacheRead: cached, OutputTokens: output, Outcome: outcome}
}

var meterDay = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func TestMeter_SumsBySession(t *testing.T) {
	dir := meterLedger(t,
		modelCall("session-a", meterDay, "claude-sonnet-5-5", "codex:gpt-6.2-sol", 91200, 90000, 40, ledger.OutcomeOK),
		modelCall("session-a", meterDay.Add(time.Minute), "codex:gpt-6.2-sol", "codex:gpt-6.2-sol", 800, 0, 60, "timeout"),
		modelCall("session-b", meterDay, "codex:gpt-6.2-sol", "codex:gpt-6.2-sol", 500, 0, 10, ledger.OutcomeOK),
	)
	got, err := Meter(dir, MeterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantA := MeterRow{Key: "session-a", Requests: 2, Failed: 1, InputTokens: 2000, CachedTokens: 90000, OutputTokens: 100,
		Models: []string{"codex:gpt-6.2-sol"}, Asked: []string{"claude-sonnet-5-5", "codex:gpt-6.2-sol"}}
	if len(got.Rows) != 2 || !meterRowEqual(got.Rows[0], wantA) || got.Rows[1].Key != "session-b" {
		t.Fatalf("rows %+v; want session-a first as %+v", got.Rows, wantA)
	}
	if got.Total.Requests != 3 || got.Total.Failed != 1 || got.Total.InputTokens != 2500 || got.Total.CachedTokens != 90000 || got.Total.OutputTokens != 110 {
		t.Fatalf("total %+v", got.Total)
	}
	if got.Incomplete || got.Unmatched != 0 {
		t.Fatalf("report %+v; want a complete one", got)
	}
}

func meterRowEqual(a, b MeterRow) bool {
	return a.Key == b.Key && a.Requests == b.Requests && a.Failed == b.Failed && a.InputTokens == b.InputTokens &&
		a.CachedTokens == b.CachedTokens && a.OutputTokens == b.OutputTokens && a.Estimated == b.Estimated &&
		slices.Equal(a.Models, b.Models) && slices.Equal(a.Asked, b.Asked)
}

// By model the key is the model that answered, and the row still says which names were asked for.
func TestMeter_ByModelUsesTheServedModel(t *testing.T) {
	dir := meterLedger(t,
		modelCall("a", meterDay, "claude-sonnet-5-5", "codex:gpt-6.2-sol", 100, 0, 10, ledger.OutcomeOK),
		modelCall("b", meterDay, "codex:gpt-6.2-sol", "codex:gpt-6.2-sol", 100, 0, 10, ledger.OutcomeOK),
	)
	got, err := Meter(dir, MeterOptions{By: MeterByModel})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || got.Rows[0].Key != "codex:gpt-6.2-sol" || got.Rows[0].Requests != 2 || len(got.Rows[0].Models) != 0 ||
		!slices.Equal(got.Rows[0].Asked, []string{"claude-sonnet-5-5", "codex:gpt-6.2-sol"}) {
		t.Fatalf("rows %+v", got.Rows)
	}
}

// A day is the owner's local day: two calls two minutes apart around local midnight fall on different days.
func TestMeter_ByDayUsesTheLocalDay(t *testing.T) {
	zone := time.FixedZone("UTC+1", 3600)
	dir := meterLedger(t,
		modelCall("a", time.Date(2026, 11, 24, 22, 59, 0, 0, time.UTC), "m", "m", 100, 0, 10, ledger.OutcomeOK),
		modelCall("a", time.Date(2026, 11, 24, 23, 1, 0, 0, time.UTC), "m", "m", 300, 0, 10, ledger.OutcomeOK),
	)
	got, err := Meter(dir, MeterOptions{By: MeterByDay, Zone: zone})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 2 || got.Rows[0].Key != "2026-11-25" || got.Rows[1].Key != "2026-11-24" {
		t.Fatalf("rows %+v; want 2026-11-25 then 2026-11-24, the newest day first", got.Rows)
	}
}

func TestMeter_SinceAndSessionFilter(t *testing.T) {
	const first, second = "0b61f1d5-first-session", "220431f5-second-session"
	dir := meterLedger(t,
		modelCall(first, meterDay.Add(-48*time.Hour), "m", "m", 100, 0, 10, ledger.OutcomeOK),
		modelCall(first, meterDay, "m", "m", 200, 0, 10, ledger.OutcomeOK),
		modelCall(second, meterDay, "m", "m", 400, 0, 10, ledger.OutcomeOK),
	)
	got, _ := Meter(dir, MeterOptions{Since: meterDay.Add(-time.Hour)})
	if got.Total.Requests != 2 || got.Total.InputTokens != 600 {
		t.Fatalf("since: total %+v, want the two recent calls", got.Total)
	}
	// A session is named in full or by the start of its id, as the table shows it.
	for _, name := range []string{first, first[:8]} {
		got, _ = Meter(dir, MeterOptions{Session: name})
		if len(got.Rows) != 1 || got.Rows[0].Key != first || got.Total.Requests != 2 {
			t.Fatalf("session %q: rows %+v", name, got.Rows)
		}
	}
	if got, _ = Meter(dir, MeterOptions{Session: "ffffffff"}); len(got.Rows) != 0 || got.Total.Requests != 0 {
		t.Fatalf("a session that is not there: %+v", got)
	}
}

// A call with no session is counted apart: it is in no row and not in the total, and it is not dropped.
func TestMeter_CallsWithNoSessionAreCountedApart(t *testing.T) {
	dir := meterLedger(t,
		modelCall("a", meterDay, "m", "m", 100, 0, 10, ledger.OutcomeOK),
		modelCall("", meterDay, "m", "m", 900, 0, 90, ledger.OutcomeOK),
	)
	got, _ := Meter(dir, MeterOptions{})
	if got.Unmatched != 1 || got.Total.Requests != 1 || got.Total.InputTokens != 100 || len(got.Rows) != 1 {
		t.Fatalf("report %+v", got)
	}
}

// What could not be read is reported, and the totals are then marked incomplete.
func TestMeter_ReportsWhatItCouldNotRead(t *testing.T) {
	dir := meterLedger(t, modelCall("a", meterDay, "m", "m", 100, 0, 10, ledger.OutcomeOK))
	path := filepath.Join(dir, "model-proxy.jsonl")
	appendTo(t, path, `{"schema":"aimux.ledger/2","writer":"model-proxy","seq":2,"kind":"model_call","session":"a"}`+"\n")
	appendTo(t, path, `{"schema":"aimux.ledger/1","writer":"model-proxy","seq":5,"kind":"model_call","session":"a","at":"2026-11-24T12:00:00Z"}`+"\n")
	appendTo(t, path, `{"schema":"aimux.ledger/1","wri`)
	got, err := Meter(dir, MeterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Skipped != 1 || got.Torn != 1 || len(got.Gaps["model-proxy"]) != 1 || !got.Incomplete || got.Total.Requests != 2 {
		t.Fatalf("report %+v; want 1 skipped, 1 torn, a gap for model-proxy, incomplete, 2 requests", got)
	}
}

func TestMeter_EmptyFolder(t *testing.T) {
	got, err := Meter(filepath.Join(t.TempDir(), "none"), MeterOptions{})
	if err != nil || len(got.Rows) != 0 || got.Rows == nil || got.Total.Requests != 0 || got.Incomplete {
		t.Fatalf("report %+v, err %v; want no rows (an empty list), a zero total, complete", got, err)
	}
}

func TestMeter_OnlyModelCallsCount(t *testing.T) {
	dir := meterLedger(t,
		modelCall("a", meterDay, "m", "m", 100, 0, 10, ledger.OutcomeOK),
		ledger.Line{Kind: ledger.KindToolCall, Session: "a", Call: "t1", At: meterDay},
		ledger.Line{Kind: ledger.KindVerdict, Session: "a", At: meterDay},
	)
	got, _ := Meter(dir, MeterOptions{})
	if got.Total.Requests != 1 {
		t.Fatalf("total %+v; a tool call or a verdict was counted as a model call", got.Total)
	}
}

// A row says so when any of its calls had estimated usage.
func TestMeter_MarksEstimatedRows(t *testing.T) {
	estimated := modelCall("a", meterDay, "m", "m", 100, 0, 10, ledger.OutcomeOK)
	estimated.Estimated = true
	got, _ := Meter(meterLedger(t, estimated, modelCall("b", meterDay, "m", "m", 50, 0, 5, ledger.OutcomeOK)), MeterOptions{})
	if !got.Rows[0].Estimated || got.Rows[1].Estimated || !got.Total.Estimated {
		t.Fatalf("rows %+v, total %+v", got.Rows, got.Total)
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// A shortened session id that fits two sessions is refused, unless it is one of them in full.
func TestMeter_AmbiguousSessionIsRefused(t *testing.T) {
	dir := meterLedger(t,
		modelCall("abc1-one", meterDay, "m", "m", 100, 0, 10, ledger.OutcomeOK),
		modelCall("abc1-two", meterDay, "m", "m", 200, 0, 10, ledger.OutcomeOK),
		modelCall("abc1", meterDay, "m", "m", 400, 0, 10, ledger.OutcomeOK),
	)
	if _, err := Meter(dir, MeterOptions{Session: "abc1-"}); !errors.Is(err, ErrAmbiguousSession) {
		t.Fatalf("a prefix of two sessions: %v, want ErrAmbiguousSession", err)
	}
	got, err := Meter(dir, MeterOptions{Session: "abc1"})
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Key != "abc1" || got.Total.InputTokens != 400 {
		t.Fatalf("a whole id that others start with: %+v, %v; want that session alone", got.Rows, err)
	}
}

// A report says how many ledger files it read, so "nothing recorded yet" can be told from "nothing today".
func TestMeter_CountsTheFilesItRead(t *testing.T) {
	old := modelCall("a", meterDay.Add(-72*time.Hour), "m", "m", 100, 0, 10, ledger.OutcomeOK)
	got, _ := Meter(meterLedger(t, old), MeterOptions{Since: meterDay})
	if got.Files != 1 || len(got.Rows) != 0 {
		t.Fatalf("report %+v; want one file read and no rows in the window", got)
	}
}

func TestMeter_CallsWithNoTokenFiguresAreSaid(t *testing.T) {
	at := time.Now()
	dir := meterLedger(t,
		ledger.Line{Kind: ledger.KindModelCall, At: at, Session: "s1", ResponseModel: "claude-sonnet-5-5", InputTokens: 100, OutputTokens: 5, Outcome: ledger.OutcomeOK},
		ledger.Line{Kind: ledger.KindModelCall, At: at, Session: "s1", UsageUnknown: true, Outcome: "upstream_error"},
		ledger.Line{Kind: ledger.KindSwitch, At: at, Session: "s1", FromEngine: "anthropic", ToEngine: "codex"},
	)
	report, err := Meter(dir, MeterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.NoUsage != 1 || report.Total.Requests != 2 || report.Total.InputTokens != 100 || report.Total.Failed != 1 {
		t.Fatalf("report = %+v; want one call without figures among two requests, and the switch line counted as neither", report)
	}
}

func TestMeter_NamesTheEngines(t *testing.T) {
	at := time.Now()
	dir := meterLedger(t,
		ledger.Line{Kind: ledger.KindModelCall, At: at, Session: "s1", ResponseModel: "claude-haiku-5-5", Engine: "anthropic", InputTokens: 10, Outcome: ledger.OutcomeOK},
		ledger.Line{Kind: ledger.KindModelCall, At: at, Session: "s1", ResponseModel: "codex:gpt-6.2-sol", Engine: "codex", InputTokens: 20, Outcome: ledger.OutcomeOK},
		ledger.Line{Kind: ledger.KindModelCall, At: at, Session: "s1", ResponseModel: "codex:gpt-6.2-sol", Engine: "codex", InputTokens: 20, Outcome: ledger.OutcomeOK},
	)
	bySession, err := Meter(dir, MeterOptions{})
	if err != nil || len(bySession.Rows) != 1 || !slices.Equal(bySession.Rows[0].Engines, []string{"anthropic", "codex"}) {
		t.Fatalf("by session: %v, %+v", err, bySession.Rows)
	}
	byModel, err := Meter(dir, MeterOptions{By: MeterByModel})
	if err != nil || len(byModel.Rows) != 2 || byModel.Rows[0].Key != "codex:gpt-6.2-sol" || !slices.Equal(byModel.Rows[0].Engines, []string{"codex"}) || !slices.Equal(byModel.Rows[1].Engines, []string{"anthropic"}) {
		t.Fatalf("by model: %v, %+v", err, byModel.Rows)
	}
}
