package cmd

import (
	"strings"
	"testing"
)

// What the proxy says of its engines when it starts: every engine it answers with, and that Anthropic is off
// when it is, so the owner need not ask to find out whether a flag took effect.
func TestServeEngineLines(t *testing.T) {
	both := strings.Join(serveEngineLines(true, []messagesUpstream{{name: "lab", address: "https://gateway.example.com"}}), "\n")
	for _, want := range []string{
		"Engines",
		"codex      on   the Codex program on this machine, on your ChatGPT plan; its start is reported below",
		"anthropic  on   Claude models go to https://api.anthropic.com on the caller's own Claude login; nothing to start",
		"lab        on   lab:<model> goes to https://gateway.example.com with the key kept for it",
	} {
		if !strings.Contains(both, want) {
			t.Errorf("no %q in:\n%s", want, both)
		}
	}
	alone := strings.Join(serveEngineLines(false, nil), "\n")
	if !strings.Contains(alone, "anthropic  off  start with --anthropic, or for the everyday proxy: aimux engine enable anthropic") || strings.Contains(alone, "lab") {
		t.Errorf("Codex alone:\n%s", alone)
	}
	// A long upstream name keeps the columns in line.
	long := serveEngineLines(true, []messagesUpstream{{name: "my-internal-gateway", address: "https://g.example.com"}})
	if !strings.HasPrefix(long[1], "  codex                on   ") {
		t.Errorf("columns: %q", long[1])
	}
}
