package controller

import (
	"sort"

	"github.com/zanetworker/aimux/internal/agent"
)

// ProjectCost is the token and cost total of the agents in one project.
type ProjectCost struct {
	Project    string  `json:"project"`
	Provider   string  `json:"provider"` // of the first agent seen; may be empty
	Model      string  `json:"model"`    // short model of the first agent seen
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CostUSD    float64 `json:"cost"`
	AgentCount int     `json:"agent_count"`
}

// CostsByProject groups agents by short project name ("(unknown)" when an
// agent has none) and returns the groups most expensive first, equal costs by
// project name. The TUI cost view and GET /api/costs both call it.
func CostsByProject(agents []agent.Agent) []ProjectCost {
	groups := make(map[string]*ProjectCost)
	for _, a := range agents {
		proj := a.ShortProject()
		if proj == "" {
			proj = "(unknown)"
		}
		e, ok := groups[proj]
		if !ok {
			e = &ProjectCost{Project: proj, Provider: a.ProviderName, Model: a.ShortModel()}
			groups[proj] = e
		}
		e.TokensIn += a.TokensIn
		e.TokensOut += a.TokensOut
		e.CostUSD += a.EstCostUSD
		e.AgentCount++
	}
	out := make([]ProjectCost, 0, len(groups))
	for _, e := range groups {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Project < out[j].Project
	})
	return out
}
