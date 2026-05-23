package controller

import (
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// SortAgents sorts agents in place using sort.SliceStable.
// Supported fields: "", "name", "cost", "cpu", "mem", "age", "model".
// The default ("") sorts by status priority (active first) then name alphabetically.
func SortAgents(agents []agent.Agent, field string) {
	switch field {
	case "name":
		sort.SliceStable(agents, func(i, j int) bool {
			return strings.ToLower(agents[i].ShortProject()) < strings.ToLower(agents[j].ShortProject())
		})
	case "cost":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].EstCostUSD > agents[j].EstCostUSD
		})
	case "cpu":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].CPUPercent > agents[j].CPUPercent
		})
	case "mem":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].MemoryMB > agents[j].MemoryMB
		})
	case "age":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].AgeTime().Before(agents[j].AgeTime())
		})
	case "model":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].ShortModel() < agents[j].ShortModel()
		})
	default:
		// Default: status priority (Active=0 < Idle=1 < Waiting=2 < Error=3 < Unknown=4), then name.
		sort.SliceStable(agents, func(i, j int) bool {
			si, sj := agents[i].Status, agents[j].Status
			if si != sj {
				return si < sj
			}
			return strings.ToLower(agents[i].ShortProject()) < strings.ToLower(agents[j].ShortProject())
		})
	}
}
