package modelproxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoleOf(t *testing.T) {
	for name, tc := range map[string]struct {
		facts CallFacts
		want  string
	}{
		"the main conversation":                           {CallFacts{Session: "s1", SessionFromBody: true, Model: "claude-opus-5-5"}, RoleMain},
		"a main conversation on a small model is main":    {CallFacts{Session: "s1", SessionFromBody: true, Model: "claude-haiku-5-5"}, RoleMain},
		"a subagent, by its agent id":                     {CallFacts{Session: "s1", SessionFromBody: true, Agent: "a3000b5e"}, RoleSubagent},
		"a subagent whose body names no session":          {CallFacts{Session: "s1", Agent: "a3000b5e"}, RoleSubagent},
		"no session in the body and no agent: background": {CallFacts{Session: "s1"}, RoleBackground},
		"nothing known at all: background":                {CallFacts{}, RoleBackground},
	} {
		if got := RoleOf(tc.facts); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// messagesFacts is the facts of a request as the Claude Code front reads them.
func messagesFacts(t *testing.T, session, agent, model, prompt string) CallFacts {
	t.Helper()
	body := map[string]any{"model": model, "messages": []map[string]any{{"role": "user", "content": prompt}}}
	if session != "" {
		user, _ := json.Marshal(map[string]string{"session_id": session})
		body["metadata"] = map[string]string{"user_id": string(user)}
	}
	raw, _ := json.Marshal(body)
	var request messageRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(""))
	r.Header.Set("User-Agent", "claude-cli/2.1.332")
	if session != "" {
		r.Header.Set(sessionHeader, session)
	}
	if agent != "" {
		r.Header.Set(agentHeader, agent)
	}
	return callFactsOfMessages(r, request, "high")
}

func TestCallFactsOfMessages(t *testing.T) {
	f := messagesFacts(t, "0b61f1d5-session", "a3000b5e", "codex:gpt-6.2-sol", "hello")
	want := CallFacts{Session: "0b61f1d5-session", SessionFromBody: true, Agent: "a3000b5e", Model: "codex:gpt-6.2-sol", Effort: "high",
		Harness: "claude-code", StartsTurn: true, Prompts: 1, LastFingerprint: f.LastFingerprint}
	if f != want || f.LastFingerprint == 0 {
		t.Fatalf("facts = %+v", f)
	}
	// An id that is not one never reaches the ledger.
	if got := messagesFacts(t, "has a space", "", "m", "hi"); got.Session != "" || got.SessionFromBody {
		t.Errorf("a session id that is not an id was kept: %+v", got)
	}
}

// Nothing after the facts reads a Messages request: the same facts written by hand, as a front for another API
// would supply them, give the same ledger line, the same turn and the same switch.
func TestStartModelCall_TheSameFromAnyFront(t *testing.T) {
	read := messagesFacts(t, "0b61f1d5-session", "", "claude-opus-5-5", "first prompt")
	byHand := CallFacts{Session: "0b61f1d5-session", SessionFromBody: true, Model: "claude-opus-5-5", Effort: "high",
		Harness: "claude-code", StartsTurn: true, Prompts: 1, LastFingerprint: read.LastFingerprint}
	a, b := &turns{}, &turns{}
	for _, engine := range []string{"anthropic", "codex"} { // the second call is answered by another engine: a switch
		one, changeOne := startModelCall(a, read, engine, "")
		two, changeTwo := startModelCall(b, byHand, engine, "")
		one.Call, two.Call = "", ""
		if *one != *two || (changeOne == nil) != (changeTwo == nil) {
			t.Fatalf("%s: %+v\n    and %+v", engine, one, two)
		}
		if one.Role != RoleMain || one.Side {
			t.Errorf("role = %q, side = %v", one.Role, one.Side)
		}
		if engine == "codex" && (changeOne == nil || changeOne.FromEngine != "anthropic" || changeOne.ToEngine != "codex") {
			t.Errorf("switch = %+v", changeOne)
		}
	}
	// A subagent's call carries its role and stays off the main conversation, as before.
	sub := messagesFacts(t, "0b61f1d5-session", "a3000b5e", "codex:gpt-6.2-sol", "do this")
	line, change := startModelCall(a, sub, "codex", "codex:gpt-6.2-sol")
	if line.Role != RoleSubagent || !line.Side || line.Agent != "a3000b5e" || change != nil {
		t.Fatalf("subagent line = %+v, change = %+v", line, change)
	}
}
