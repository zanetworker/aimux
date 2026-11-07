package insight

// DashboardPrompt is the prompt for the skill analytics dashboard's insight:
// the caller's own prompt when given, else the default instructions followed
// by the dashboard data. data is ignored when custom is given.
func DashboardPrompt(custom string, data []byte) string {
	if custom != "" {
		return custom
	}
	return "You are analyzing a skill analytics dashboard for an AI coding agent system. " +
		"The data below shows skill invocations, correction rates, learning funnel metrics, and failure patterns. " +
		"For each section with data, provide 1-2 sentences of actionable insight. " +
		"Focus on: which skills need attention, what patterns indicate, and specific next steps. " +
		"Format as a JSON object where keys are section names and values are insight strings.\n\n" +
		"Dashboard data:\n" + string(data)
}
