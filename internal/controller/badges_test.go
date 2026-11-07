package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

func TestApplyBadges_SetsBadgesFromRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"1.2.3"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	agents := []agent.Agent{{PID: 1, WorkingDir: dir}, {PID: 2}}
	ApplyBadges(agents, []config.BadgeRule{{Path: "package.json", JSONPath: "version", Label: "v", Color: "#fff"}})

	if len(agents[0].Badges) != 1 {
		t.Fatalf("badges = %+v", agents[0].Badges)
	}
	if b := agents[0].Badges[0]; b.Label != "v" || b.Value != "1.2.3" || b.Color != "#fff" {
		t.Errorf("badge = %+v", b)
	}
	if agents[1].Badges != nil {
		t.Errorf("agent without working dir got badges %+v", agents[1].Badges)
	}
}

func TestApplyBadges_NoRulesLeavesAgentsUntouched(t *testing.T) {
	prev := []agent.BadgeValue{{Label: "old"}}
	agents := []agent.Agent{{WorkingDir: t.TempDir(), Badges: prev}}
	ApplyBadges(agents, nil)
	if len(agents[0].Badges) != 1 || agents[0].Badges[0].Label != "old" {
		t.Errorf("badges changed: %+v", agents[0].Badges)
	}
}

func TestApplyBadges_MissingFileGivesEmptyNonNil(t *testing.T) {
	agents := []agent.Agent{{WorkingDir: t.TempDir()}}
	ApplyBadges(agents, []config.BadgeRule{{Path: "nope.json", Label: "x"}})
	if agents[0].Badges == nil || len(agents[0].Badges) != 0 {
		t.Errorf("badges = %#v, want empty non-nil", agents[0].Badges)
	}
}
