package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The lines that tie everything to a session and an agent: a session line, an agent line, and a tool call that
// carries ids only. Each round-trips, and a line from before these fields reads as it did.
func TestIdentityLines_RoundTrip(t *testing.T) {
	yes := true
	at := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	for _, l := range []Line{
		{Schema: Schema, Kind: KindSession, Writer: "model-proxy", Seq: 1, At: at, Session: "s1", Harness: "claude-code", HarnessVersion: "2.1.332", Place: PlaceHost, StartedBy: "s0", Learned: LearnedSeen},
		{Schema: Schema, Kind: KindAgent, Writer: "collector", Seq: 2, At: at, Session: "s1", Agent: "a2", AgentParent: "a1", AgentType: "general-purpose", Learned: LearnedReported},
		{Schema: Schema, Kind: KindModelCall, Writer: "model-proxy", Seq: 3, At: at, Session: "s1", Agent: AgentRoot, Call: "mc_1", Request: "req_011", Reasoned: &yes, Learned: LearnedSeen},
		{Schema: Schema, Kind: KindToolCall, Writer: "model-proxy", Seq: 4, At: at, Session: "s1", Agent: "a2", Call: "toolu_01", EventID: "toolu_01:returned", AskedBy: "mc_1",
			ToolName: "search_messages", ToolServer: "slack", Outcome: OutcomeError, Learned: LearnedSeen},
		{Schema: Schema, Kind: KindToolCall, Writer: "collector", Seq: 5, At: at, Session: "s1", Call: "toolu_01", Secondary: true, DurationMs: 2453, ErrorType: "ShellError", PromptID: "p-1", Learned: LearnedReported},
		{Schema: Schema, Kind: KindSession, Writer: "model-proxy", Seq: 6, At: at, Session: "s2", Place: PlaceSandbox, Sandbox: "aimux-research", PlaceRequest: "osr-1", Learned: LearnedVerified},
	} {
		raw, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		var back Line
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		again, _ := json.Marshal(back)
		if string(raw) != string(again) {
			t.Errorf("did not round-trip:\n%s\n%s", raw, again)
		}
	}
	// The names on the wire are the ones the plan fixes.
	raw, _ := json.Marshal(Line{Schema: Schema, Kind: KindToolCall, ToolName: "Bash", ToolServer: "slack", AgentParent: "p", AgentType: "k", AskedBy: "m", ErrorType: "E",
		PromptID: "p1", Learned: LearnedSeen, Place: PlaceHost, Sandbox: "sb", PlaceRequest: "r", StartedBy: "s0", HarnessVersion: "1", Request: "req", Reasoned: &yes})
	for _, name := range []string{`"gen_ai.tool.name"`, `"aimux.tool.server"`, `"aimux.agent_parent"`, `"aimux.agent_type"`, `"aimux.asked_by"`, `"aimux.error_type"`, `"aimux.prompt_id"`,
		`"aimux.learned"`, `"aimux.place"`, `"aimux.sandbox"`, `"aimux.place_request"`, `"aimux.started_by"`, `"aimux.harness_version"`, `"aimux.request"`, `"aimux.reasoned"`} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("no %s in %s", name, raw)
		}
	}
	// Reasoned is three-valued: yes, no, and not known, which is left out.
	no := false
	said, _ := json.Marshal(Line{Schema: Schema, Kind: KindModelCall, Reasoned: &no})
	unknown, _ := json.Marshal(Line{Schema: Schema, Kind: KindModelCall})
	if !strings.Contains(string(said), `"aimux.reasoned":false`) || strings.Contains(string(unknown), "reasoned") {
		t.Errorf("reasoned: said %s, unknown %s", said, unknown)
	}
	// A line written before these fields has none of them, and reads as it did.
	var old Line
	if err := json.Unmarshal([]byte(`{"schema":"aimux.ledger/1","kind":"model_call","writer":"model-proxy","seq":7,"session":"s1","turn":"2","call":"mc_9"}`), &old); err != nil || old.Learned != "" || old.Reasoned != nil || old.Turn != "2" {
		t.Fatalf("%v %+v", err, old)
	}
}

// A name is written only when it looks like one; a tool's name is split into its MCP server and the tool.
func TestNameAndSplitTool(t *testing.T) {
	for in, want := range map[string]string{
		"general-purpose": "general-purpose", "search_messages": "search_messages", "Bash": "Bash", "a.b:c-d_e": "a.b:c-d_e",
		"": "", "has space": "other", "a/b": "other", "naïve": "other", strings.Repeat("x", 128): strings.Repeat("x", 128), strings.Repeat("x", 129): "other", "line\nbreak": "other",
	} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string][2]string{
		"Bash":                           {"", "Bash"},
		"mcp__slack__search_messages":    {"slack", "search_messages"},
		"mcp__google_workspace__get__it": {"google_workspace", "get__it"}, // the tool's own name may hold the separator
		"mcp__slack":                     {"", "mcp__slack"},              // not the form: kept whole
		"mcp____tool":                    {"", "mcp____tool"},
		"mcp__has space__tool":           {"other", "tool"},
		"":                               {"", ""},
	} {
		server, tool := SplitTool(in)
		if server != want[0] || tool != want[1] {
			t.Errorf("SplitTool(%q) = %q, %q; want %q, %q", in, server, tool, want[0], want[1])
		}
	}
}

// A tool call counts once, with what telemetry added: the proxy's line is the record, and a secondary line of the
// same call in the same session lends it a duration, a class of failure and the harness's turn id.
func TestCount_SecondaryLinesAddToTheRecord(t *testing.T) {
	proxy := Line{Kind: KindToolCall, Writer: "model-proxy", Seq: 1, At: at(1), Session: "s1", Agent: "a1", Call: "toolu_1", EventID: "toolu_1:returned", ToolName: "Bash", Outcome: OutcomeError, Learned: LearnedSeen}
	added := Line{Kind: KindToolCall, Writer: "collector", Seq: 1, At: at(2), Session: "s1", Call: "toolu_1", Secondary: true, DurationMs: 2453, ErrorType: "ShellError", PromptID: "p-1", Learned: LearnedReported}
	for name, lines := range map[string][]Line{"the proxy's first": {proxy, added}, "telemetry's first": {added, proxy}} {
		got := Count(lines)
		if len(got.Lines) != 1 || len(got.Unmatched) != 0 {
			t.Fatalf("%s: %d counted, %d unmatched", name, len(got.Lines), len(got.Unmatched))
		}
		l := got.Lines[0]
		if l.Writer != "model-proxy" || l.Learned != LearnedSeen || l.ToolName != "Bash" || l.DurationMs != 2453 || l.ErrorType != "ShellError" || l.PromptID != "p-1" {
			t.Errorf("%s: %+v", name, l)
		}
	}
	// The same call id in another session is another call: nothing is lent across sessions.
	other := added
	other.Session = "s2"
	got := Count([]Line{proxy, other})
	var first Line
	for _, l := range got.Lines {
		if l.Session == "s1" {
			first = l
		}
	}
	if first.DurationMs != 0 || len(got.Lines) != 1 || len(got.Orphans) != 1 {
		t.Errorf("across sessions: %d lines, %d orphans, duration %d", len(got.Lines), len(got.Orphans), first.DurationMs)
	}
	// A secondary line that names its session and has no record there is an orphan: evidence that something was
	// reported, set apart, and never counted as a call (it would inflate a session the proxy did not carry).
	alone := Count([]Line{added})
	if len(alone.Lines) != 0 || len(alone.Orphans) != 1 || len(alone.Unmatched) != 0 {
		t.Errorf("an orphan: %d counted, %d orphans, %d unmatched", len(alone.Lines), len(alone.Orphans), len(alone.Unmatched))
	}
	// What the proxy saw is never overwritten by what was reported.
	clash := added
	clash.Outcome, clash.ToolName = OutcomeOK, "Other"
	if l := Count([]Line{proxy, clash}).Lines[0]; l.Outcome != OutcomeError || l.ToolName != "Bash" {
		t.Errorf("a reported value replaced a seen one: %+v", l)
	}
	// The MCP proxy's rule holds as it was: its secondary line, which knows no session, is dropped for a primary
	// line of the same call id, and counted when it is the only witness.
	mcp := Line{Kind: KindToolCall, Writer: "mcp-proxy", Seq: 1, At: at(3), Call: "toolu_1", Secondary: true}
	if got := Count([]Line{proxy, mcp}); len(got.Lines) != 1 {
		t.Errorf("the MCP proxy's line beside a primary one: %d counted", len(got.Lines))
	}
	if got := Count([]Line{mcp}); len(got.Lines) != 1 {
		t.Errorf("the MCP proxy's line alone: %d counted", len(got.Lines))
	}
}

// Session and agent lines say who and where. They are never calls, and the same fact written twice (a proxy that
// restarted writes it again) is held to one; a fact that changed is kept, each with its time.
func TestCount_SessionAndAgentLines(t *testing.T) {
	session := Line{Kind: KindSession, Writer: "model-proxy", Seq: 1, At: at(1), Session: "s1", Harness: "claude-code", HarnessVersion: "2.1.332", Place: PlaceHost}
	again := session
	again.Seq, again.At = 5, at(9)
	moved := session
	moved.Seq, moved.At, moved.Place, moved.Sandbox = 6, at(10), PlaceSandbox, "aimux-research"
	agent := Line{Kind: KindAgent, Writer: "model-proxy", Seq: 2, At: at(2), Session: "s1", Agent: "a2", AgentParent: "a1"}
	agentAgain := agent
	agentAgain.Seq, agentAgain.At = 7, at(11)
	kind := Line{Kind: KindAgent, Writer: "collector", Seq: 1, At: at(3), Session: "s1", Agent: "a2", AgentType: "general-purpose"}
	call := Line{Kind: KindToolCall, Writer: "model-proxy", Seq: 3, At: at(4), Session: "s1", Agent: "a2", Call: "toolu_1"}

	got := Count([]Line{again, agentAgain, kind, call, moved, agent, session})
	byKind := map[Kind]int{}
	for _, l := range got.Lines {
		byKind[l.Kind]++
	}
	if byKind[KindSession] != 2 || byKind[KindAgent] != 2 || byKind[KindToolCall] != 1 {
		t.Fatalf("%v", byKind)
	}
	// The one kept of two equal lines is the earlier.
	for _, l := range got.Lines {
		if l.Kind == KindSession && l.Place == PlaceHost && !l.At.Equal(at(1)) {
			t.Errorf("kept the later of two equal session lines: %v", l.At)
		}
	}
	// A fact that returns to an earlier value is kept: host, then a sandbox, then the host again are three facts.
	back := session
	back.Seq, back.At = 8, at(12)
	places := 0
	for _, l := range Count([]Line{session, moved, back}).Lines {
		if l.Kind == KindSession {
			places++
		}
	}
	if places != 3 {
		t.Errorf("host, sandbox, host: %d session lines kept, want 3", places)
	}
	// The same fact learned another way is another line: what was reported does not stand in for what was seen.
	reported := session
	reported.Seq, reported.Learned = 9, LearnedReported
	if n := len(Count([]Line{session, reported}).Lines); n != 2 {
		t.Errorf("seen and reported: %d lines, want 2", n)
	}
	// A session or agent line with no session cannot be tied to anything.
	if got := Count([]Line{{Kind: KindAgent, Writer: "collector", Seq: 9, At: at(1), Agent: "a2", AgentType: "x"}}); len(got.Lines) != 0 || len(got.Unmatched) != 1 {
		t.Errorf("an agent line with no session: %d counted, %d unmatched", len(got.Lines), len(got.Unmatched))
	}
}
