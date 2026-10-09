package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAutomated(t *testing.T) {
	cases := []struct {
		name                         string
		entrypoint, cwd, firstPrompt string
		want                         bool
	}{
		{"sdk entrypoint (claude -p, cron)", "sdk-cli", "/Users/me/tools", "summarize", true},
		{"temp dir cwd", "cli", "/private/var/folders/y2/T/tmp.x", "hello", true},
		{"known prompt prefix", "cli", "/Users/me/r", "Read the JSON file /var/folders/a.json", true},
		{"interactive session", "cli", "/Users/me/research", "why does openshell need service accounts", false},
		{"unknown entrypoint, normal cwd", "", "/Users/me/research", "hello", false},
	}
	for _, c := range cases {
		if got := IsAutomated(c.entrypoint, c.cwd, c.firstPrompt, DefaultAutomatedPrefixes); got != c.want {
			t.Errorf("%s: IsAutomated = %v, want %v", c.name, got, c.want)
		}
	}
	if IsAutomated("cli", "/Users/me", "Read the JSON file x", nil) {
		t.Error("with no prefixes configured, prompt text alone must not mark a session automated")
	}
}

func writeLines(t *testing.T, dir, id string, lines ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func userLine(entrypoint, cwd, text string) map[string]any {
	return map[string]any{
		"type": "user", "entrypoint": entrypoint, "cwd": cwd, "timestamp": "2026-10-09T10:00:00Z",
		"message": map[string]any{"role": "user", "content": text},
	}
}

func TestScanSession_SetsAutomated(t *testing.T) {
	dir := t.TempDir()
	auto, err := scanSession("a", writeLines(t, dir, "a", userLine("sdk-cli", "/Users/me/tools", "Read the JSON file /var/folders/x")), "/p")
	if err != nil {
		t.Fatal(err)
	}
	if !auto.Automated {
		t.Error("sdk-cli session not marked automated")
	}
	human, _ := scanSession("h", writeLines(t, dir, "h", userLine("cli", "/Users/me/research", "is there a way to get my clipboard back")), "/p")
	if human.Automated {
		t.Error("interactive session marked automated")
	}
}

func TestScanSession_FindsFirstPromptPastLineTen(t *testing.T) {
	dir := t.TempDir()
	var lines []map[string]any
	for i := 0; i < 12; i++ { // metadata lines before any human message
		lines = append(lines, map[string]any{"type": "system", "cwd": "/Users/me/r", "entrypoint": "cli"})
	}
	lines = append(lines, userLine("cli", "/Users/me/r", "YOU ARE A SESSION ANALYZER. Analyze this"))
	s, err := scanSession("late", writeLines(t, dir, "late", lines...), "/p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.FirstPrompt, "YOU ARE A SESSION ANALYZER") || !s.Automated {
		t.Errorf("FirstPrompt=%q Automated=%v; a first prompt after line 10 must still be found and classified", s.FirstPrompt, s.Automated)
	}
}
