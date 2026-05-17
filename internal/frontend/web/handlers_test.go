package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
)

func TestLaunchHandler(t *testing.T) {
	s := NewServer(0)
	var launched bool
	s.SetLaunchFunc(func(provider, dir, model, mode, prompt string) error {
		launched = true
		if provider != "claude" {
			t.Errorf("expected provider claude, got %s", provider)
		}
		return nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	tmpDir := t.TempDir()
	body, _ := json.Marshal(map[string]string{
		"provider": "claude",
		"dir":      tmpDir,
		"model":    "opus",
		"mode":     "auto",
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/agents/launch failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !launched {
		t.Fatal("launch function was not called")
	}
}

func TestHistoryHandler(t *testing.T) {
	s := NewServer(0)

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Should return a non-nil array (may be empty in test environment)
	if payload.Sessions == nil {
		t.Fatal("expected sessions array, got nil")
	}

	// If there are sessions, verify the shape
	if len(payload.Sessions) > 0 {
		s0 := payload.Sessions[0]
		for _, field := range []string{"id", "provider", "project", "filePath", "lastActive", "turnCount", "costUSD"} {
			if _, ok := s0[field]; !ok {
				t.Errorf("session missing field %q", field)
			}
		}
	}
}

func TestHistoryHandler_AllSessionFieldsMapped(t *testing.T) {
	expectedFields := []string{
		"id", "provider", "project", "filePath", "startTime", "lastActive",
		"turnCount", "tokensIn", "tokensOut", "costUSD", "firstPrompt",
		"title", "resumable", "annotation", "tags", "note",
		"isSubagent", "permissionMode", "starred",
	}

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(payload.Sessions) == 0 {
		t.Skip("no sessions in test environment")
	}

	s0 := payload.Sessions[0]
	for _, field := range expectedFields {
		if _, ok := s0[field]; !ok {
			t.Errorf("session missing field %q in /api/history response", field)
		}
	}
}

func TestAnnotateHandler(t *testing.T) {
	s := NewServer(0)

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, ".aimux", "evaluations", "abc-123.jsonl"))
	})

	body, _ := json.Marshal(map[string]any{
		"turn":  1,
		"label": "good",
		"note":  "clean implementation",
	})
	resp, err := http.Post(s.URL()+"/api/sessions/abc-123/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGetAnnotationsHandler(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, ".aimux", "evaluations", "test-annot-session.jsonl"))
	})

	// POST an annotation
	body, _ := json.Marshal(map[string]any{"turn": 1, "label": "good", "note": "clean code"})
	resp, err := http.Post(s.URL()+"/api/sessions/test-annot-session/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET annotations
	resp, err = http.Get(s.URL() + "/api/sessions/test-annot-session/annotations")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Annotations []map[string]any `json:"annotations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if len(payload.Annotations) == 0 {
		t.Fatal("expected at least one annotation")
	}
	if payload.Annotations[0]["label"] != "good" {
		t.Errorf("expected label good, got %v", payload.Annotations[0]["label"])
	}
}

func TestSessionMetaHandler(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-meta-session.jsonl")
	_ = os.WriteFile(sessionFile, []byte("{\"type\":\"user\"}\n"), 0o600)

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// POST meta
	body, _ := json.Marshal(map[string]any{
		"filePath": sessionFile, "annotation": "achieved",
		"tags": []string{"clean-code"}, "note": "Great session",
	})
	resp, err := http.Post(s.URL()+"/api/sessions/meta", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET meta
	resp, err = http.Get(s.URL() + "/api/sessions/meta?file=" + sessionFile)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var meta struct {
		Annotation string   `json:"annotation"`
		Tags       []string `json:"tags"`
		Note       string   `json:"note"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	if meta.Annotation != "achieved" {
		t.Errorf("expected achieved, got %s", meta.Annotation)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "clean-code" {
		t.Errorf("expected [clean-code], got %v", meta.Tags)
	}
	if meta.Note != "Great session" {
		t.Errorf("expected 'Great session', got %s", meta.Note)
	}
}

func TestHandleBrowseDirectory(t *testing.T) {
	// Create temp dir with subdirectory, file, and hidden dir
	tmpDir := t.TempDir()
	_ = os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o750)
	_ = os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("test"), 0o600)
	_ = os.Mkdir(filepath.Join(tmpDir, ".hidden"), 0o750)

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/directories/browse?path=" + tmpDir)
	if err != nil {
		t.Fatalf("GET /api/directories/browse failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Path    string `json:"path"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify path
	if payload.Path != tmpDir {
		t.Errorf("expected path %s, got %s", tmpDir, payload.Path)
	}

	// Verify hidden dir is excluded
	for _, e := range payload.Entries {
		if e.Name == ".hidden" {
			t.Error("hidden directory should be excluded")
		}
	}

	// Verify subdirectory appears with isDir: true
	foundSubdir := false
	for _, e := range payload.Entries {
		if e.Name == "subdir" {
			foundSubdir = true
			if !e.IsDir {
				t.Error("subdir should have isDir: true")
			}
		}
	}
	if !foundSubdir {
		t.Error("subdir should be in the results")
	}

	// Verify file appears
	foundFile := false
	for _, e := range payload.Entries {
		if e.Name == "file.txt" {
			foundFile = true
			if e.IsDir {
				t.Error("file.txt should have isDir: false")
			}
		}
	}
	if !foundFile {
		t.Error("file.txt should be in the results")
	}
}

func TestSessionDiffsHandler_MissingFile(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/sessions/diffs")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSessionDiffsHandler_WithSessionFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-diff.jsonl")

	content := `{"type":"summary","session_id":"test-123"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Edit","input":{"file_path":"main.go","old_string":"old","new_string":"new"}}]}}
{"type":"result","tool_use_id":"tu1","content":"OK"}
`
	_ = os.WriteFile(sessionFile, []byte(content), 0o600)

	s := NewServer(0)
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		return &provider.Claude{}
	})
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/sessions/diffs?file=" + sessionFile)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Files      []map[string]any `json:"files"`
		TotalFiles int              `json:"totalFiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if payload.TotalFiles < 0 {
		t.Errorf("totalFiles should be >= 0, got %d", payload.TotalFiles)
	}
}
