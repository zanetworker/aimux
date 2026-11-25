package modelproxy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/zanetworker/aimux/internal/ledger"
)

// A turn is one user request and everything done for it. The rule here, and the requests it was read from, are in
// docs/superpowers/specs/2026-11-25-turn-identity.md.

// maxTurnSessions bounds the turn table. A session dropped from it loses nothing while its conversation is still
// in its requests: the number is never below the prompts a request holds.
const maxTurnSessions = 4096

// sideRequest reports whether a request is off its session's main conversation: a subagent's, which carries the
// X-Claude-Code-Agent-Id header, or a background call, which has no session id in its metadata.
func sideRequest(metadataSession, agentHeader bool) bool {
	return agentHeader || !metadataSession
}

// requestShape reads a message list without keeping what it says: whether its last user message is a prompt (it
// holds no tool_result; a tool result can arrive with a text block beside it), how many such prompts the list
// holds, and a one-way fingerprint of the last user message, by which the same request sent again is recognised.
func requestShape(messages []conversationMessage) (startsTurn bool, prompts int, last uint64) {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		startsTurn = !holdsToolResult(message.Content)
		if startsTurn {
			prompts++
		}
		sum := sha256.Sum256(message.Content)
		last = binary.BigEndian.Uint64(sum[:8])
	}
	return startsTurn, prompts, last
}

func holdsToolResult(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false // a plain string is text
	}
	for _, block := range blocks {
		if block.Type == "tool_result" {
			return true
		}
	}
	return false
}

// turns numbers each session's turns.
type turns struct {
	mu       sync.Mutex
	sessions map[string]*turnState
	clock    uint64
}

type turnState struct {
	turn int
	// prompts and last describe the request that last started a turn: the same request sent again holds the same
	// number of prompts and the same last user message, and is the same turn. prompts is -1 when they are not
	// known (the number came from the ledger).
	prompts int
	last    uint64
	used    uint64
}

// next reports the turn a request belongs to, as a decimal number, or "" when there is no session or nothing is
// known of it yet. side: the request is off the main conversation and takes the session's current turn.
// startsTurn, prompts and last are requestShape's answers.
func (t *turns) next(session string, side, startsTurn bool, prompts int, last uint64) string {
	if session == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.sessions[session]
	if side {
		if state == nil || state.turn == 0 {
			return ""
		}
		t.clock++
		state.used = t.clock
		return strconv.Itoa(state.turn)
	}
	t.clock++
	if state == nil {
		state = &turnState{prompts: -1, used: t.clock}
		if t.sessions == nil {
			t.sessions = map[string]*turnState{}
		}
		t.sessions[session] = state
		t.forgetOldest()
	}
	state.used = t.clock
	switch {
	case startsTurn && (prompts != state.prompts || last != state.last):
		// A new prompt. After a compaction the list holds fewer prompts than the session has had turns, so the
		// number goes on from where it was.
		state.turn = max(state.turn+1, prompts)
		state.prompts, state.last = prompts, last
	default:
		// A tool result going back, or the same request sent again.
		state.turn = max(state.turn, prompts)
	}
	return strconv.Itoa(state.turn)
}

// forgetOldest drops the session used longest ago when the table is over its bound.
func (t *turns) forgetOldest() {
	if len(t.sessions) <= maxTurnSessions {
		return
	}
	oldest, least := "", ^uint64(0)
	for id, state := range t.sessions {
		if state.used < least {
			oldest, least = id, state.used
		}
	}
	delete(t.sessions, oldest)
}

// seed sets each session's number from model calls already in the ledger, so a proxy that restarted goes on from
// the last number it wrote. Only the most recent sessions are kept, up to the table's bound.
func (t *turns) seed(lines []ledger.Line) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = map[string]*turnState{}
	}
	for _, line := range lines {
		number, err := strconv.Atoi(line.Turn)
		if line.Kind != ledger.KindModelCall || line.Session == "" || err != nil || number <= 0 {
			continue
		}
		state := t.sessions[line.Session]
		if state == nil {
			state = &turnState{prompts: -1}
			t.sessions[line.Session] = state
		}
		t.clock++
		state.turn, state.used = max(state.turn, number), t.clock
		t.forgetOldest()
	}
}

func (t *turns) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sessions)
}
