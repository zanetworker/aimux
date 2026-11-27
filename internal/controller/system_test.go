package controller

import "testing"

// The diagnosis has one line that says how many things are failing or worth a look: the line every frontend
// heads the findings with.
func TestProxyHeadline(t *testing.T) {
	f := func(levels ...string) []ProxyFinding {
		var out []ProxyFinding
		for _, l := range levels {
			out = append(out, ProxyFinding{Level: l})
		}
		return out
	}
	for _, c := range []struct {
		findings []ProxyFinding
		want     string
	}{
		{nil, "Nothing to fix."},
		{f(FindingOK, FindingOK), "Nothing to fix."},
		{f(FindingWarn), "1 thing to look at; nothing is failing."},
		{f(FindingWarn, FindingOK, FindingWarn), "2 things to look at; nothing is failing."},
		{f(FindingFail), "1 thing is failing."},
		{f(FindingFail, FindingFail), "2 things are failing."},
		{f(FindingFail, FindingWarn, FindingWarn), "1 thing is failing, 2 things to look at."},
	} {
		if got := ProxyHeadline(c.findings); got != c.want {
			t.Errorf("%v: %q, want %q", c.findings, got, c.want)
		}
	}
}

// An engine row says what the file states and what runs, in the words every frontend shows.
func TestEngineRowWords(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		row           EngineRow
		file, running string
		differs       bool
	}{
		{EngineRow{Name: "codex", On: true, Stated: true, Running: &yes}, "on", "yes", false},
		{EngineRow{Name: "anthropic", On: false, Stated: true, Running: &yes}, "off", "yes", true},
		{EngineRow{Name: "anthropic", On: true, Stated: false, Running: &yes}, "not stated", "yes", false},
		// The proxy did not say: nothing about running, and nothing differs.
		{EngineRow{Name: "enmaas", On: true, Stated: true}, "on", "-", false},
		{EngineRow{Name: "byhand", Type: EngineNotInFile, Stated: true, Running: &yes}, "-", "yes", true},
		{EngineRow{Name: "spare", On: false, Stated: true, Running: &no}, "off", "no", false},
	} {
		file, running, differs := c.row.Words()
		if file != c.file || running != c.running || differs != c.differs {
			t.Errorf("%s: %q %q %v, want %q %q %v", c.row.Name, file, running, differs, c.file, c.running, c.differs)
		}
	}
}

// The meter says what its figures leave out or cannot vouch for, a sentence each.
func TestMeterNotes(t *testing.T) {
	if got := MeterNotes(MeterReport{Files: 2, Rows: []MeterRow{{Key: "main", Requests: 2}}}); len(got) != 0 {
		t.Errorf("nothing to say: %v", got)
	}
	got := MeterNotes(MeterReport{Files: 2, Incomplete: true, Unmatched: 1, Skipped: 2, Torn: 3, NoUsage: 4, Rows: []MeterRow{{Key: "main", Estimated: true}}})
	want := []string{
		"The ledger has lines that could not be counted, so these figures are low.",
		"Not in the table: 1 with no session, 2 of another schema, 3 torn.",
		"4 of the calls counted carried no token figures.",
		"~ marks tokens that are partly estimated.",
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%q, want %q", got[i], want[i])
		}
	}
}
