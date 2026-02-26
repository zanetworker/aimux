package discovery

import (
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/agentmux/internal/cost"
	"github.com/zanetworker/agentmux/internal/model"
)

// Orchestrator coordinates all discovery sources to produce enriched instances.
type Orchestrator struct {
	projectsDir string
	teamsDir    string
}

// NewOrchestrator creates an orchestrator with default paths.
func NewOrchestrator() *Orchestrator {
	home, _ := os.UserHomeDir()
	return &Orchestrator{
		projectsDir: filepath.Join(home, ".claude", "projects"),
		teamsDir:    filepath.Join(home, ".claude", "teams"),
	}
}

// Discover finds all Claude instances and enriches them with session and tmux data.
func (o *Orchestrator) Discover() ([]model.Instance, error) {
	instances, err := ScanProcesses()
	if err != nil {
		return nil, err
	}

	tmuxSessions := ListTmuxSessions()

	for i := range instances {
		o.enrichInstance(&instances[i], tmuxSessions)
	}
	return instances, nil
}

func (o *Orchestrator) enrichInstance(inst *model.Instance, tmuxSessions []tmuxSession) {
	// Resolve working directory
	if inst.WorkingDir == "" {
		cwd, err := getProcessCwd(inst.PID)
		if err == nil {
			inst.WorkingDir = cwd
		}
	}

	// Match tmux session
	if inst.WorkingDir != "" {
		inst.TMuxSession = matchTmuxSession(tmuxSessions, inst.WorkingDir)
	}

	// Find and parse session JSONL
	sessionFile := ""
	if inst.SessionID != "" {
		sessionFile = findSessionFile(inst.SessionID, o.projectsDir)
	}
	if sessionFile == "" && inst.WorkingDir != "" {
		files := SessionFilesForDir(inst.WorkingDir)
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
		info, err := ParseSessionFile(sessionFile)
		if err == nil {
			if inst.SessionID == "" {
				inst.SessionID = info.SessionID
			}
			inst.GitBranch = info.GitBranch
			inst.TokensIn = info.TokensIn
			inst.TokensOut = info.TokensOut
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
				inst.Status = model.StatusActive
			} else if !info.LastTimestamp.IsZero() {
				inst.Status = model.StatusIdle
			}
		}
	}
}
