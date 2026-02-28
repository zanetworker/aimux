package discovery

import (
	"encoding/json"
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
	result := FindSessionFile("abc-123", "/nonexistent/projects")
	if result != "" {
		t.Errorf("FindSessionFile should return empty for nonexistent dir, got %q", result)
	}
}

// --- extractLastToolAction tests ---

func TestExtractLastToolAction(t *testing.T) {
	tests := []struct {
		name    string
		content json.RawMessage
		want    string
	}{
		{
			name:    "nil content",
			content: nil,
			want:    "",
		},
		{
			name:    "no tool_use blocks",
			content: json.RawMessage(`[{"type":"text","text":"Hello, world!"}]`),
			want:    "",
		},
		{
			name:    "one tool_use Read block",
			content: json.RawMessage(`[{"type":"text","text":"Let me read the file."},{"type":"tool_use","name":"Read","input":{"file_path":"/path/to/filename.go"}}]`),
			want:    "Rd filename.go",
		},
		{
			name: "multiple tool_use blocks returns the last one",
			content: json.RawMessage(`[
				{"type":"tool_use","name":"Read","input":{"file_path":"/src/first.go"}},
				{"type":"text","text":"Now editing."},
				{"type":"tool_use","name":"Edit","input":{"file_path":"/src/second.go"}}
			]`),
			want: "Ed second.go",
		},
		{
			name:    "Edit block",
			content: json.RawMessage(`[{"type":"tool_use","name":"Edit","input":{"file_path":"/home/user/project/filename.go"}}]`),
			want:    "Ed filename.go",
		},
		{
			name:    "Bash block",
			content: json.RawMessage(`[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]`),
			want:    "Sh go test ./...",
		},
		{
			name:    "invalid JSON",
			content: json.RawMessage(`not json`),
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLastToolAction(tt.content)
			if got != tt.want {
				t.Errorf("extractLastToolAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- shortToolLabel tests ---

func TestShortToolLabel(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Read", "Rd"},
		{"Write", "Wr"},
		{"Edit", "Ed"},
		{"Bash", "Sh"},
		{"Grep", "Gr"},
		{"Glob", "Gl"},
		{"Task", "Tk"},
		{"SomeLongTool", "Som"},
		{"AB", "AB"},
		{"X", "X"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortToolLabel(tt.name)
			if got != tt.want {
				t.Errorf("shortToolLabel(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// --- toolSnippetForAction tests ---

func TestToolSnippetForAction(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		want     string
	}{
		{
			name:     "nil input",
			toolName: "Read",
			input:    nil,
			want:     "",
		},
		{
			name:     "Read with file_path returns base filename",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/home/user/project/main.go"},
			want:     "main.go",
		},
		{
			name:     "Write with file_path returns base filename",
			toolName: "Write",
			input:    map[string]interface{}{"file_path": "/tmp/output.txt"},
			want:     "output.txt",
		},
		{
			name:     "Edit with file_path returns base filename",
			toolName: "Edit",
			input:    map[string]interface{}{"file_path": "/src/handler.go"},
			want:     "handler.go",
		},
		{
			name:     "Bash with short command returns full command",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "go test ./..."},
			want:     "go test ./...",
		},
		{
			name:     "Bash with long command truncates to 17 chars plus ellipsis",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "go test -v -run TestSomethingVeryLong ./..."},
			want:     "go test -v -run T...",
		},
		{
			name:     "Grep with pattern returns slash-delimited pattern",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": "func main"},
			want:     "/func main/",
		},
		{
			name:     "Grep with long pattern truncates",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": "a]very]long]pattern]that]exceeds]limit"},
			want:     "/a]very]long].../",
		},
		{
			name:     "Glob with pattern",
			toolName: "Glob",
			input:    map[string]interface{}{"pattern": "**/*.go"},
			want:     "**/*.go",
		},
		{
			name:     "unknown tool returns empty",
			toolName: "UnknownTool",
			input:    map[string]interface{}{"foo": "bar"},
			want:     "",
		},
		{
			name:     "Read without file_path key",
			toolName: "Read",
			input:    map[string]interface{}{"path": "/some/path"},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolSnippetForAction(tt.toolName, tt.input)
			if got != tt.want {
				t.Errorf("toolSnippetForAction(%q, %v) = %q, want %q", tt.toolName, tt.input, got, tt.want)
			}
		})
	}
}
