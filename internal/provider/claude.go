package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/zanetworker/agentmux/internal/agent"
	"github.com/zanetworker/agentmux/internal/cost"
	"github.com/zanetworker/agentmux/internal/discovery"
)

// Claude is a Provider implementation for the Claude Code CLI.
type Claude struct{}

func (c *Claude) Name() string { return "claude" }

// Discover finds all running Claude Code processes, then enriches each agent
// with CWD, tmux session, session JSONL data, and estimated cost. SDK-spawned
// agents sharing the same directory and model are grouped into single entries.
func (c *Claude) Discover() ([]agent.Agent, error) {
	agents, err := discovery.ScanProcesses()
	if err != nil {
		return nil, err
	}

	tmuxSessions := discovery.ListTmuxSessions()

	home, _ := os.UserHomeDir()
	projectsDir := filepath.Join(home, ".claude", "projects")

	for i := range agents {
		c.enrichAgent(&agents[i], tmuxSessions, projectsDir)
		agents[i].ProviderName = "claude"
		if agents[i].Name == "" {
			agents[i].Name = agents[i].ShortProject()
		}
	}

	// Deduplicate: group SDK agents by (WorkingDir, Model), dedup CLI by SessionFile
	agents = deduplicateAgents(agents)

	return agents, nil
}

// deduplicateAgents groups SDK agents sharing the same (WorkingDir, Model) into
// single entries with a GroupCount, and deduplicates CLI agents that share the
// same SessionFile (same conversation, multiple processes).
func deduplicateAgents(agents []agent.Agent) []agent.Agent {
	type groupKey struct {
		WorkingDir  string
		Model       string
		Source      agent.SourceType
		SessionFile string
	}

	groups := make(map[string]*agent.Agent)
	order := make([]string, 0) // preserve discovery order

	for i := range agents {
		a := &agents[i]
		var key string

		switch a.Source {
		case agent.SourceSDK:
			// Group SDK agents by (WorkingDir, Model)
			key = fmt.Sprintf("sdk:%s:%s", a.WorkingDir, a.Model)
		case agent.SourceVSCode:
			// Group VSCode agents by (WorkingDir, Model)
			key = fmt.Sprintf("vsc:%s:%s", a.WorkingDir, a.Model)
		default:
			// CLI agents: dedup by SessionFile if available, otherwise by PID
			if a.SessionFile != "" {
				key = fmt.Sprintf("cli:sf:%s", a.SessionFile)
			} else {
				key = fmt.Sprintf("cli:pid:%d", a.PID)
			}
		}

		if existing, ok := groups[key]; ok {
			// Merge into existing: keep the more active one, accumulate count
			existing.GroupCount++
			existing.GroupPIDs = append(existing.GroupPIDs, a.PID)
			// Keep the one with more recent activity
			if a.LastActivity.After(existing.LastActivity) {
				pid := existing.PID
				gpids := existing.GroupPIDs
				gc := existing.GroupCount
				*existing = *a
				existing.GroupPIDs = append([]int{pid}, gpids...)
				existing.GroupCount = gc
			}
		} else {
			copy := *a
			copy.GroupCount = 1
			copy.GroupPIDs = []int{a.PID}
			groups[key] = &copy
			order = append(order, key)
		}
	}

	result := make([]agent.Agent, 0, len(groups))
	for _, key := range order {
		result = append(result, *groups[key])
	}
	return result
}

// enrichAgent resolves the working directory, matches a tmux session,
// parses the session JSONL file, and calculates the estimated cost.
func (c *Claude) enrichAgent(inst *agent.Agent, tmuxSessions []discovery.TmuxSession, projectsDir string) {
	// Resolve working directory
	if inst.WorkingDir == "" {
		cwd, err := discovery.GetProcessCwd(inst.PID)
		if err == nil {
			inst.WorkingDir = cwd
		}
	}

	// Match tmux session
	if inst.WorkingDir != "" {
		inst.TMuxSession = discovery.MatchTmuxSession(tmuxSessions, inst.WorkingDir)
	}

	// Find and parse session JSONL
	sessionFile := ""
	if inst.SessionID != "" {
		sessionFile = discovery.FindSessionFile(inst.SessionID, projectsDir)
	}
	if sessionFile == "" && inst.WorkingDir != "" {
		files := discovery.SessionFilesForDir(inst.WorkingDir)
		if len(files) > 0 {
			// Use the most recently modified file
			var newest string
			var newestTime time.Time
			for _, f := range files {
				info, err := os.Stat(f)
				if err == nil && info.ModTime().After(newestTime) {
					newest = f
					newestTime = info.ModTime()
				}
			}
			sessionFile = newest
		}
	}

	if sessionFile != "" {
		inst.SessionFile = sessionFile
		info, err := discovery.ParseSessionFile(sessionFile)
		if err == nil {
			if inst.SessionID == "" {
				inst.SessionID = info.SessionID
			}
			inst.GitBranch = info.GitBranch
			if inst.Model == "" && info.Model != "" {
				inst.Model = info.Model
			}
			inst.TokensIn = info.TokensIn
			inst.TokensOut = info.TokensOut
			inst.LastAction = info.LastAction
			inst.LastActivity = info.LastTimestamp
			inst.EstCostUSD = cost.Calculate(
				inst.Model,
				info.TokensIn,
				info.TokensOut,
				info.CacheReadTokens,
				info.CacheWriteTokens,
			)

			// Determine status from activity
			if time.Since(info.LastTimestamp) < 30*time.Second {
				inst.Status = agent.StatusActive
			} else if !info.LastTimestamp.IsZero() {
				inst.Status = agent.StatusIdle
			}
		}
	}
}

func (c *Claude) ResumeCommand(a agent.Agent) *exec.Cmd {
	bin := findBinary("claude")
	var cmd *exec.Cmd
	if a.SessionID != "" {
		cmd = exec.Command(bin, "--resume", a.SessionID)
	} else if a.WorkingDir != "" {
		cmd = exec.Command(bin, "--continue")
	} else {
		return nil
	}
	if a.WorkingDir != "" {
		cmd.Dir = a.WorkingDir
	}
	return cmd
}

func (c *Claude) ParseConversation(sessionPath string) ([]Segment, error) {
	return nil, nil // Will be implemented when we refactor logs view
}

func findBinary(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}
