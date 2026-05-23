package controller

import (
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

// Notification describes a notification that should be delivered by the caller.
// The controller decides whether to notify; the UI layer decides how.
type Notification struct {
	Title   string
	Message string
	Sound   bool
}

// ShouldNotify returns a Notification if the agent's current status warrants
// one, or nil if no notification should fire. The caller is responsible for
// delivering the notification (e.g., macOS notification center, SSE event).
func ShouldNotify(status agent.Status, projectName string, cfg config.NotificationsConfig) *Notification {
	if !cfg.Enabled {
		return nil
	}
	title := "aimux: " + projectName

	switch status {
	case agent.StatusWaitingPermission:
		if !cfg.OnWaiting {
			return nil
		}
		return &Notification{Title: title, Message: "Needs permission", Sound: cfg.Sound}
	case agent.StatusError:
		if !cfg.OnError {
			return nil
		}
		return &Notification{Title: title, Message: "Agent error", Sound: cfg.Sound}
	case agent.StatusIdle:
		if !cfg.OnIdle {
			return nil
		}
		return &Notification{Title: title, Message: "Finished", Sound: false}
	default:
		return nil
	}
}
