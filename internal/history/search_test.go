package history

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSearchOutput_MultipleFiles(t *testing.T) {
	output := `/home/user/.claude/projects/foo/abc-123.jsonl
/home/user/.claude/projects/bar/def-456.jsonl
`
	matches := parseSearchOutput(output)
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
	if matches[0].SessionID != "abc-123" {
		t.Errorf("matches[0].SessionID = %q, want %q", matches[0].SessionID, "abc-123")
	}
	if matches[1].SessionID != "def-456" {
		t.Errorf("matches[1].SessionID = %q, want %q", matches[1].SessionID, "def-456")
	}
}

func TestParseSearchOutput_Empty(t *testing.T) {
	matches := parseSearchOutput("")
	if len(matches) != 0 {
		t.Errorf("got %d matches for empty input, want 0", len(matches))
	}
}

func TestParseSearchOutput_DeduplicatesSameSession(t *testing.T) {
	output := `/path/abc-123.jsonl
/path/abc-123.jsonl
`
	matches := parseSearchOutput(output)
	if len(matches) != 1 {
		t.Errorf("got %d matches, want 1 (deduped)", len(matches))
	}
}

func TestParseSearchOutput_SkipsNonJSONL(t *testing.T) {
	output := `/path/abc.json
/path/def.jsonl
/path/ghi.meta.json
`
	matches := parseSearchOutput(output)
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	if matches[0].SessionID != "def" {
		t.Errorf("SessionID = %q, want %q", matches[0].SessionID, "def")
	}
}

func TestCleanSnippet_BasicMatch(t *testing.T) {
	line := `{"message":{"content":"I added clipboard support to the project"}}`
	snippet := cleanSnippet(line, "clipboard")
	if snippet == "" {
		t.Fatal("cleanSnippet returned empty string")
	}
	if len(snippet) > 100 {
		t.Errorf("snippet too long: %d chars", len(snippet))
	}
}

func TestCleanSnippet_NoMatch(t *testing.T) {
	line := `{"message":{"content":"hello world"}}`
	snippet := cleanSnippet(line, "nonexistent")
	if snippet != "" {
		t.Errorf("expected empty snippet for non-match, got %q", snippet)
	}
}

func TestCleanSnippet_CleansEscapedJSON(t *testing.T) {
	line := `{"text":"line one\\nline two\\twith\\\"quotes\\\""}`
	snippet := cleanSnippet(line, "line one")
	if snippet == "" {
		t.Fatal("cleanSnippet returned empty")
	}
	// Should not contain raw \n or \t
	if contains(snippet, `\n`) || contains(snippet, `\t`) {
		t.Errorf("snippet still has escape sequences: %q", snippet)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSearchContent_EmptyQuery(t *testing.T) {
	matches, err := SearchContent("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches for empty query, got %d", len(matches))
	}
}

func TestSearchContent_NonexistentDir(t *testing.T) {
	matches, err := SearchContent("test", "/nonexistent/path/that/doesnt/exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches for nonexistent dir, got %d", len(matches))
	}
}

func TestSearchContent_MatchesTestFixture(t *testing.T) {
	// Create temp dir with a fake session JSONL
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "test-project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `{"type":"message","message":{"role":"user","content":"implement clipboard feature"}}
{"type":"message","message":{"role":"assistant","content":"I'll add clipboard support now"}}
`
	if err := os.WriteFile(filepath.Join(projectDir, "test-session-123.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	matches, err := SearchContent("clipboard", dir)
	if err != nil {
		t.Fatalf("SearchContent error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	if matches[0].SessionID != "test-session-123" {
		t.Errorf("SessionID = %q, want %q", matches[0].SessionID, "test-session-123")
	}
}

func TestSearchContentWithSnippets_ExtractsSnippet(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "test-project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `{"type":"message","message":{"role":"user","content":"please add ripgrep integration"}}
`
	if err := os.WriteFile(filepath.Join(projectDir, "sess-abc.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	matches, err := SearchContentWithSnippets("ripgrep", dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	if matches[0].Snippet == "" {
		t.Error("expected non-empty snippet")
	}
}

func TestSearchCommand_PreferRipgrep(t *testing.T) {
	bin, args := searchCommand("test", "/tmp/sessions")
	if bin == "" {
		t.Skip("neither rg nor grep in PATH")
	}
	// Should have the query and directory in args
	found := false
	for _, a := range args {
		if a == "test" {
			found = true
		}
	}
	if !found {
		t.Error("query not found in command args")
	}
}

func TestExtractSnippet_NoFile(t *testing.T) {
	snippet := extractSnippet("/nonexistent/file.jsonl", "test")
	if snippet != "" {
		t.Errorf("expected empty snippet for missing file, got %q", snippet)
	}
}

func TestSearchFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	content := `{"role":"user","content":"fix auth.go"}
{"role":"assistant","content":"looking at auth.go now"}
{"role":"tool","name":"Read","input":"auth.go"}
{"role":"assistant","content":"done with the fix"}
`
	os.WriteFile(path, []byte(content), 0644)

	count, snippet := SearchFile(path, "auth.go")
	if count != 3 {
		t.Errorf("expected 3 matches, got %d", count)
	}
	if snippet == "" {
		t.Error("expected non-empty snippet")
	}
}

func TestSearchFileEmpty(t *testing.T) {
	count, snippet := SearchFile("", "query")
	if count != 0 || snippet != "" {
		t.Error("expected zero results for empty path")
	}
}

func TestSearchFileMissing(t *testing.T) {
	count, snippet := SearchFile("/nonexistent/file.jsonl", "query")
	if count != 0 || snippet != "" {
		t.Error("expected zero results for missing file")
	}
}
