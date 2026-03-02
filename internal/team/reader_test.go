package team

import (
	"path/filepath"
	"runtime"
	"testing"
)

// testdataDir returns the absolute path to the testdata/teams directory.
func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "teams")
}

func TestReadTeamConfig(t *testing.T) {
	cfgPath := filepath.Join(testdataDir(), "test-team", "config.json")
	cfg, err := ReadTeamConfig(cfgPath)
	if err != nil {
		t.Fatalf("ReadTeamConfig(%q) error: %v", cfgPath, err)
	}

	if cfg.Name != "test-team" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test-team")
	}
	if cfg.Description != "A test team" {
		t.Errorf("Description = %q, want %q", cfg.Description, "A test team")
	}
	if len(cfg.Members) != 2 {
		t.Fatalf("len(Members) = %d, want 2", len(cfg.Members))
	}
	if cfg.Members[0].Name != "team-lead" {
		t.Errorf("Members[0].Name = %q, want %q", cfg.Members[0].Name, "team-lead")
	}
	if cfg.Members[0].AgentID != "lead@test-team" {
		t.Errorf("Members[0].AgentID = %q, want %q", cfg.Members[0].AgentID, "lead@test-team")
	}
	if cfg.Members[0].AgentType != "team-lead" {
		t.Errorf("Members[0].AgentType = %q, want %q", cfg.Members[0].AgentType, "team-lead")
	}
	if cfg.Members[1].Name != "researcher" {
		t.Errorf("Members[1].Name = %q, want %q", cfg.Members[1].Name, "researcher")
	}
	if cfg.Members[1].Model != "claude-opus-4-6" {
		t.Errorf("Members[1].Model = %q, want %q", cfg.Members[1].Model, "claude-opus-4-6")
	}
}

func TestReadTeamConfig_NotFound(t *testing.T) {
	_, err := ReadTeamConfig("/nonexistent/config.json")
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
}

func TestListTeams(t *testing.T) {
	teams, err := ListTeams(testdataDir())
	if err != nil {
		t.Fatalf("ListTeams(%q) error: %v", testdataDir(), err)
	}

	if len(teams) != 1 {
		t.Fatalf("len(teams) = %d, want 1", len(teams))
	}
	if teams[0].Name != "test-team" {
		t.Errorf("teams[0].Name = %q, want %q", teams[0].Name, "test-team")
	}
	if len(teams[0].Members) != 2 {
		t.Errorf("len(teams[0].Members) = %d, want 2", len(teams[0].Members))
	}
}

func TestListTeams_EmptyDir(t *testing.T) {
	teams, err := ListTeams(t.TempDir())
	if err != nil {
		t.Fatalf("ListTeams on empty dir error: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("len(teams) = %d, want 0", len(teams))
	}
}
