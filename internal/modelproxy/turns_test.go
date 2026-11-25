package modelproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/ledger"
)

// turnFixture is one request shape recorded from a real Claude Code session: roles and block kinds, no content.
type turnFixture struct {
	MetadataSession bool `json:"metadata_session"`
	AgentHeader     bool `json:"agent_header"`
	Messages        []struct {
		Role  string   `json:"role"`
		Kinds []string `json:"kinds"`
	} `json:"messages"`
	Prompts    int  `json:"prompts"`
	StartsTurn bool `json:"starts_turn"`
	Side       bool `json:"side"`
}

func loadTurnFixture(t *testing.T, name string) (turnFixture, []conversationMessage) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "turns", name+".json")) // #nosec G304 -- a fixture in this package
	if err != nil {
		t.Fatal(err)
	}
	var fx turnFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	// Rebuild a message list of the same shape, with placeholder content.
	var messages []conversationMessage
	for _, m := range fx.Messages {
		var blocks []map[string]string
		for _, kind := range m.Kinds {
			blocks = append(blocks, map[string]string{"type": kind})
		}
		content, _ := json.Marshal(blocks)
		messages = append(messages, conversationMessage{Role: m.Role, Content: content})
	}
	return fx, messages
}

var turnFixtures = []string{"fresh-prompt", "tool-continuation", "tool-continuation-with-text", "subagent", "background", "after-compaction"}

// Each recorded shape is read as the fixture says: whether it starts a turn, how many prompts it holds, and
// whether it is off the main conversation.
func TestRequestShape_FromRecordedShapes(t *testing.T) {
	for _, name := range turnFixtures {
		fx, messages := loadTurnFixture(t, name)
		starts, prompts, _ := requestShape(messages)
		side := sideRequest(fx.MetadataSession, fx.AgentHeader)
		// A side request never starts a turn, whatever its last message holds.
		if got := starts && !side; got != fx.StartsTurn || prompts != fx.Prompts || side != fx.Side {
			t.Errorf("%s: starts %v, prompts %d, side %v; fixture says %v, %d, %v", name, got, prompts, side, fx.StartsTurn, fx.Prompts, fx.Side)
		}
	}
}

// A user message whose content is a plain string is a prompt.
func TestRequestShape_StringContentIsAPrompt(t *testing.T) {
	starts, prompts, last := requestShape([]conversationMessage{conversation("user", `"hello"`)})
	if !starts || prompts != 1 || last == 0 {
		t.Fatalf("starts %v, prompts %d, fingerprint %d; want a turn start with one prompt", starts, prompts, last)
	}
	if _, _, other := requestShape([]conversationMessage{conversation("user", `"goodbye"`)}); other == last {
		t.Fatal("two different prompts have the same fingerprint")
	}
	if starts, prompts, _ := requestShape(nil); starts || prompts != 0 {
		t.Fatalf("an empty list: starts %v, prompts %d", starts, prompts)
	}
}

// One session through the recorded shapes: the number goes up on a new prompt only, a subagent and a background
// call take the current one, and a compaction does not start it again.
func TestTurns_ThroughOneSession(t *testing.T) {
	var table turns
	step := func(name string, prompts int, want string) {
		t.Helper()
		fx, messages := loadTurnFixture(t, name)
		starts, counted, _ := requestShape(messages)
		if prompts == 0 {
			prompts = counted
		}
		side := sideRequest(fx.MetadataSession, fx.AgentHeader)
		// The fingerprint stands for the prompt's text: here, one text per number of prompts.
		if got := table.next("s1", side, starts, prompts, uint64(prompts)); got != want { // #nosec G115 -- a small count
			t.Fatalf("%s: turn %q, want %q", name, got, want)
		}
	}
	step("fresh-prompt", 0, "1")
	step("tool-continuation-with-text", 0, "1")
	step("subagent", 0, "1")
	step("tool-continuation", 0, "1")
	step("background", 0, "1")
	step("fresh-prompt", 2, "2") // the second prompt: the list now holds two
	step("fresh-prompt", 2, "2") // the same request sent again is the same turn
	step("fresh-prompt", 3, "3")
	step("fresh-prompt", 4, "4")
	step("after-compaction", 0, "5") // its list holds 2 prompts; the number goes on
}

func TestTurns_SessionsDoNotShareNumbers(t *testing.T) {
	var table turns
	if a, b, a2 := table.next("a", false, true, 1, 1), table.next("b", false, true, 1, 1), table.next("a", false, true, 2, 2); a != "1" || b != "1" || a2 != "2" {
		t.Fatalf("a %q, b %q, a again %q; want 1, 1, 2", a, b, a2)
	}
	if got := table.next("", false, true, 1, 1); got != "" {
		t.Fatalf("no session: turn %q, want none", got)
	}
	// A side request for a session nothing is known about has no turn to take.
	if got := table.next("unknown", true, true, 1, 1); got != "" {
		t.Fatalf("a side request first: turn %q, want none", got)
	}
}

func TestTurns_ForgetsIdleSessions(t *testing.T) {
	var table turns
	for i := range maxTurnSessions + 50 {
		table.next(fmt.Sprintf("s%d", i), false, true, 1, 1)
	}
	if n := table.len(); n > maxTurnSessions {
		t.Fatalf("the table holds %d sessions, more than its bound %d", n, maxTurnSessions)
	}
	// The newest session is still known: its next prompt is turn 2.
	if got := table.next(fmt.Sprintf("s%d", maxTurnSessions+49), false, true, 2, 2); got != "2" {
		t.Fatalf("the newest session was forgotten: turn %q", got)
	}
}

// A session the table forgot does not reuse a number while the conversation is still in the request.
func TestTurns_AForgottenSessionDoesNotReuseANumber(t *testing.T) {
	var table turns
	if got := table.next("a", false, true, 3, 3); got != "3" {
		t.Fatalf("a prompt whose list holds three: turn %q, want 3", got)
	}
	var fresh turns
	if got := fresh.next("a", false, false, 3, 3); got != "3" {
		t.Fatalf("a tool result for a forgotten session: turn %q, want 3", got)
	}
}

// After a restart the table goes on from the ledger, so a compacted conversation does not start again at 1.
func TestTurns_SeededFromTheLedgerAfterARestart(t *testing.T) {
	var table turns
	table.seed([]ledger.Line{
		{Kind: ledger.KindModelCall, Session: "a", Turn: "6"},
		{Kind: ledger.KindModelCall, Session: "a", Turn: "7"},
		{Kind: ledger.KindModelCall, Session: "a", Turn: "7", Side: true},
		{Kind: ledger.KindModelCall, Session: "b", Turn: "2"},
		{Kind: ledger.KindModelCall, Session: "c", Turn: "not a number"},
		{Kind: ledger.KindToolCall, Session: "a", Turn: "99"},
	})
	if got := table.next("a", false, true, 1, 1); got != "8" {
		t.Fatalf("a prompt after a compaction and a restart: turn %q, want 8", got)
	}
	if got := table.next("b", false, false, 1, 1); got != "2" {
		t.Fatalf("a tool result after a restart: turn %q, want 2", got)
	}
	if got := table.next("c", false, true, 1, 1); got != "1" {
		t.Fatalf("a session whose line had no usable number: turn %q, want 1", got)
	}
}

// After a compaction a new prompt can hold as many prompts as the last one did. It is still a new turn, because
// its last message is another one; the same request sent again is not.
func TestTurns_ANewPromptWithTheSameCountIsANewTurn(t *testing.T) {
	var table turns
	if got := table.next("s", false, true, 2, 111); got != "2" {
		t.Fatalf("first: %q", got)
	}
	if got := table.next("s", false, true, 2, 111); got != "2" {
		t.Fatalf("the same request again: turn %q, want 2", got)
	}
	if got := table.next("s", false, true, 2, 222); got != "3" {
		t.Fatalf("another prompt with the same count: turn %q, want 3", got)
	}
}
