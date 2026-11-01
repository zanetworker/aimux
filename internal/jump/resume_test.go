package jump

import (
	"strings"
	"testing"
)

func TestBuildClaudeCommand(t *testing.T) {
	tests := []struct {
		name, id, dir, suffix string
		empty                 bool
	}{
		{name: "resume by id", id: "abc", dir: "/w", suffix: " --resume abc"},
		{name: "id without dir", id: "abc", suffix: " --resume abc"},
		{name: "continue by dir", dir: "/w", suffix: " --continue"},
		{name: "nothing", empty: true},
	}
	for _, tt := range tests {
		got := buildClaudeCommand(tt.id, tt.dir)
		if tt.empty {
			if got != "" {
				t.Errorf("%s: got %q, want empty", tt.name, got)
			}
			continue
		}
		if !strings.HasSuffix(got, tt.suffix) || !strings.Contains(got, "claude") {
			t.Errorf("%s: got %q, want claude...%s", tt.name, got, tt.suffix)
		}
	}
}

func TestResumeCmd(t *testing.T) {
	if ResumeCmd("", "") != nil {
		t.Fatal("no id and no dir must give nil")
	}
	c := ResumeCmd("abc", "/w")
	if c == nil || c.Dir != "/w" || len(c.Args) != 3 || c.Args[1] != "--resume" || c.Args[2] != "abc" {
		t.Fatalf("resume by id: %+v", c)
	}
	c = ResumeCmd("", "/w")
	if c == nil || c.Dir != "/w" || len(c.Args) != 2 || c.Args[1] != "--continue" {
		t.Fatalf("continue by dir: %+v", c)
	}
	c = ResumeCmd("abc", "")
	if c == nil || c.Dir != "" || c.Args[2] != "abc" {
		t.Fatalf("id without dir: %+v", c)
	}
}
