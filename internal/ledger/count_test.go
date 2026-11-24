package ledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// eachActionOnceInput is one model call, one hook-reported tool call, the
// same hook event delivered again, a secondary MCP line for the same tool
// call, and a verdict, all in session s1 turn 1.
func eachActionOnceInput() []Line {
	return []Line{
		{At: at(1), Kind: KindModelCall, Writer: "model-proxy", Seq: 1, Session: "s1", Turn: "1", Call: "m1"},
		{At: at(2), Kind: KindToolCall, Writer: "collector", Seq: 1, EventID: "t1:PostToolUse", Session: "s1", Turn: "1", Call: "t1"},
		{At: at(3), Kind: KindToolCall, Writer: "collector", Seq: 2, EventID: "t1:PostToolUse", Session: "s1", Turn: "1", Call: "t1"},
		{At: at(2), Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, Session: "s1", Turn: "1", Call: "t1", Secondary: true},
		{At: at(4), Kind: KindVerdict, Writer: "model-proxy", Seq: 2, Session: "s1", Turn: "1", Call: "m1"},
	}
}

func kinds(lines []Line) []Kind {
	out := make([]Kind, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Kind)
	}
	return out
}

func TestCount_EachActionOnce(t *testing.T) {
	got := Count(eachActionOnceInput())

	if len(got.Lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3: %+v", len(got.Lines), got.Lines)
	}
	wantKinds := []Kind{KindModelCall, KindToolCall, KindVerdict}
	if !reflect.DeepEqual(kinds(got.Lines), wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds(got.Lines), wantKinds)
	}
	// The tool call that counts is the hook's first delivery, not the
	// duplicate and not the MCP proxy's secondary line.
	tool := got.Lines[1]
	if tool.Writer != "collector" || tool.Seq != 1 || tool.Secondary {
		t.Fatalf("counted tool line = %+v, want collector seq 1 primary", tool)
	}
	if len(got.Unmatched) != 0 {
		t.Fatalf("Unmatched = %+v, want none", got.Unmatched)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("Gaps = %v, want none", got.Gaps)
	}
}

func TestCount_SecondaryWithoutPrimaryCounts(t *testing.T) {
	in := []Line{
		{At: at(1), Kind: KindToolCall, Writer: "collector", Seq: 1, EventID: "t1:PostToolUse", Session: "s1", Turn: "1", Call: "t1"},
		{At: at(2), Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, Session: "s1", Turn: "1", Call: "t2", Secondary: true},
	}
	got := Count(in)

	if len(got.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2: %+v", len(got.Lines), got.Lines)
	}
	sec := got.Lines[1]
	if !sec.Secondary || sec.Call != "t2" || sec.Writer != "mcp-proxy" {
		t.Fatalf("Lines[1] = %+v, want the secondary mcp-proxy line for t2", sec)
	}
	if len(got.Unmatched) != 0 {
		t.Fatalf("Unmatched = %+v, want none", got.Unmatched)
	}
}

func TestCount_SecondaryWithoutCallIsUnmatched(t *testing.T) {
	noCall := Line{At: at(2), Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, Session: "s1", Turn: "1", Secondary: true}
	in := []Line{
		{At: at(1), Kind: KindModelCall, Writer: "model-proxy", Seq: 1, Session: "s1", Turn: "1", Call: "m1"},
		noCall,
	}
	got := Count(in)

	if len(got.Lines) != 1 || got.Lines[0].Kind != KindModelCall {
		t.Fatalf("Lines = %+v, want only the model call", got.Lines)
	}
	if !reflect.DeepEqual(got.Unmatched, []Line{noCall}) {
		t.Fatalf("Unmatched = %+v, want [%+v]", got.Unmatched, noCall)
	}
}

func TestCount_NoSessionIsUnmatched(t *testing.T) {
	bgModel := Line{At: at(1), Kind: KindModelCall, Writer: "model-proxy", Seq: 1, Call: "m1"}
	mpVerdict := Line{At: at(3), Kind: KindVerdict, Writer: "mcp-proxy", Seq: 1, Call: "t9"}
	in := []Line{
		mpVerdict,
		{At: at(2), Kind: KindToolCall, Writer: "collector", Seq: 1, EventID: "t1:PostToolUse", Session: "s1", Turn: "1", Call: "t1"},
		bgModel,
	}
	got := Count(in)

	if len(got.Lines) != 1 || got.Lines[0].Call != "t1" {
		t.Fatalf("Lines = %+v, want only the tool call t1", got.Lines)
	}
	// Unmatched is in time order too; a verdict from a writer other than
	// the tool gateway gets no exception.
	if !reflect.DeepEqual(got.Unmatched, []Line{bgModel, mpVerdict}) {
		t.Fatalf("Unmatched = %+v, want [bgModel, mpVerdict]", got.Unmatched)
	}
}

func TestCount_GatewayLinesWithoutSessionCount(t *testing.T) {
	// An admin-token caller of the tool gateway has no lease and so no
	// session; its calls and verdicts are real and must still be counted.
	verdict := Line{At: at(1), Kind: KindVerdict, Writer: "tool-gateway", Seq: 1, Call: "g1"}
	call := Line{At: at(2), Kind: KindToolCall, Writer: "tool-gateway", Seq: 2, Call: "g1"}
	got := Count([]Line{call, verdict})

	if !reflect.DeepEqual(got.Lines, []Line{verdict, call}) {
		t.Fatalf("Lines = %+v, want [gateway verdict, gateway tool call]", got.Lines)
	}
	if len(got.Unmatched) != 0 {
		t.Fatalf("Unmatched = %+v, want none", got.Unmatched)
	}
}

func TestCount_OtherWritersWithoutSessionStayUnmatched(t *testing.T) {
	verdict := Line{At: at(1), Kind: KindVerdict, Writer: "mcp-proxy", Seq: 1, Call: "c1"}
	got := Count([]Line{verdict})

	if len(got.Lines) != 0 || !reflect.DeepEqual(got.Unmatched, []Line{verdict}) {
		t.Fatalf("Lines = %+v, Unmatched = %+v, want the verdict unmatched", got.Lines, got.Unmatched)
	}
}

func TestCount_OrderIndependent(t *testing.T) {
	base := eachActionOnceInput()
	want := Count(base)

	shuffles := [][]int{
		{4, 3, 2, 1, 0},
		{2, 0, 4, 1, 3},
		{3, 2, 1, 4, 0}, // the duplicate hook event arrives before the first
	}
	for i, order := range shuffles {
		in := make([]Line, 0, len(base))
		for _, j := range order {
			in = append(in, base[j])
		}
		got := Count(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("shuffle %d: Count = %+v, want %+v", i, got, want)
		}
	}
	if len(want.Lines) != 3 {
		t.Fatalf("baseline len(Lines) = %d, want 3", len(want.Lines))
	}
}

func TestCount_Gaps(t *testing.T) {
	lines := []Line{
		{At: at(1), Kind: KindModelCall, Writer: "model-proxy", Seq: 1, Session: "s1", Call: "m1"},
		{At: at(2), Kind: KindModelCall, Writer: "model-proxy", Seq: 2, Session: "s1", Call: "m2"},
		{At: at(3), Kind: KindModelCall, Writer: "model-proxy", Seq: 5, Session: "s1", Call: "m5"},
		{At: at(4), Kind: KindModelCall, Writer: "collector", Seq: 9, Session: "s1", Call: "x"},
	}
	got := Count(lines).Gaps
	want := map[string][]Gap{"model-proxy": {{From: 3, To: 4}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Gaps = %+v, want %+v", got, want)
	}
}

func TestCount_GapsStayCheapOnACorruptSequence(t *testing.T) {
	// One absurd sequence number must cost one range, not a slice with a number per missing line.
	lines := []Line{
		{At: at(1), Kind: KindModelCall, Writer: "model-proxy", Seq: 1, Session: "s1", Call: "m1"},
		{At: at(2), Kind: KindModelCall, Writer: "model-proxy", Seq: 1 << 60, Session: "s1", Call: "m2"},
	}
	got := Count(lines).Gaps
	want := map[string][]Gap{"model-proxy": {{From: 2, To: 1<<60 - 1}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Gaps = %+v, want %+v", got, want)
	}
}

func TestCount_SecondaryOnlyWitnessCountsWithoutSession(t *testing.T) {
	// The MCP proxy knows no session today. When its line is the only record of a call, the call happened
	// and is counted, with its attribution unknown.
	only := Line{At: at(1), Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, Call: "c1", Secondary: true}
	got := Count([]Line{only})
	if !reflect.DeepEqual(got.Lines, []Line{only}) || len(got.Unmatched) != 0 {
		t.Fatalf("Lines = %+v, Unmatched = %+v, want the line counted", got.Lines, got.Unmatched)
	}
}

func TestCount_TwoPrimaryWritersCountOnce(t *testing.T) {
	// A sandbox with hooks would have the collector and the tool gateway both report one invocation.
	hook := Line{At: at(1), Kind: KindToolCall, Writer: "collector", Seq: 1, Session: "s1", Turn: "1", Call: "t1"}
	gateway := Line{At: at(2), Kind: KindToolCall, Writer: "tool-gateway", Seq: 1, Session: "s1", Turn: "1", Call: "t1"}
	other := Line{At: at(3), Kind: KindToolCall, Writer: "tool-gateway", Seq: 2, Session: "s1", Turn: "1", Call: "t2"}
	for _, in := range [][]Line{{hook, gateway, other}, {other, gateway, hook}} {
		got := Count(in)
		if !reflect.DeepEqual(got.Lines, []Line{hook, other}) {
			t.Fatalf("Lines = %+v, want the earliest line for t1 and the line for t2", got.Lines)
		}
	}
}

func TestLine_SwitchCarriesBothSessions(t *testing.T) {
	b, err := json.Marshal(Line{Kind: KindSwitch, Writer: "model-proxy", Session: "old", ToSession: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"session":"old"`) || !strings.Contains(string(b), `"to_session":"new"`) {
		t.Fatalf("switch line = %s, want both session ids", b)
	}
}

func TestCount_SameCallIDInTwoSessionsIsTwoCalls(t *testing.T) {
	// A harness's call id is only unique inside its session.
	a := Line{At: at(1), Kind: KindToolCall, Writer: "collector", Seq: 1, Session: "s1", Call: "call_1", EventID: "call_1:PostToolUse"}
	b := Line{At: at(2), Kind: KindToolCall, Writer: "collector", Seq: 2, Session: "s2", Call: "call_1", EventID: "call_1:PostToolUse"}
	got := Count([]Line{b, a})
	if !reflect.DeepEqual(got.Lines, []Line{a, b}) {
		t.Fatalf("Lines = %+v, want both calls", got.Lines)
	}
}

func TestCount_DroppedDuplicateDoesNotHideAnMCPLine(t *testing.T) {
	// The second delivery of a hook event is discarded, so the call id it alone carried covers nothing.
	kept := Line{At: at(1), Kind: KindToolCall, Writer: "collector", Seq: 1, Session: "s1", Call: "t1", EventID: "e1"}
	dropped := Line{At: at(2), Kind: KindToolCall, Writer: "collector", Seq: 2, Session: "s1", Call: "t9", EventID: "e1"}
	mcp := Line{At: at(3), Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, Call: "t9", Secondary: true}
	got := Count([]Line{kept, dropped, mcp})
	if !reflect.DeepEqual(got.Lines, []Line{kept, mcp}) {
		t.Fatalf("Lines = %+v, want the kept hook line and the MCP line", got.Lines)
	}
}

func TestCount_GapsAtTheTopOfTheRange(t *testing.T) {
	top := ^uint64(0)
	lines := []Line{
		{At: at(1), Kind: KindModelCall, Writer: "w", Seq: top, Session: "s1", Call: "a"},
		{At: at(2), Kind: KindModelCall, Writer: "w", Seq: top, Session: "s1", Call: "b"},
	}
	if got := Count(lines).Gaps; len(got) != 0 {
		t.Fatalf("Gaps = %+v, want none for a repeated sequence number", got)
	}
}

func TestCount_SameInstantInTwoZonesIsOrderIndependent(t *testing.T) {
	utc := at(1)
	cet := utc.In(time.FixedZone("CET", 3600))
	a := Line{At: utc, Kind: KindToolCall, Writer: "collector", Seq: 1, Session: "s1", Call: "t1", EventID: "e1"}
	b := Line{At: cet, Kind: KindToolCall, Writer: "collector", Seq: 1, Session: "s1", Call: "t1", EventID: "e1"}
	first, second := Count([]Line{a, b}), Count([]Line{b, a})
	if len(first.Lines) != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ by input order:\n%+v\n%+v", first, second)
	}
	if first.Lines[0].At.Location() != time.UTC {
		t.Fatalf("kept line's time is in %v, want UTC", first.Lines[0].At.Location())
	}
}

func TestCount_UnmatchedPrimaryDoesNotHideAnMCPLine(t *testing.T) {
	// A hook line with no session cannot be counted, so it must not suppress the MCP proxy's record of the call.
	hook := Line{At: at(1), Kind: KindToolCall, Writer: "collector", Seq: 1, Call: "t1"}
	mcp := Line{At: at(2), Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, Call: "t1", Secondary: true}
	got := Count([]Line{hook, mcp})
	if !reflect.DeepEqual(got.Lines, []Line{mcp}) || !reflect.DeepEqual(got.Unmatched, []Line{hook}) {
		t.Fatalf("Lines = %+v, Unmatched = %+v, want the MCP line counted and the hook line unmatched", got.Lines, got.Unmatched)
	}
}
