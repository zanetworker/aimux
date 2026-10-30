package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/terminput"
)

func TestNamingConfig_FromSessionsConfig(t *testing.T) {
	home := t.TempDir()
	lookup := func(string) (history.Session, bool) { return history.Session{}, false }
	cfg := namingConfig(config.SessionsConfig{TitleModel: "sonnet", NamingDailyLimit: 7, APIKey: "sk-test-not-real"}, home, lookup, nil)
	if cfg.Model != "sonnet" || cfg.DailyLimit != 7 || cfg.Lookup == nil || cfg.Primary == nil {
		t.Fatalf("config %+v, want model, limit, lookup and a backend", cfg)
	}
	if want := filepath.Join(home, ".aimux", "naming-state.json"); cfg.StatePath != want {
		t.Fatalf("state path %q, want %q", cfg.StatePath, want)
	}
	if cfg.Fallback == nil {
		t.Fatal("with an API key, want claude -p as the fallback")
	}
}

// fakeFocusDriver is a terminal driver that reports focus.
type fakeFocusDriver struct{ terminput.Driver }

func (fakeFocusDriver) Focused(context.Context) (bool, string, error) {
	return true, "/dev/ttys001", nil
}

// plainDriver is a terminal driver that cannot report focus.
type plainDriver struct{ terminput.Driver }

func TestBuildLastFocus(t *testing.T) {
	pids := func() map[string]int { return nil }
	on := config.TerminalInputConfig{Enabled: true}
	if lf := buildLastFocus(config.TerminalInputConfig{}, pids, fakeFocusDriver{}); lf != nil {
		t.Fatal("terminal input off: want no Last on your Mac")
	}
	if lf := buildLastFocus(on, pids); lf != nil {
		t.Fatal("no driver: want no Last on your Mac")
	}
	if lf := buildLastFocus(on, pids, plainDriver{}); lf != nil {
		t.Fatal("a driver without a focus probe: want no Last on your Mac")
	}
	lf := buildLastFocus(on, pids, plainDriver{}, fakeFocusDriver{})
	if lf == nil || !lf.Enabled || lf.Focus == nil || lf.Sessions == nil || lf.Sys == nil || lf.Now == nil {
		t.Fatalf("Last on your Mac %+v, want it wired to the focus driver", lf)
	}
}
