package notify

import (
	"testing"
)

func TestBuildAppleScript(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
		sound bool
		want  string
	}{
		{
			"without sound",
			"aimux: myapp",
			"Agent needs permission",
			false,
			`display notification "Agent needs permission" with title "aimux: myapp"`,
		},
		{
			"with sound",
			"aimux: myapp",
			"Agent crashed",
			true,
			`display notification "Agent crashed" with title "aimux: myapp" sound name "default"`,
		},
		{
			"escapes quotes",
			`title with "quotes"`,
			`body with "quotes"`,
			false,
			`display notification "body with \"quotes\"" with title "title with \"quotes\""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAppleScript(tt.title, tt.body, tt.sound)
			if got != tt.want {
				t.Errorf("buildAppleScript(%q, %q, %v)\n got: %s\nwant: %s",
					tt.title, tt.body, tt.sound, got, tt.want)
			}
		})
	}
}

func TestSendDoesNotPanic(t *testing.T) {
	// Verify no panic — actual osascript may not be available in CI
	Send("test", "body")
	SendWithSound("test", "body")
}
