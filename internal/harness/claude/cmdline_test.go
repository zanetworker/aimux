package claude

import (
	"reflect"
	"testing"
)

func TestCommandLineSessions(t *testing.T) {
	tests := []struct {
		args string
		want []string
	}{
		{"/usr/local/bin/claude --dangerously-skip-permissions --resume idle-1", []string{"--dangerously-skip-permissions", "--resume", "idle-1"}},
		{"claude --session-id=s-2", []string{"--session-id=s-2", "s-2"}},
		{"claude", nil},
		{"vim notes-about-idle-1.md", nil},
		{"node /x/claude --resume idle-1", nil}, // only a claude binary counts
		{"", nil},
		{"claude a=b=c", []string{"a=b=c", "c"}},
	}
	for _, tt := range tests {
		if got := CommandLineSessions(tt.args); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CommandLineSessions(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}
