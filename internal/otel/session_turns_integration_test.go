//go:build integration

package otel

import (
	"os"
	"testing"
)

func TestFetchSessionTurns_LiveSandbox(t *testing.T) {
	sandbox := os.Getenv("TEST_SANDBOX")
	sessionID := os.Getenv("TEST_SESSION_ID")
	if sandbox == "" || sessionID == "" {
		t.Skip("set TEST_SANDBOX and TEST_SESSION_ID")
	}
	turns := FetchSessionTurns(sandbox, sessionID)
	if len(turns) == 0 {
		t.Fatal("FetchSessionTurns returned 0 turns")
	}
	for _, turn := range turns {
		out := "(no output)"
		if len(turn.OutputLines) > 0 {
			out = turn.OutputLines[0]
			if len(out) > 60 { out = out[:60] + "..." }
		}
		input := ""
		if len(turn.UserLines) > 0 { input = turn.UserLines[0] }
		t.Logf("Turn %d: input=%q output=%q", turn.Number, input, out)
	}
}
