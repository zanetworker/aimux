package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

func TestFilterHidden(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, SessionID: "s1"},
		{PID: 2, SessionID: "s2"},
		{PID: 3, SessionID: "s3"},
	}
	hidden := map[string]bool{"s2": true}

	result := FilterHidden(agents, hidden)
	if len(result) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(result))
	}
	for _, a := range result {
		if a.SessionID == "s2" {
			t.Error("hidden agent s2 should be filtered out")
		}
	}
}

func TestFilterHidden_Empty(t *testing.T) {
	agents := []agent.Agent{{PID: 1, SessionID: "s1"}}
	result := FilterHidden(agents, nil)
	if len(result) != 1 {
		t.Errorf("expected 1 agent with nil hidden, got %d", len(result))
	}
}

func TestFilterHidden_FallbackToSessionFile(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, SessionFile: "/tmp/session.jsonl"},
	}
	hidden := map[string]bool{"/tmp/session.jsonl": true}

	result := FilterHidden(agents, hidden)
	if len(result) != 0 {
		t.Errorf("expected 0 agents when hidden by SessionFile, got %d", len(result))
	}
}

func TestFilterHidden_FallbackToPID(t *testing.T) {
	agents := []agent.Agent{
		{PID: 42},
	}
	hidden := map[string]bool{"pid-42": true}

	result := FilterHidden(agents, hidden)
	if len(result) != 0 {
		t.Errorf("expected 0 agents when hidden by PID, got %d", len(result))
	}
}

func TestDeleteSession_NonexistentFile(t *testing.T) {
	s := history.Session{FilePath: "/nonexistent/path.jsonl"}
	err := DeleteSession(s)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestDeleteSession_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Also create a meta sidecar
	metaPath := history.MetaPath(path)
	if err := os.WriteFile(metaPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := history.Session{FilePath: path}
	if err := DeleteSession(s); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("session file should be deleted")
	}
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("meta file should be deleted")
	}
}

func TestBulkDeleteSessions(t *testing.T) {
	dir := t.TempDir()

	// Create 3 fake session files
	var sessions []history.Session
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, fmt.Sprintf("session-%d.jsonl", i))
		os.WriteFile(path, []byte("{}"), 0o644)
		sessions = append(sessions, history.Session{ID: fmt.Sprintf("s%d", i), FilePath: path})
	}

	deleted, err := BulkDeleteSessions(sessions)
	if err != nil {
		t.Fatalf("BulkDeleteSessions: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}

	// Verify files are gone
	for _, s := range sessions {
		if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
			t.Errorf("session file %s should be deleted", s.FilePath)
		}
	}
}

func TestBulkDeleteSessions_PartialFailure(t *testing.T) {
	dir := t.TempDir()

	// One real file and one nonexistent
	realPath := filepath.Join(dir, "real.jsonl")
	os.WriteFile(realPath, []byte("{}"), 0o644)

	sessions := []history.Session{
		{ID: "s0", FilePath: realPath},
		{ID: "s1", FilePath: "/nonexistent/fake.jsonl"},
	}

	deleted, err := BulkDeleteSessions(sessions)
	if err == nil {
		t.Error("expected error for partial failure")
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
}
