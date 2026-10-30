package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scanAwayFixture(t *testing.T, data string) Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := scanSession("s", path, "/w")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Claude Code writes an away_summary when an interactive session sits idle;
// the latest one wins and is trimmed.
func TestScanSession_AwaySummary_LatestWins(t *testing.T) {
	s := scanAwayFixture(t, `{"type":"user","timestamp":"2026-01-01T10:00:00Z","cwd":"/w","entrypoint":"cli","message":{"role":"user","content":"plan the garden shed"}}
{"type":"system","subtype":"away_summary","content":"Sketched a shed plan.","timestamp":"2026-01-01T10:05:00Z"}
{"type":"system","subtype":"other","content":"not a summary","timestamp":"2026-01-01T10:06:00Z"}
{"type":"system","subtype":"away_summary","content":"  Shed plan done; next is the roof.  ","timestamp":"2026-01-01T10:09:00Z"}
`)
	if s.AwaySummary != "Shed plan done; next is the roof." {
		t.Fatalf("AwaySummary = %q", s.AwaySummary)
	}
}

func TestScanSession_AwaySummary_AbsentWhenNone(t *testing.T) {
	s := scanAwayFixture(t, `{"type":"user","timestamp":"2026-01-01T10:00:00Z","cwd":"/w","message":{"role":"user","content":"hello"}}
{"type":"system","subtype":"away_summary","content":"   ","timestamp":"2026-01-01T10:05:00Z"}
`)
	if s.AwaySummary != "" {
		t.Fatalf("AwaySummary = %q, want empty", s.AwaySummary)
	}
}

// A non-string content must not break the line's other fields or the summary.
func TestScanSession_AwaySummary_NonStringContentIgnored(t *testing.T) {
	s := scanAwayFixture(t, `{"type":"system","subtype":"away_summary","content":"First recap.","timestamp":"2026-01-01T10:05:00Z"}
{"type":"system","subtype":"away_summary","content":{"x":1},"timestamp":"2026-01-01T10:09:00Z"}
`)
	if s.AwaySummary != "First recap." {
		t.Fatalf("AwaySummary = %q", s.AwaySummary)
	}
	if s.LastActive.IsZero() {
		t.Fatal("timestamp of the line with object content was lost")
	}
}

func TestScanSession_AwaySummary_CappedAt600(t *testing.T) {
	long := strings.Repeat("é", 700) // multi-byte: the cap counts characters
	s := scanAwayFixture(t, `{"type":"system","subtype":"away_summary","content":"`+long+`","timestamp":"2026-01-01T10:05:00Z"}
`)
	if n := len([]rune(s.AwaySummary)); n != 600 {
		t.Fatalf("AwaySummary has %d characters, want 600", n)
	}
}

// Automated sessions are classified as before; a summary does not change that.
func TestScanSession_AwaySummary_AutomatedUnaffected(t *testing.T) {
	s := scanAwayFixture(t, `{"type":"user","timestamp":"2026-01-01T10:00:00Z","cwd":"/w","entrypoint":"sdk-cli","message":{"role":"user","content":"run the checks"}}
{"type":"system","subtype":"away_summary","content":"Checks ran.","timestamp":"2026-01-01T10:05:00Z"}
`)
	if !s.Automated {
		t.Fatal("sdk-cli session no longer automated")
	}
}

func TestScanSession_AwaySummaryAt_IsTheLatestRecapsTime(t *testing.T) {
	s := scanAwayFixture(t, `{"type":"system","subtype":"away_summary","content":"First.","timestamp":"2026-01-01T10:05:00Z"}
{"type":"system","subtype":"away_summary","content":"Second.","timestamp":"2026-01-01T10:09:00Z"}
{"type":"user","timestamp":"2026-01-01T10:20:00Z","message":{"role":"user","content":"later"}}
`)
	if want := time.Date(2026, 1, 1, 10, 9, 0, 0, time.UTC); !s.AwaySummaryAt.Equal(want) {
		t.Fatalf("AwaySummaryAt = %v, want %v", s.AwaySummaryAt, want)
	}
}

func TestDiscover_FillsDescriptionSource(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "-w")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello there"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(p, func(m *Meta) {
		m.Description, m.DescriptionBy, m.DescribedAt = "Ours.", "model", "2026-01-01T11:00:00Z"
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(DiscoverOpts{}, projects)
	if err != nil || len(got) != 1 {
		t.Fatalf("Discover = %v, %v", got, err)
	}
	if got[0].DescriptionBy != "model" || got[0].DescribedAt != "2026-01-01T11:00:00Z" {
		t.Errorf("session = %+v", got[0])
	}
}

func TestShownDescription(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for _, tc := range []struct {
		name         string
		s            Session
		text, source string
	}{
		{"nothing", Session{}, "", ""},
		{"away only", Session{AwaySummary: "Recap.", AwaySummaryAt: at("2026-01-01T10:00:00Z")}, "Recap.", "away_summary"},
		{"ours only", Session{Description: "Ours.", DescriptionBy: "model", DescribedAt: "2026-01-01T10:00:00Z"}, "Ours.", "model"},
		{"owner beats a newer recap", Session{Description: "Mine.", DescriptionBy: "owner", DescribedAt: "2026-01-01T09:00:00Z",
			AwaySummary: "Recap.", AwaySummaryAt: at("2026-01-01T12:00:00Z")}, "Mine.", "owner"},
		{"newer recap beats ours", Session{Description: "Ours.", DescriptionBy: "model", DescribedAt: "2026-01-01T09:00:00Z",
			AwaySummary: "Recap.", AwaySummaryAt: at("2026-01-01T12:00:00Z")}, "Recap.", "away_summary"},
		{"ours beats an older recap", Session{Description: "Ours.", DescriptionBy: "model", DescribedAt: "2026-01-01T12:00:00Z",
			AwaySummary: "Recap.", AwaySummaryAt: at("2026-01-01T09:00:00Z")}, "Ours.", "model"},
		{"same time: ours", Session{Description: "Ours.", DescribedAt: "2026-01-01T12:00:00Z",
			AwaySummary: "Recap.", AwaySummaryAt: at("2026-01-01T12:00:00Z")}, "Ours.", "model"},
		{"ours without a time loses to a recap", Session{Description: "Ours.", AwaySummary: "Recap.", AwaySummaryAt: at("2026-01-01T09:00:00Z")}, "Recap.", "away_summary"},
		{"owner marker without text", Session{DescriptionBy: "owner", AwaySummary: "Recap."}, "Recap.", "away_summary"},
	} {
		text, source := ShownDescription(tc.s)
		if text != tc.text || source != tc.source {
			t.Errorf("%s: got %q, %q; want %q, %q", tc.name, text, source, tc.text, tc.source)
		}
	}
}
