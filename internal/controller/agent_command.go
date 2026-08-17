package controller

import "github.com/google/uuid"

// RemoteAgentCommand builds the shell command that starts the agent inside the
// sandbox. For Claude, the session id is pinned so telemetry and conversation
// stay continuous across reconnects.
func RemoteAgentCommand(provider, sessionID string, resume bool) string {
	if provider == "claude" && UUIDValid(sessionID) {
		if resume {
			return "claude --resume " + sessionID
		}
		return "claude --session-id " + sessionID
	}
	return provider
}

func UUIDValid(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
