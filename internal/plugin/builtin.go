package plugin

var builtins = []Plugin{
	{
		Name:      "skill-dashboard",
		Tab:       "Skills",
		Command:   "python3 ~/.claude/scripts/skill-dashboard.py --format json",
		CacheSecs: 30,
		AutoDetect: []string{
			"~/.claude/skill-usage.jsonl",
			"~/.claude/skill-effectiveness.jsonl",
		},
		BuiltIn: true,
		Panels: []Panel{
			{ID: "metrics", Type: PanelMetricRow, Title: "Overview", Description: "Total skill invocations, correction rate (>30% needs fixing, 16-30% review, <15% healthy), pending learnings awaiting review, and skills registered but never triggered."},
			{ID: "health", Type: PanelTable, Title: "Skill Health", Description: "Per-skill effectiveness. Rate = corrections / invocations. Green <15%, orange 16-30%, red >30%. Sort by any column.", Sortable: true},
			{ID: "top-skills", Type: PanelBarChart, Title: "Top Skills", Description: "Most frequently invoked skills.", Width: "half"},
			{ID: "triggers", Type: PanelBarChart, Title: "Trigger Breakdown", Description: "Auto-triggered (from CLAUDE.md table) vs user-triggered (typed /skill).", Width: "half"},
			{ID: "funnel", Type: PanelMetricRow, Title: "Continuous Learning Funnel", Description: "SessionEnd hook pipeline: triggers (hook fired) > qualified (enough messages) > proposals (learnings found) > saved (written to pending)."},
			{ID: "proposals", Type: PanelBarChart, Title: "Proposals by Category", Description: "What types of learnings are being captured.", Width: "half"},
			{ID: "combos", Type: PanelList, Title: "Skill Combos", Description: "Skills frequently used together in the same session.", Width: "half"},
			{ID: "invocations", Type: PanelTable, Title: "Recent Invocations", Description: "Last 30 skill invocations from the PreToolUse hook log.", Sortable: true},
			{ID: "pending", Type: PanelList, Title: "Pending Learnings", Description: "Learnings extracted from sessions awaiting review. Use /review-learnings to approve or reject.", Expandable: true},
			{ID: "never-triggered", Type: PanelList, Title: "Never Triggered", Description: "Skills in the trigger table with zero observations. Either the description needs rewriting or the skill should be removed."},
		},
	},
}

func Builtins() []Plugin {
	var result []Plugin
	for _, p := range builtins {
		if autoDetect(p.AutoDetect) {
			result = append(result, p)
		}
	}
	return result
}

func autoDetect(paths []string) bool {
	for _, p := range paths {
		expanded := expandHome(p)
		if fileExists(expanded) {
			return true
		}
	}
	return false
}
