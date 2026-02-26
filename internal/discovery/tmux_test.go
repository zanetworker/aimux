package discovery

import (
	"testing"
)

func TestParseTmuxLine(t *testing.T) {
	tests := []struct {
		name         string
		line         string
		wantName     string
		wantAttached bool
	}{
		{
			"attached session",
			"claude-myproject: 1 windows (created Thu Feb 20 10:00:00 2026) (attached)",
			"claude-myproject",
			true,
		},
		{
			"detached session",
			"claude-other: 2 windows (created Thu Feb 20 11:00:00 2026)",
			"claude-other",
			false,
		},
		{
			"simple name",
			"main: 1 windows (created Thu Feb 20 12:00:00 2026)",
			"main",
			false,
		},
		{
			"empty line",
			"",
			"",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, attached := parseTmuxLine(tt.line)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if attached != tt.wantAttached {
				t.Errorf("attached = %v, want %v", attached, tt.wantAttached)
			}
		})
	}
}

func TestMatchTmuxSession(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "claude-myproject", Attached: true},
		{Name: "claude-other", Attached: false},
		{Name: "main", Attached: true},
	}

	tests := []struct {
		name       string
		workingDir string
		want       string
	}{
		{"matching project", "/home/user/myproject", "claude-myproject"},
		{"other project", "/home/user/other", "claude-other"},
		{"no match", "/home/user/unknown", ""},
		{"empty dir", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchTmuxSession(sessions, tt.workingDir)
			if got != tt.want {
				t.Errorf("MatchTmuxSession(%q) = %q, want %q", tt.workingDir, got, tt.want)
			}
		})
	}
}
