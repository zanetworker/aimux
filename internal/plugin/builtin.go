package plugin

var builtins = []Plugin{
	{
		Name:      "skill-dashboard",
		Tab:       "Skill Dashboard",
		Command:   "python3.11 ~/.claude/scripts/skill-dashboard.py --format json",
		CacheSecs: 30,
		AutoDetect: []string{
			"~/.claude/skill-usage.jsonl",
			"~/.claude/skill-effectiveness.jsonl",
		},
		BuiltIn: true,
		Panels: []Panel{
			{ID: "metrics", Type: PanelMetricRow, Title: "Overview", Description: "CL-tracked = skill invocations observed by continuous-learning. Hook Log = raw counts from PreToolUse hook (ground truth). Corrections = times user corrected Claude while a skill was active."},
			{ID: "health", Type: PanelTable, Title: "Skill Health", Description: "Per-skill effectiveness. Correction Rate = corrections / invocations. Green <15% (healthy), orange 16-30% (review), red >30% (needs fixing).", Sortable: true},
			{ID: "actions", Type: PanelList, Title: "Actions Needed", Description: "Skills with high correction rates that need review. Low-sample warnings are flagged separately.", Expandable: true},
			{ID: "top-skills", Type: PanelBarChart, Title: "Top Skills", Description: "Teal = invocations, purple = corrections attributed to that skill. Large purple relative to teal = first candidate for improvement.", Width: "half"},
			{ID: "triggers", Type: PanelBarChart, Title: "Trigger Breakdown", Description: "Auto-triggered (from CLAUDE.md trigger table) vs user-triggered (typed /skill).", Width: "half"},
			{ID: "funnel", Type: PanelTable, Title: "Continuous Learning Funnel", Description: "Triggered > Qualified (enough messages to analyze) > Proposals (Sonnet found patterns) > Saved (written to ~/.claude/pending-learnings/)."},
			{ID: "by-project", Type: PanelTable, Title: "By Project", Description: "Skill invocations by project from hook log.", Width: "half"},
			{ID: "proposals", Type: PanelBarChart, Title: "Proposals by Category", Description: "What types of learnings are being captured (user_correction, tool_usage, error_resolution, etc.).", Width: "half"},
			{ID: "timeline", Type: PanelTable, Title: "Timeline", Description: "Daily session triggers vs learnings saved. Trigger spikes without saves = routine work. Both flat = hook may be broken."},
			{ID: "combos", Type: PanelList, Title: "Skill Combos", Description: "Skills frequently used together. Approved combos are published to CLAUDE.md via /sync-plugins. Pending combos need /review-learnings.", Expandable: true},
			{ID: "invocations", Type: PanelTable, Title: "Recent Invocations", Description: "Last 30 skill invocations from the PreToolUse hook log.", Sortable: true},
			{ID: "clusters", Type: PanelList, Title: "Cluster Health", Description: "Known failure patterns and how often they recur. >30% = too broad or weak, 15-30% = monitor, <15% = under control.", Expandable: true},
			{ID: "pending", Type: PanelList, Title: "Pending Learnings", Description: "Learnings extracted from sessions awaiting review. Run /review-learnings to process. Stale queue = knowledge stuck in limbo.", Expandable: true},
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
