package provider

import (
	"testing"
	"time"
)

func TestRoleString(t *testing.T) {
	tests := []struct {
		role Role
		want string
	}{
		{RoleUser, "User"},
		{RoleAssistant, "Assistant"},
		{RoleTool, "Tool"},
		{RoleSystem, "System"},
		{Role(99), "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.role.String(); got != tt.want {
			t.Errorf("Role(%d).String() = %q, want %q", tt.role, got, tt.want)
		}
	}
}

func TestSegmentZeroValue(t *testing.T) {
	var s Segment

	if !s.Time.IsZero() {
		t.Error("zero-value Segment.Time should be zero")
	}
	if s.Role != RoleUser {
		t.Errorf("zero-value Segment.Role = %v, want RoleUser (0)", s.Role)
	}
	if s.Content != "" {
		t.Error("zero-value Segment.Content should be empty")
	}
	if s.Tool != "" {
		t.Error("zero-value Segment.Tool should be empty")
	}
	if s.Detail != "" {
		t.Error("zero-value Segment.Detail should be empty")
	}
}

func TestSegmentWithValues(t *testing.T) {
	now := time.Now()
	s := Segment{
		Time:    now,
		Role:    RoleTool,
		Content: "Reading file",
		Tool:    "Read",
		Detail:  "/path/to/file.go",
	}

	if s.Time != now {
		t.Error("Segment.Time should match assigned value")
	}
	if s.Role != RoleTool {
		t.Errorf("Segment.Role = %v, want RoleTool", s.Role)
	}
	if s.Content != "Reading file" {
		t.Errorf("Segment.Content = %q, want %q", s.Content, "Reading file")
	}
	if s.Tool != "Read" {
		t.Errorf("Segment.Tool = %q, want %q", s.Tool, "Read")
	}
	if s.Detail != "/path/to/file.go" {
		t.Errorf("Segment.Detail = %q, want %q", s.Detail, "/path/to/file.go")
	}
}
