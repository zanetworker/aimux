package web

import (
	"bytes"
	"encoding/json"
	"net/http"
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

func TestAnnotateHandler(t *testing.T) {
	s := NewServer(0)
	var annotated bool
	s.SetAnnotateFunc(func(sessionID string, turn int, label, note string) error {
		annotated = true
		if label != "good" {
			t.Errorf("expected label good, got %s", label)
		}
		return nil
	})

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	body, _ := json.Marshal(map[string]any{
		"turn":  1,
		"label": "good",
		"note":  "clean implementation",
	})
	resp, err := http.Post(s.URL()+"/api/agents/abc-123/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !annotated {
		t.Fatal("annotate function was not called")
	}
}
