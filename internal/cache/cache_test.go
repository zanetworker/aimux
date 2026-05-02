package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestSaveAndLoad(t *testing.T) {
	// Setup: create a temp directory
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "test-cache.json")

	// Create two test agents
	now := time.Now()
	agents := []agent.Agent{
		{
			PID:          1234,
			Name:         "aimux",
			ProviderName: "claude",
			WorkingDir:   "/Users/test/aimux",
			Model:        "claude-opus-4-6[1m]",
			Status:       agent.StatusActive,
			EstCostUSD:   1.25,
			GitBranch:    "main",
			LastActivity: now.Add(-5 * time.Minute),
		},
		{
			PID:          5678,
			Name:         "showtime",
			ProviderName: "codex",
			WorkingDir:   "/Users/test/showtime",
			Model:        "codex-default",
			Status:       agent.StatusIdle,
			EstCostUSD:   0.50,
			GitBranch:    "",
			LastActivity: now.Add(-10 * time.Minute),
		},
	}

	// Save agents to cache
	if err := Save(cachePath, agents); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		t.Fatal("Cache file was not created")
	}

	// Load agents back
	loaded, err := Load(cachePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify count
	if len(loaded) != 2 {
		t.Fatalf("Expected 2 agents, got %d", len(loaded))
	}

	// Verify first agent fields
	a := loaded[0]
	if a.PID != 1234 {
		t.Errorf("Expected PID 1234, got %d", a.PID)
	}
	if a.Name != "aimux" {
		t.Errorf("Expected Name 'aimux', got '%s'", a.Name)
	}
	if a.ProviderName != "claude" {
		t.Errorf("Expected ProviderName 'claude', got '%s'", a.ProviderName)
	}
	if a.WorkingDir != "/Users/test/aimux" {
		t.Errorf("Expected WorkingDir '/Users/test/aimux', got '%s'", a.WorkingDir)
	}
	if a.Model != "claude-opus-4-6[1m]" {
		t.Errorf("Expected Model 'claude-opus-4-6[1m]', got '%s'", a.Model)
	}
	if a.Status.String() != "Active" {
		t.Errorf("Expected Status 'Active', got '%s'", a.Status.String())
	}
	if a.EstCostUSD != 1.25 {
		t.Errorf("Expected EstCostUSD 1.25, got %.2f", a.EstCostUSD)
	}
	if a.GitBranch != "main" {
		t.Errorf("Expected GitBranch 'main', got '%s'", a.GitBranch)
	}
	if a.LastActivity.IsZero() {
		t.Error("Expected LastActivity to be set")
	}

	// Verify second agent
	b := loaded[1]
	if b.PID != 5678 {
		t.Errorf("Expected PID 5678, got %d", b.PID)
	}
	if b.Name != "showtime" {
		t.Errorf("Expected Name 'showtime', got '%s'", b.Name)
	}
	if b.GitBranch != "" {
		t.Errorf("Expected empty GitBranch, got '%s'", b.GitBranch)
	}
}

func TestLoadMissingFile(t *testing.T) {
	// Try to load from a path that doesn't exist
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "nonexistent.json")

	loaded, err := Load(cachePath)

	// Should return empty slice, not error
	if err != nil {
		t.Errorf("Expected no error for missing file, got: %v", err)
	}
	if loaded == nil {
		t.Error("Expected non-nil slice")
	}
	if len(loaded) != 0 {
		t.Errorf("Expected empty slice, got %d items", len(loaded))
	}
}

func TestLoadCorruptFile(t *testing.T) {
	// Setup: create a temp directory with garbage JSON
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "corrupt.json")

	// Write invalid JSON
	if err := os.WriteFile(cachePath, []byte("not valid json {{{"), 0644); err != nil {
		t.Fatalf("Failed to write corrupt file: %v", err)
	}

	// Try to load it
	loaded, err := Load(cachePath)

	// Should return empty slice, not error
	if err != nil {
		t.Errorf("Expected no error for corrupt file, got: %v", err)
	}
	if loaded == nil {
		t.Error("Expected non-nil slice")
	}
	if len(loaded) != 0 {
		t.Errorf("Expected empty slice, got %d items", len(loaded))
	}
}
