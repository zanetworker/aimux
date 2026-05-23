package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

func TestShouldNotify_WaitingEnabled(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled:   true,
		OnWaiting: true,
		Sound:     false,
	}
	n := ShouldNotify(agent.StatusWaitingPermission, "myproject", cfg)
	if n == nil {
		t.Fatal("expected notification, got nil")
	}
	if n.Title != "aimux: myproject" {
		t.Errorf("title = %q, want %q", n.Title, "aimux: myproject")
	}
	if n.Message != "Needs permission" {
		t.Errorf("message = %q, want %q", n.Message, "Needs permission")
	}
	if n.Sound {
		t.Error("sound should be false when cfg.Sound is false")
	}
}

func TestShouldNotify_WaitingDisabled(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled:   true,
		OnWaiting: false,
	}
	n := ShouldNotify(agent.StatusWaitingPermission, "myproject", cfg)
	if n != nil {
		t.Errorf("expected nil, got %+v", n)
	}
}

func TestShouldNotify_MasterDisabled(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled:   false,
		OnWaiting: true,
		OnError:   true,
		OnIdle:    true,
	}
	for _, status := range []agent.Status{
		agent.StatusWaitingPermission,
		agent.StatusError,
		agent.StatusIdle,
	} {
		n := ShouldNotify(status, "proj", cfg)
		if n != nil {
			t.Errorf("status %v: expected nil when master disabled, got %+v", status, n)
		}
	}
}

func TestShouldNotify_ActiveReturnsNil(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled:   true,
		OnWaiting: true,
		OnError:   true,
		OnIdle:    true,
	}
	n := ShouldNotify(agent.StatusActive, "proj", cfg)
	if n != nil {
		t.Errorf("expected nil for Active status, got %+v", n)
	}
}

func TestShouldNotify_ErrorWithSound(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled: true,
		OnError: true,
		Sound:   true,
	}
	n := ShouldNotify(agent.StatusError, "crashy", cfg)
	if n == nil {
		t.Fatal("expected notification, got nil")
	}
	if n.Title != "aimux: crashy" {
		t.Errorf("title = %q, want %q", n.Title, "aimux: crashy")
	}
	if n.Message != "Agent error" {
		t.Errorf("message = %q, want %q", n.Message, "Agent error")
	}
	if !n.Sound {
		t.Error("sound should be true when cfg.Sound is true")
	}
}

func TestShouldNotify_IdleDisabled(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled: true,
		OnIdle:  false,
	}
	n := ShouldNotify(agent.StatusIdle, "proj", cfg)
	if n != nil {
		t.Errorf("expected nil when OnIdle is false, got %+v", n)
	}
}

func TestShouldNotify_IdleEnabled(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled: true,
		OnIdle:  true,
		Sound:   true, // Sound is true in config, but Idle never plays sound
	}
	n := ShouldNotify(agent.StatusIdle, "done-proj", cfg)
	if n == nil {
		t.Fatal("expected notification, got nil")
	}
	if n.Message != "Finished" {
		t.Errorf("message = %q, want %q", n.Message, "Finished")
	}
	if n.Sound {
		t.Error("idle notifications should never have sound, even when cfg.Sound is true")
	}
}

func TestShouldNotify_EmptyProjectName(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled:   true,
		OnWaiting: true,
	}
	n := ShouldNotify(agent.StatusWaitingPermission, "", cfg)
	if n == nil {
		t.Fatal("expected notification, got nil")
	}
	if n.Title != "aimux: " {
		t.Errorf("title = %q, want %q", n.Title, "aimux: ")
	}
}
