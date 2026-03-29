package views

import (
	"fmt"
	"strings"

	"github.com/zanetworker/aimux/internal/trace"
)

// TraceSnippet generates a compressed trace from the last few turns,
// limited to maxLines. Used by the dashboard mini-previews for agents
// without a tmux session.
//
// Output format:
//
//	TOOL ✓ Read config.go
//	TOOL ✓ Edit config.go:45
//	TOOL ✗ Bash go test ./...
//	ASST Done. All tests pass.
func TraceSnippet(turns []trace.Turn, maxLines int) string {
	if len(turns) == 0 || maxLines <= 0 {
		return ""
	}

	var lines []string

	// Walk backwards through turns, collect lines (newest first).
	for i := len(turns) - 1; i >= 0 && len(lines) < maxLines; i-- {
		t := turns[i]

		// Assistant output (last line only).
		if len(t.OutputLines) > 0 {
			text := lastLineFromSlice(t.OutputLines)
			if len(text) > 50 {
				text = text[:47] + "..."
			}
			if text != "" {
				lines = append(lines, fmt.Sprintf("ASST %s", text))
			}
		}

		// Tool actions (newest first within the turn).
		for j := len(t.Actions) - 1; j >= 0 && len(lines) < maxLines; j-- {
			a := t.Actions[j]
			icon := "✓"
			if !a.Success {
				icon = "✗"
			}
			snippet := a.Snippet
			if len(snippet) > 40 {
				snippet = snippet[:37] + "..."
			}
			lines = append(lines, fmt.Sprintf("TOOL %s %s %s", icon, a.Name, snippet))
		}
	}

	// Reverse to chronological order.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}

	// Trim to maxLines (keep the most recent).
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return strings.Join(lines, "\n")
}

// lastLineFromSlice returns the last non-empty trimmed line from a slice.
func lastLineFromSlice(ss []string) string {
	for i := len(ss) - 1; i >= 0; i-- {
		s := strings.TrimSpace(ss[i])
		if s != "" {
			return s
		}
	}
	return ""
}
