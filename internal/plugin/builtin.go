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
			{ID: "metrics", Type: PanelMetricRow, Title: "Overview"},
			{ID: "health", Type: PanelTable, Title: "Skill Health", Sortable: true},
			{ID: "top-skills", Type: PanelBarChart, Title: "Top Skills", Width: "half"},
			{ID: "triggers", Type: PanelBarChart, Title: "Trigger Breakdown", Width: "half"},
			{ID: "funnel", Type: PanelMetricRow, Title: "Continuous Learning Funnel"},
			{ID: "proposals", Type: PanelBarChart, Title: "Proposals by Category", Width: "half"},
			{ID: "combos", Type: PanelList, Title: "Skill Combos", Width: "half"},
			{ID: "invocations", Type: PanelTable, Title: "Recent Invocations", Sortable: true},
			{ID: "pending", Type: PanelList, Title: "Pending Learnings", Expandable: true},
			{ID: "never-triggered", Type: PanelList, Title: "Never Triggered"},
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
