package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestTaskResult_Text_RoundTrip(t *testing.T) {
	r := TaskResult{
		Type:     "text",
		Summary:  "Library X uses OAuth2",
		FullText: "Library X uses OAuth2 with PKCE for authentication...",
		Tokens:   4200,
		Duration: 12,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed TaskResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Type != "text" {
		t.Errorf("type: got %q, want 'text'", parsed.Type)
	}
	if parsed.Summary != "Library X uses OAuth2" {
		t.Errorf("summary: got %q", parsed.Summary)
	}
	if parsed.FullText == "" {
		t.Error("full_text should not be empty")
	}
	if parsed.Tokens != 4200 {
		t.Errorf("tokens: got %d", parsed.Tokens)
	}
}

func TestTaskResult_Branch_RoundTrip(t *testing.T) {
	r := TaskResult{
		Type:         "branch",
		Summary:      "Added tests for auth.go",
		Branch:       "task-abc123",
		Commit:       "a1b2c3d",
		FilesChanged: 3,
		Tokens:       8500,
		Duration:     45,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("result is not valid JSON")
	}

	var parsed TaskResult
	_ = json.Unmarshal(data, &parsed)
	if parsed.Branch != "task-abc123" {
		t.Errorf("branch: got %q", parsed.Branch)
	}
	if parsed.Commit != "a1b2c3d" {
		t.Errorf("commit: got %q", parsed.Commit)
	}
	if parsed.FilesChanged != 3 {
		t.Errorf("files_changed: got %d", parsed.FilesChanged)
	}
}

func TestTaskResult_OmitsEmpty(t *testing.T) {
	r := TaskResult{Type: "text", Summary: "done"}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if contains(s, "branch") {
		t.Error("branch field should be omitted when empty")
	}
	if contains(s, "commit") {
		t.Error("commit field should be omitted when empty")
	}
	if contains(s, "files_changed") {
		t.Error("files_changed should be omitted when 0")
	}
}
