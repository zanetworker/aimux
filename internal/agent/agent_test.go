package agent

import (
	"testing"
	"time"
)

func TestSourceTypeString(t *testing.T) {
	tests := []struct {
		source SourceType
		want   string
	}{
		{SourceCLI, "CLI"},
		{SourceVSCode, "VSCode"},
		{SourceSDK, "SDK"},
		{SourceType(99), "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.source.String(); got != tt.want {
			t.Errorf("SourceType(%d).String() = %q, want %q", tt.source, got, tt.want)
		}
	}
}

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusActive, "Active"},
		{StatusIdle, "Idle"},
		{StatusWaitingPermission, "Waiting"},
		{StatusUnknown, "Unknown"},
		{Status(99), "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusActive, "●"},
		{StatusIdle, "○"},
		{StatusWaitingPermission, "◐"},
		{StatusUnknown, "?"},
		{Status(99), "?"},
	}
	for _, tt := range tests {
		if got := tt.status.Icon(); got != tt.want {
			t.Errorf("Status(%d).Icon() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestShortModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-6[1m]", "opus-4.6"},
		{"claude-sonnet-4-5@20250929", "sonnet-4.5"},
		{"claude-haiku-3-5", "haiku-3.5"},
		{"claude-opus-4-6", "opus-4.6"},
		{"claude-sonnet-4-5[1m]", "sonnet-4.5"},
		{"unknown-model", "unknown-model"},
		{"singleword", "singleword"},
		{"", "default"},
	}
	for _, tt := range tests {
		a := Agent{Model: tt.model}
		if got := a.ShortModel(); got != tt.want {
			t.Errorf("ShortModel(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestShortProject(t *testing.T) {
	tests := []struct {
		workingDir string
		want       string
	}{
		{"/Users/user/projects/claudetopus", "claudetopus"},
		{"/home/dev/my-app", "my-app"},
		{"/single", "single"},
		{"", ""},
	}
	for _, tt := range tests {
		a := Agent{WorkingDir: tt.workingDir}
		if got := a.ShortProject(); got != tt.want {
			t.Errorf("ShortProject(%q) = %q, want %q", tt.workingDir, got, tt.want)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	tests := []struct {
		memoryMB uint64
		want     string
	}{
		{0, "0M"},
		{100, "100M"},
		{405, "405M"},
		{999, "999M"},
		{1000, "1.0G"},
		{1400, "1.4G"},
		{2048, "2.0G"},
		{10240, "10.2G"},
	}
	for _, tt := range tests {
		a := Agent{MemoryMB: tt.memoryMB}
		if got := a.FormatMemory(); got != tt.want {
			t.Errorf("FormatMemory(%d) = %q, want %q", tt.memoryMB, got, tt.want)
		}
	}
}

func TestFormatCost(t *testing.T) {
	tests := []struct {
		cost float64
		want string
	}{
		{0, "$0.00"},
		{0.82, "$0.82"},
		{12.5, "$12.50"},
		{0.001, "$0.00"},
		{100.999, "$101.00"},
	}
	for _, tt := range tests {
		a := Agent{EstCostUSD: tt.cost}
		if got := a.FormatCost(); got != tt.want {
			t.Errorf("FormatCost(%f) = %q, want %q", tt.cost, got, tt.want)
		}
	}
}

func TestAgentIcon(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusActive, "●"},
		{StatusIdle, "○"},
		{StatusWaitingPermission, "◐"},
		{StatusUnknown, "?"},
	}
	for _, tt := range tests {
		a := Agent{Status: tt.status}
		if got := a.Icon(); got != tt.want {
			t.Errorf("Agent.Icon() with status %v = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestAgeTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		agent    Agent
		wantZero bool
		wantTime time.Time
	}{
		{
			name:     "prefers StartTime when both set",
			agent:    Agent{StartTime: now.Add(-time.Hour), LastActivity: now.Add(-5 * time.Minute)},
			wantTime: now.Add(-time.Hour),
		},
		{
			name:     "falls back to LastActivity when StartTime zero",
			agent:    Agent{LastActivity: now.Add(-30 * time.Minute)},
			wantTime: now.Add(-30 * time.Minute),
		},
		{
			name:     "returns zero when both zero",
			agent:    Agent{},
			wantZero: true,
		},
		{
			name:     "uses StartTime even if older",
			agent:    Agent{StartTime: now.Add(-24 * time.Hour), LastActivity: now.Add(-time.Second)},
			wantTime: now.Add(-24 * time.Hour),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.agent.AgeTime()
			if tt.wantZero {
				if !got.IsZero() {
					t.Errorf("AgeTime() = %v, want zero time", got)
				}
				return
			}
			if !got.Equal(tt.wantTime) {
				t.Errorf("AgeTime() = %v, want %v", got, tt.wantTime)
			}
		})
	}
}

func TestAgentFullStruct(t *testing.T) {
	now := time.Now()
	a := Agent{
		PID:            12345,
		SessionID:      "sess-abc",
		Name:           "claudetopus",
		ProviderName:   "claude",
		SessionFile:    "/tmp/session.jsonl",
		Model:          "claude-opus-4-6[1m]",
		PermissionMode: "default",
		WorkingDir:     "/Users/user/projects/claudetopus",
		Source:         SourceCLI,
		StartTime:      now,
		Status:         StatusActive,
		TMuxSession:    "main",
		MemoryMB:       1400,
		GitBranch:      "feat/model",
		TokensIn:       5000,
		TokensOut:      3000,
		EstCostUSD:     0.82,
		TeamName:       "alpha",
		TaskID:         "task-1",
		TaskSubject:    "implement model",
		LastActivity:   now,
	}

	if got := a.ShortModel(); got != "opus-4.6" {
		t.Errorf("ShortModel() = %q, want %q", got, "opus-4.6")
	}
	if got := a.ShortProject(); got != "claudetopus" {
		t.Errorf("ShortProject() = %q, want %q", got, "claudetopus")
	}
	if got := a.FormatMemory(); got != "1.4G" {
		t.Errorf("FormatMemory() = %q, want %q", got, "1.4G")
	}
	if got := a.FormatCost(); got != "$0.82" {
		t.Errorf("FormatCost() = %q, want %q", got, "$0.82")
	}
	if got := a.Icon(); got != "●" {
		t.Errorf("Icon() = %q, want %q", got, "●")
	}
	if got := a.Source.String(); got != "CLI" {
		t.Errorf("Source.String() = %q, want %q", got, "CLI")
	}
	if a.Name != "claudetopus" {
		t.Errorf("Name = %q, want %q", a.Name, "claudetopus")
	}
	if a.ProviderName != "claude" {
		t.Errorf("ProviderName = %q, want %q", a.ProviderName, "claude")
	}
	if a.SessionFile != "/tmp/session.jsonl" {
		t.Errorf("SessionFile = %q, want %q", a.SessionFile, "/tmp/session.jsonl")
	}
}
