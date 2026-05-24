package controller

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/provider"
)

// Integration tests: real session fixtures, real parsers, real controller functions.
// No mocks, no stubs. Tests the full pipeline that all frontends share.

func fixtureFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "sample_session.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture not found: %s", path)
	}
	return path
}

func TestIntegration_ParseAndSort(t *testing.T) {
	file := fixtureFile(t)
	p := &provider.Claude{}
	turns, err := p.ParseTrace(file)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("expected turns from fixture, got 0")
	}

	agents := []agent.Agent{
		{PID: 1, Name: "zzz", Status: agent.StatusIdle, EstCostUSD: 5.0},
		{PID: 2, Name: "aaa", Status: agent.StatusActive, EstCostUSD: 10.0},
	}
	SortAgents(agents, "cost")
	if agents[0].Name != "aaa" {
		t.Errorf("sort by cost: first should be aaa (highest), got %s", agents[0].Name)
	}

	SortAgents(agents, "")
	if agents[0].Status != agent.StatusActive {
		t.Error("default sort: active agents first")
	}
}

func TestIntegration_ParseAndFilter(t *testing.T) {
	file := fixtureFile(t)
	p := &provider.Claude{}
	turns, err := p.ParseTrace(file)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}

	agents := []agent.Agent{
		{PID: 1, Name: "claude-project", ProviderName: "claude"},
		{PID: 2, Name: "codex-project", ProviderName: "codex"},
	}
	filtered := FilterAgents(agents, "claude")
	if len(filtered) != 1 || filtered[0].ProviderName != "claude" {
		t.Errorf("filter by 'claude': expected 1 result, got %d", len(filtered))
	}

	_ = turns // parsed successfully, confirms fixture is valid
}

func TestIntegration_ParseAndExport(t *testing.T) {
	file := fixtureFile(t)
	p := &provider.Claude{}
	turns, err := p.ParseTrace(file)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("expected turns from fixture")
	}

	inputs := TurnsToInputs(turns)
	if len(inputs) != len(turns) {
		t.Errorf("TurnsToInputs: got %d inputs, want %d", len(inputs), len(turns))
	}

	cfg := config.Default()
	ctrl := New(cfg)
	ctx := ExportContext{
		SessionID:    "integration-test",
		SessionFile:  file,
		ProviderName: "claude",
		Turns:        inputs,
		EvalStore:    evaluation.NewStore("integration-test"),
	}

	result, err := ctrl.ExportJSONL(ctx)
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if result.Count == 0 {
		t.Error("exported 0 turns")
	}
	if result.Path == "" {
		t.Error("export path is empty")
	}
	// Clean up export file
	defer func() { _ = os.Remove(result.Path) }()

	if _, err := os.Stat(result.Path); err != nil {
		t.Errorf("export file not created: %v", err)
	}
}

func TestIntegration_AttendCycle(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, Name: "working"},
		{PID: 2, Status: agent.StatusWaitingPermission, Name: "waiting"},
		{PID: 3, Status: agent.StatusIdle, Name: "idle"},
		{PID: 4, Status: agent.StatusError, Name: "error"},
	}

	idx := NextAttend(agents, -1)
	if idx != 1 {
		t.Errorf("first attend should be waiting (idx 1), got %d", idx)
	}

	idx = NextAttend(agents, 1)
	if agents[idx].Status == agent.StatusActive {
		t.Error("attend should skip active agents")
	}
}

func TestIntegration_ArchivePartition(t *testing.T) {
	now := time.Now()
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, LastActivity: now},
		{PID: 2, Status: agent.StatusIdle, LastActivity: now.Add(-2 * time.Hour)},
		{PID: 3, Status: agent.StatusWaitingPermission, LastActivity: now.Add(-3 * time.Hour)},
	}

	active, archived := PartitionByArchive(agents, 1*time.Hour)
	if len(active) != 2 {
		t.Errorf("expected 2 active (active + waiting), got %d", len(active))
	}
	if len(archived) != 1 || archived[0].PID != 2 {
		t.Errorf("expected 1 archived (idle), got %d", len(archived))
	}
}

func TestIntegration_SessionMeta(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test-session.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	starred, err := ToggleStar(file)
	if err != nil {
		t.Fatal(err)
	}
	if !starred {
		t.Error("first toggle should star")
	}

	if err := SetAnnotation(file, "achieved"); err != nil {
		t.Fatal(err)
	}
	if err := SetTags(file, []string{"bugfix", "urgent"}); err != nil {
		t.Fatal(err)
	}
	if err := SetNote(file, "Fixed the auth bug"); err != nil {
		t.Fatal(err)
	}

	starred, err = ToggleStar(file)
	if err != nil {
		t.Fatal(err)
	}
	if starred {
		t.Error("second toggle should unstar")
	}
}

func TestIntegration_KillAction(t *testing.T) {
	tests := []struct {
		name   string
		agent  agent.Agent
		expect KillType
	}{
		{"local process", agent.Agent{PID: 123}, KillProcess},
		{"k8s pod", agent.Agent{PID: 0, SessionID: "pod-my-agent-0", WorkingDir: "k8s://agents/repo"}, KillPod},
		{"session only", agent.Agent{PID: 0}, KillRemoveOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := DetermineKillAction(tt.agent)
			if action.Type != tt.expect {
				t.Errorf("KillAction = %v, want %v", action.Type, tt.expect)
			}
		})
	}
}

func TestIntegration_Notify(t *testing.T) {
	cfg := config.NotificationsConfig{Enabled: true, OnWaiting: true, OnError: true}

	n := ShouldNotify(agent.StatusWaitingPermission, "test-project", cfg)
	if n == nil {
		t.Error("expected notification for waiting status")
	}

	n = ShouldNotify(agent.StatusActive, "test-project", cfg)
	if n != nil {
		t.Error("active should never notify")
	}
}

func TestIntegration_EndToEnd_ParseSortFilterExport(t *testing.T) {
	file := fixtureFile(t)

	// 1. Parse real session file
	p := &provider.Claude{}
	turns, err := p.ParseTrace(file)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 2. Build agents from the parsed data
	agents := []agent.Agent{
		{PID: 1, Name: "project-a", ProviderName: "claude", Status: agent.StatusActive, EstCostUSD: 5.0, SessionFile: file},
		{PID: 2, Name: "project-b", ProviderName: "codex", Status: agent.StatusIdle, EstCostUSD: 1.0},
	}

	// 3. Sort
	SortAgents(agents, "cost")
	if agents[0].Name != "project-a" {
		t.Error("highest cost should be first after sort")
	}

	// 4. Filter
	filtered := FilterAgents(agents, "claude")
	if len(filtered) != 1 {
		t.Error("filter by claude should return 1")
	}

	// 5. Attend
	idx := NextAttend(agents, -1)
	if idx < 0 {
		t.Error("should find an agent needing attention")
	}

	// 6. Convert and export
	inputs := TurnsToInputs(turns)
	cfg := config.Default()
	ctrl := New(cfg)
	result, err := ctrl.ExportJSONL(ExportContext{
		SessionID:    "e2e-test",
		SessionFile:  file,
		ProviderName: "claude",
		Turns:        inputs,
		EvalStore:    evaluation.NewStore("e2e-test"),
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer func() { _ = os.Remove(result.Path) }()
	if result.Count == 0 {
		t.Error("exported 0 turns")
	}

	t.Logf("End-to-end: parsed %d turns, sorted %d agents, filtered to %d, exported %d turns to %s",
		len(turns), len(agents), len(filtered), result.Count, result.Path)
}
