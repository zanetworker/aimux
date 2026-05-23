package controller

import (
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// FilterAgents returns agents matching the query string. The match is
// case-insensitive and checks ShortProject, ShortModel, Status, Source,
// ProviderName, ShortDir, and LastAction. An empty query returns all agents.
func FilterAgents(agents []agent.Agent, query string) []agent.Agent {
	if query == "" {
		return agents
	}
	q := strings.ToLower(query)
	var out []agent.Agent
	for _, a := range agents {
		if strings.Contains(strings.ToLower(a.ShortProject()), q) ||
			strings.Contains(strings.ToLower(a.ShortModel()), q) ||
			strings.Contains(strings.ToLower(a.Status.String()), q) ||
			strings.Contains(strings.ToLower(a.Source.String()), q) ||
			strings.Contains(strings.ToLower(a.ProviderName), q) ||
			strings.Contains(strings.ToLower(a.ShortDir()), q) ||
			strings.Contains(strings.ToLower(a.LastAction), q) {
			out = append(out, a)
		}
	}
	return out
}
