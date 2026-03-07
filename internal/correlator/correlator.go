package correlator

import (
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
)

// ParentPIDFunc returns the parent PID for a given PID.
type ParentPIDFunc func(pid int) int

// TagFromProcessTree walks the PID ancestry of each agent.
// If an agent's ancestor PID matches another agent's PID,
// it's tagged as a subagent with ParentPID set.
// Used for tmux-based Agent Teams where subagents have their own PIDs.
func TagFromProcessTree(agents []agent.Agent, getParentPID ParentPIDFunc) {
	if len(agents) <= 1 {
		return
	}
	pidToIdx := make(map[int]int, len(agents))
	for i, a := range agents {
		pidToIdx[a.PID] = i
	}
	for i := range agents {
		parentPID := findAncestorInSet(agents[i].PID, pidToIdx, getParentPID)
		if parentPID > 0 && parentPID != agents[i].PID {
			agents[i].ParentPID = parentPID
		}
	}
}

func findAncestorInSet(pid int, pidSet map[int]int, getParentPID ParentPIDFunc) int {
	cur := pid
	seen := map[int]bool{pid: true}
	for i := 0; i < 5; i++ {
		ppid := getParentPID(cur)
		if ppid <= 1 || seen[ppid] {
			return 0
		}
		if _, ok := pidSet[ppid]; ok {
			return ppid
		}
		seen[ppid] = true
		cur = ppid
	}
	return 0
}

// OTELLookup is the minimal interface the correlator needs from the OTEL store.
type OTELLookup interface {
	SubagentInfoBySession(sessionID string) subagent.Info
	SubagentsBySession(sessionID string) []subagent.Info
}

// EnrichFromOTEL fills in subagent labels and creates virtual agent entries
// for in-process subagents that don't have their own PID.
//
// For each parent agent with a SessionID, it queries the OTEL store for
// distinct agent_id values. If a subagent ID doesn't already exist as a
// process-tree-tagged agent, a virtual entry is created with ParentPID
// set to the parent's PID.
func EnrichFromOTEL(agents []agent.Agent, store OTELLookup) []agent.Agent {
	if store == nil {
		return agents
	}

	// Index existing agents by subagent ID to avoid duplicates
	// (process-tree agents that already have OTEL identity).
	existingBySubID := make(map[string]bool)
	for _, a := range agents {
		if a.Subagent.ID != "" {
			existingBySubID[a.Subagent.ID] = true
		}
	}

	var virtualAgents []agent.Agent

	for i := range agents {
		if agents[i].SessionID == "" || agents[i].IsSubagent() {
			continue
		}

		// Enrich the parent agent's own subagent info
		info := store.SubagentInfoBySession(agents[i].SessionID)
		if info.HasIdentity() {
			agents[i].Subagent = info
		}

		// Create virtual entries for in-process subagents
		subs := store.SubagentsBySession(agents[i].SessionID)
		for _, sub := range subs {
			if existingBySubID[sub.ID] {
				continue // already exists from process tree
			}
			virtualAgents = append(virtualAgents, agent.Agent{
				Name:         sub.Type,
				ProviderName: agents[i].ProviderName,
				Model:        agents[i].Model,
				WorkingDir:   agents[i].WorkingDir,
				ParentPID:    agents[i].PID,
				Subagent:     sub,
				Status:       agent.StatusActive,
				LastActivity: agents[i].LastActivity,
				SessionID:    agents[i].SessionID,
			})
			existingBySubID[sub.ID] = true
		}
	}

	if len(virtualAgents) > 0 {
		agents = append(agents, virtualAgents...)
	}
	return agents
}
