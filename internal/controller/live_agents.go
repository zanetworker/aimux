package controller

import (
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

// ephemeralMaxAge is how long a cheap agent may run and still count as an
// ephemeral automation subagent (session analyzers, hooks).
const ephemeralMaxAge = 5 * time.Minute

// DropEphemeral removes ephemeral automation subagents: local agents with no
// cost, fewer than 1000 input tokens, a non-opus model, not launched by aimux
// (tmux session "aimux-..."), started less than 5 minutes before now. Remote
// sandbox agents are always kept (they carry no cost or token data by design).
// The dashboard agent stream uses it; the TUI shows every discovered agent.
func DropEphemeral(agents []agent.Agent, now time.Time) []agent.Agent {
	var out []agent.Agent
	for _, a := range agents {
		if a.Location != "remote" &&
			a.EstCostUSD == 0 && a.TokensIn < 1000 &&
			!strings.Contains(a.Model, "opus") &&
			!strings.HasPrefix(a.TMuxSession, "aimux-") &&
			now.Sub(a.StartTime) < ephemeralMaxAge {
			continue
		}
		out = append(out, a)
	}
	return out
}

// DedupBySession keeps one agent per session id, the earliest started, in the
// position of the first agent seen for that id. Agents with no session id are
// all kept. The dashboard agent stream uses it.
func DedupBySession(agents []agent.Agent) []agent.Agent {
	seen := make(map[string]int)
	var out []agent.Agent
	for _, a := range agents {
		if a.SessionID == "" {
			out = append(out, a)
			continue
		}
		if idx, ok := seen[a.SessionID]; ok {
			if a.StartTime.Before(out[idx].StartTime) {
				out[idx] = a
			}
			continue
		}
		seen[a.SessionID] = len(out)
		out = append(out, a)
	}
	return out
}
