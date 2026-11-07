package controller

import (
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/badge"
	"github.com/zanetworker/aimux/internal/config"
)

// ApplyBadges evaluates the configured badge rules against each agent's
// working directory and sets agent.Badges in place. With no rules the agents
// are left untouched; an agent with no working directory keeps its badges.
// The TUI agent refresh and the dashboard stream both call it.
func ApplyBadges(agents []agent.Agent, rules []config.BadgeRule) {
	if len(rules) == 0 {
		return
	}
	br := make([]badge.Rule, len(rules))
	for i, r := range rules {
		br[i] = badge.Rule{Path: r.Path, JSONPath: r.JSONPath, Label: r.Label, Color: r.Color}
	}
	for i := range agents {
		a := &agents[i]
		if a.WorkingDir == "" {
			continue
		}
		badges := badge.Evaluate(a.WorkingDir, br)
		values := make([]agent.BadgeValue, 0, len(badges))
		for _, b := range badges {
			values = append(values, agent.BadgeValue{Label: b.Label, Value: b.Value, Color: b.Color})
		}
		a.Badges = values
	}
}
