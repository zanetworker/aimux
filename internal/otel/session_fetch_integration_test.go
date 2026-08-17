//go:build integration

package otel

import (
	"os"
	"testing"
)

func TestFetchSessionReplies_LiveSandbox(t *testing.T) {
	sandbox := os.Getenv("TEST_SANDBOX")
	sessionID := os.Getenv("TEST_SESSION_ID")
	if sandbox == "" || sessionID == "" {
		t.Skip("set TEST_SANDBOX and TEST_SESSION_ID to run")
	}
	replies := FetchSessionReplies(sandbox, sessionID)
	if len(replies) == 0 {
		t.Fatal("FetchSessionReplies returned 0 replies")
	}
	for pid, text := range replies {
		short := text
		if len(short) > 60 {
			short = short[:60] + "..."
		}
		t.Logf("promptId=%s reply=%q", pid[:16], short)
	}
	t.Logf("PASS: %d replies fetched from live sandbox %s", len(replies), sandbox)
}
