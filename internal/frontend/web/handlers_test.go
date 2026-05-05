package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchHandler(t *testing.T) {
	s := NewServer(0)
	var launched bool
	s.SetLaunchFunc(func(provider, dir, model, mode string) error {
		launched = true
		if provider != "claude" {
			t.Errorf("expected provider claude, got %s", provider)
		}
		return nil
	})

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	body, _ := json.Marshal(map[string]string{
		"provider": "claude",
		"dir":      "/tmp/test",
		"model":    "opus",
		"mode":     "auto",
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/agents/launch failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !launched {
		t.Fatal("launch function was not called")
	}
}

func TestHistoryHandler(t *testing.T) {
	s := NewServer(0)

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history failed: %v", err)
	}
	defer resp.Body.Close()

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

func TestAnnotateHandler(t *testing.T) {
	s := NewServer(0)

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, ".aimux", "evaluations", "abc-123.jsonl"))
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGetAnnotationsHandler(t *testing.T) {
	s := NewServer(0)
	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, ".aimux", "evaluations", "test-annot-session.jsonl"))
	})

	// POST an annotation
	body, _ := json.Marshal(map[string]any{"turn": 1, "label": "good", "note": "clean code"})
	resp, err := http.Post(s.URL()+"/api/sessions/test-annot-session/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET annotations
	resp, err = http.Get(s.URL() + "/api/sessions/test-annot-session/annotations")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Annotations []map[string]any `json:"annotations"`
	}
	json.NewDecoder(resp.Body).Decode(&payload)
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
	os.WriteFile(sessionFile, []byte("{\"type\":\"user\"}\n"), 0o644)

	s := NewServer(0)
	go s.Start()
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET meta
	resp, err = http.Get(s.URL() + "/api/sessions/meta?file=" + sessionFile)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	var meta struct {
		Annotation string   `json:"annotation"`
		Tags       []string `json:"tags"`
		Note       string   `json:"note"`
	}
	json.NewDecoder(resp.Body).Decode(&meta)
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
