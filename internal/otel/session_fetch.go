package otel

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/trace"
)

// FetchSessionReplies reads the Claude Code session JSONL from inside a remote
// sandbox and returns assistant reply text keyed by promptId. Returns nil (not
// an error) if the file doesn't exist yet or the exec fails — this is called
// on every trace-pane refresh and transient failures are expected.
func FetchSessionReplies(sandboxName, sessionID string) map[string]string {
	if sandboxName == "" || sessionID == "" {
		return nil
	}

	path := fmt.Sprintf("/sandbox/.claude/projects/-sandbox/%s.jsonl", sessionID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "openshell", "sandbox", "exec",
		"--name", sandboxName, "--", "cat", path).Output() // #nosec G204
	if err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}

	replies := ParseSessionReplies(out)
	if len(replies) > 0 {
		debuglog.Log("session-file: fetched %d replies from %s/%s", len(replies), sandboxName, sessionID)
	}
	return replies
}

// EnrichTurnsWithReplies fills in OutputLines on turns that have a matching
// promptId in the replies map. The converter produces turns with promptId
// stored as the first UserLine's source — we match by scanning the OTEL
// span's prompt.id attribute via the turn's PromptID field.
func EnrichTurnsWithReplies(turns []trace.Turn, replies map[string]string) {
	if len(replies) == 0 {
		return
	}
	for i := range turns {
		if turns[i].PromptID == "" || len(turns[i].OutputLines) > 0 {
			continue
		}
		if text, ok := replies[turns[i].PromptID]; ok {
			turns[i].OutputLines = splitNonEmpty(text)
		}
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range []string{s} {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
