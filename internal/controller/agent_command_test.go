package controller

import "testing"

func TestRemoteAgentCommand(t *testing.T) {
	const validUUID = "2a38ec78-566d-4f80-8682-e63ae75ea1f1"
	tests := []struct {
		name     string
		provider string
		session  string
		resume   bool
		want     string
	}{
		{"claude launch pins session id", "claude", validUUID, false, "claude --session-id " + validUUID},
		{"claude re-entry resumes session", "claude", validUUID, true, "claude --resume " + validUUID},
		{"claude without uuid falls back", "claude", "", false, "claude"},
		{"claude with non-uuid falls back", "claude", "aimux-remote-claude-123", true, "claude"},
		{"non-claude provider is bare", "codex", validUUID, false, "codex"},
		{"gemini provider is bare", "gemini", validUUID, true, "gemini"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemoteAgentCommand(tt.provider, tt.session, tt.resume); got != tt.want {
				t.Errorf("RemoteAgentCommand(%q,%q,%v) = %q, want %q",
					tt.provider, tt.session, tt.resume, got, tt.want)
			}
		})
	}
}
