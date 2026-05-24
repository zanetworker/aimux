package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixtureDir returns the absolute path to the testdata directory.
// Skips the test if the directory does not exist.
func fixtureDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("testdata directory not found at %s", dir)
	}
	return dir
}

// fixturePath returns the full path to a fixture file, skipping if it
// does not exist.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(fixtureDir(t), name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		t.Skipf("fixture file %s not found", name)
	}
	return p
}

// TestIntegration_ClaudeParseTrace parses the real sample_session.jsonl
// fixture and validates the parsed turns match expected content.
func TestIntegration_ClaudeParseTrace(t *testing.T) {
	path := fixturePath(t, "sample_session.jsonl")

	c := &Claude{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("Claude.ParseTrace() error = %v", err)
	}

	// Expect 3 turns: "hello", "fix the bug", "edit the main.go file..."
	// The edit and write tool calls are grouped into turn 3 because the parser
	// only starts a new turn on human user messages. Tool result entries (array
	// content) do not start new turns, so Edit, Write, and the final text
	// response all belong to the same turn.
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}

	// Turn 1: "hello" -> "Hi!"
	t1 := turns[0]
	if len(t1.UserLines) == 0 || !strings.Contains(t1.UserLines[0], "hello") {
		t.Errorf("turn 1 UserLines = %v, want to contain 'hello'", t1.UserLines)
	}
	if len(t1.OutputLines) == 0 || !strings.Contains(t1.OutputLines[0], "Hi!") {
		t.Errorf("turn 1 OutputLines = %v, want to contain 'Hi!'", t1.OutputLines)
	}

	// Turn 2: "fix the bug" -> "Done."
	t2 := turns[1]
	if len(t2.UserLines) == 0 || !strings.Contains(t2.UserLines[0], "fix the bug") {
		t.Errorf("turn 2 UserLines = %v, want to contain 'fix the bug'", t2.UserLines)
	}

	// Turn 3: edit request -- should have both Edit and Write tool actions
	t3 := turns[2]
	if len(t3.UserLines) == 0 || !strings.Contains(t3.UserLines[0], "edit") {
		t.Errorf("turn 3 UserLines = %v, want to contain 'edit'", t3.UserLines)
	}

	// Verify Edit action
	foundEdit := false
	for _, action := range t3.Actions {
		if action.Name == "Edit" {
			foundEdit = true
			if !strings.Contains(action.FilePath, "main.go") {
				t.Errorf("Edit action FilePath = %q, want to contain 'main.go'", action.FilePath)
			}
			if action.OldString == "" {
				t.Error("Edit action OldString is empty, expected populated")
			}
			if action.NewString == "" {
				t.Error("Edit action NewString is empty, expected populated")
			}
			if !strings.Contains(action.NewString, "hello world") {
				t.Errorf("Edit action NewString = %q, want to contain 'hello world'", action.NewString)
			}
		}
	}
	if !foundEdit {
		t.Errorf("turn 3 has no Edit action, Actions = %v", t3.Actions)
	}

	// Verify Write action (in the same turn 3)
	foundWrite := false
	for _, action := range t3.Actions {
		if action.Name == "Write" {
			foundWrite = true
			if action.Content == "" {
				t.Error("Write action Content is empty, expected populated")
			}
			if !strings.Contains(action.Content, "debug: true") {
				t.Errorf("Write action Content = %q, want to contain 'debug: true'", action.Content)
			}
		}
	}
	if !foundWrite {
		t.Error("turn 3 has no Write action")
	}

	// Verify the final text response is in turn 3's OutputLines
	foundFinalText := false
	for _, line := range t3.OutputLines {
		if strings.Contains(line, "updated both files") {
			foundFinalText = true
			break
		}
	}
	if !foundFinalText {
		t.Errorf("turn 3 OutputLines = %v, want to contain 'updated both files'", t3.OutputLines)
	}

	// Verify token counts are populated on at least one turn
	hasTokens := false
	for _, turn := range turns {
		if turn.TokensIn > 0 && turn.TokensOut > 0 {
			hasTokens = true
			break
		}
	}
	if !hasTokens {
		t.Error("no turns have TokensIn > 0 and TokensOut > 0")
	}
}

// TestIntegration_CodexParseTrace parses the real codex_session.jsonl
// fixture and validates the parsed turns.
func TestIntegration_CodexParseTrace(t *testing.T) {
	path := fixturePath(t, "codex_session.jsonl")

	c := &Codex{}
	turns, err := c.ParseTrace(path)
	if err != nil {
		t.Fatalf("Codex.ParseTrace() error = %v", err)
	}

	// Expect at least 2 turns (2 user prompts)
	if len(turns) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(turns))
	}

	// Turn 1: "list files in src"
	t1 := turns[0]
	if len(t1.UserLines) == 0 {
		t.Fatal("turn 1 UserLines is empty")
	}
	userText := strings.Join(t1.UserLines, " ")
	if !strings.Contains(userText, "list files") {
		t.Errorf("turn 1 UserLines = %v, want to contain 'list files'", t1.UserLines)
	}

	// At least one turn should have Actions (tool calls)
	hasActions := false
	hasSuccess := false
	hasFailure := false
	for _, turn := range turns {
		for _, action := range turn.Actions {
			hasActions = true
			if action.Success {
				hasSuccess = true
			} else {
				hasFailure = true
			}
		}
	}
	if !hasActions {
		t.Error("no turns have Actions (tool calls)")
	}
	if !hasSuccess {
		t.Error("no tool action has Success == true")
	}
	if !hasFailure {
		t.Error("no tool action has Success == false (expected the error function_call_output)")
	}

	// Verify OutputLines are populated on at least one turn
	hasOutput := false
	for _, turn := range turns {
		if len(turn.OutputLines) > 0 {
			hasOutput = true
			break
		}
	}
	if !hasOutput {
		t.Error("no turns have OutputLines populated")
	}
}

// TestIntegration_GeminiParseTrace parses the real gemini_session.json
// fixture and validates the parsed turns.
func TestIntegration_GeminiParseTrace(t *testing.T) {
	path := fixturePath(t, "gemini_session.json")

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("Gemini.ParseTrace() error = %v", err)
	}

	// Expect 3 turns (3 user prompts)
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}

	// Each turn should have UserLines populated
	for i, turn := range turns {
		if len(turn.UserLines) == 0 {
			t.Errorf("turn %d UserLines is empty", i+1)
		}
	}

	// At least one turn should have OutputLines (gemini responses)
	hasOutput := false
	for _, turn := range turns {
		if len(turn.OutputLines) > 0 {
			hasOutput = true
			break
		}
	}
	if !hasOutput {
		t.Error("no turns have OutputLines populated")
	}

	// Verify timestamps are parsed (non-zero)
	for i, turn := range turns {
		if turn.Timestamp.IsZero() {
			t.Errorf("turn %d has zero Timestamp", i+1)
		}
	}

	// Turn 1: "explain the project structure"
	if !strings.Contains(turns[0].UserLines[0], "explain the project structure") {
		t.Errorf("turn 1 UserLines[0] = %q, want to contain 'explain the project structure'", turns[0].UserLines[0])
	}

	// Turn 2: should have multi-part content (array of text objects)
	t2 := turns[1]
	if len(t2.OutputLines) == 0 {
		t.Error("turn 2 OutputLines is empty, expected multi-part gemini response")
	}

	// Turn 3: "add error handling"
	if !strings.Contains(turns[2].UserLines[0], "error handling") {
		t.Errorf("turn 3 UserLines[0] = %q, want to contain 'error handling'", turns[2].UserLines[0])
	}
}
