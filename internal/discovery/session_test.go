package discovery

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testdataPath(name string) string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata", name)
}

func TestParseSessionFile(t *testing.T) {
	path := testdataPath("sample_session.jsonl")
	info, err := ParseSessionFile(path)
	if err != nil {
		t.Fatalf("ParseSessionFile: %v", err)
	}

	if info.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "abc-123")
	}

	// Last git branch seen should be "feature" (from the 4th entry).
	if info.GitBranch != "feature" {
		t.Errorf("GitBranch = %q, want %q", info.GitBranch, "feature")
	}

	// TokensIn: 100 + 200 = 300
	if info.TokensIn != 300 {
		t.Errorf("TokensIn = %d, want 300", info.TokensIn)
	}

	// TokensOut: 50 + 100 = 150
	if info.TokensOut != 150 {
		t.Errorf("TokensOut = %d, want 150", info.TokensOut)
	}

	// CacheReadTokens: 500 + 1000 = 1500
	if info.CacheReadTokens != 1500 {
		t.Errorf("CacheReadTokens = %d, want 1500", info.CacheReadTokens)
	}

	// CacheWriteTokens: 200 + 0 = 200
	if info.CacheWriteTokens != 200 {
		t.Errorf("CacheWriteTokens = %d, want 200", info.CacheWriteTokens)
	}

	// 5 lines total = 5 messages (progress + 2 user + 2 assistant)
	if info.MessageCount != 5 {
		t.Errorf("MessageCount = %d, want 5", info.MessageCount)
	}

	// Last timestamp should be from the last assistant entry.
	expected, _ := time.Parse(time.RFC3339, "2026-02-20T16:35:10.000Z")
	if !info.LastTimestamp.Equal(expected) {
		t.Errorf("LastTimestamp = %v, want %v", info.LastTimestamp, expected)
	}
}

func TestParseSessionFileNotFound(t *testing.T) {
	_, err := ParseSessionFile("/nonexistent/file.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestFindSessionFile(t *testing.T) {
	// With a non-existent projects dir, should return empty string.
	result := findSessionFile("abc-123", "/nonexistent/projects")
	if result != "" {
		t.Errorf("findSessionFile should return empty for nonexistent dir, got %q", result)
	}
}
