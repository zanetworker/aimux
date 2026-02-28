package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// discoverRecentSessions finds idle Claude sessions — one per project directory,
// using the newest session file from the last 24 hours. Only shows projects that
// don't already have a running process.
func (c *Claude) discoverRecentSessions(running []agent.Agent, projectsDir string) []agent.Agent {
	cutoff := time.Now().Add(-24 * time.Hour)

	// Collect all identifiers from running agents for dedup
	runningIDs := make(map[string]bool)
	runningDirs := make(map[string]bool)
	runningFiles := make(map[string]bool)
	for _, a := range running {
		if a.SessionID != "" {
			runningIDs[a.SessionID] = true
		}
		if a.WorkingDir != "" {
			runningDirs[a.WorkingDir] = true
		}
		if a.SessionFile != "" {
			runningFiles[a.SessionFile] = true
		}
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}

	var agents []agent.Agent

	for _, dirEntry := range entries {
		if !dirEntry.IsDir() {
			continue
		}
		dirKey := dirEntry.Name()

		// Skip projects that already have a running agent
		derivedDir := decodeDirKey(dirKey)
		if derivedDir != "" && runningDirs[derivedDir] {
			continue
		}

		// Find the single newest JSONL file in this project dir
		subdir := filepath.Join(projectsDir, dirKey)
		newestPath, newestMod := newestJSONL(subdir, cutoff)
		if newestPath == "" {
			continue
		}

		// Skip if this exact file is already used by a running agent
		if runningFiles[newestPath] {
			continue
		}

		info, err := discovery.ParseSessionFile(newestPath)
		if err != nil {
			continue
		}

		// Skip if session ID matches a running agent
		if info.SessionID != "" && runningIDs[info.SessionID] {
			continue
		}

		a := agent.Agent{
			PID:          0,
			SessionID:    info.SessionID,
			ProviderName: "claude",
			SessionFile:  newestPath,
			Status:       agent.StatusIdle,
			Source:       agent.SourceCLI,
			GroupCount:   1,
			GroupPIDs:    []int{},
			Model:        info.Model,
			TokensIn:     info.TokensIn,
			TokensOut:    info.TokensOut,
			LastAction:   info.LastAction,
			GitBranch:    info.GitBranch,
		}

		a.EstCostUSD = cost.Calculate(
			info.Model,
			info.TokensIn,
			info.TokensOut,
			info.CacheReadTokens,
			info.CacheWriteTokens,
		)

		if derivedDir != "" {
			a.WorkingDir = derivedDir
			a.Name = filepath.Base(derivedDir)
		} else {
			a.Name = dirKey
		}

		if !info.LastTimestamp.IsZero() {
			a.LastActivity = info.LastTimestamp
			a.StartTime = info.LastTimestamp
		} else {
			a.LastActivity = newestMod
			a.StartTime = newestMod
		}

		agents = append(agents, a)
	}

	return agents
}

// newestJSONL returns the path and mod time of the most recently modified
// .jsonl file in dir that was modified after cutoff. Returns ("", zero) if none.
func newestJSONL(dir string, cutoff time.Time) (string, time.Time) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}
	}
	var bestPath string
	var bestMod time.Time
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		info, err := f.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			continue
		}
		if info.ModTime().After(bestMod) {
			bestPath = filepath.Join(dir, f.Name())
			bestMod = info.ModTime()
		}
	}
	return bestPath, bestMod
}

// decodeDirKey attempts to reverse Claude's dir-key encoding. Claude encodes
// working directory paths by replacing "/" and "." with "-". The result has a
// leading "-" (from the leading "/"). This decoding is best-effort because the
// encoding is lossy (hyphens in original path are indistinguishable from
// replaced separators). Returns "" if the key doesn't look like an encoded path.
func decodeDirKey(key string) string {
	if !strings.HasPrefix(key, "-") {
		return ""
	}
	// Replace the leading "-" with "/" and each remaining "-" with "/".
	// This produces a path like "/Users/foo/project" from
	// "-Users-foo-project". It won't be exact when the original path
	// contained hyphens or dots, but it's sufficient for matching against
	// running agents' WorkingDir.
	return strings.ReplaceAll(key, "-", "/")
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

// CanEmbed returns true because Claude's TUI works inside an embedded PTY.
func (c *Claude) CanEmbed() bool { return true }

// FindSessionFile resolves the session/trace file for a Claude agent.
// It first tries the session ID lookup, then falls back to finding the
// newest JSONL in the agent's working directory.
func (c *Claude) FindSessionFile(a agent.Agent) string {
	if a.SessionID != "" {
		if sf := discovery.FindSessionFileDefault(a.SessionID); sf != "" {
			return sf
		}
	}
	if a.WorkingDir != "" {
		files := discovery.SessionFilesForDir(a.WorkingDir)
		if len(files) > 0 {
			var newest string
			var newestTime time.Time
			for _, f := range files {
				info, err := os.Stat(f)
				if err == nil && info.ModTime().After(newestTime) {
					newest = f
					newestTime = info.ModTime()
				}
			}
			return newest
		}
	}
	return ""
}

// RecentDirs returns recently-used project directories from Claude's
// session history (~/.claude/projects/). Each subdirectory is a dir-key
// (the encoded working directory path). The newest .jsonl in each
// subdirectory determines LastUsed.
func (c *Claude) RecentDirs(max int) []RecentDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}

	var dirs []RecentDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(projectsDir, e.Name())
		newest := newestFileModTime(subdir, "*.jsonl")
		if newest.IsZero() {
			continue
		}
		dirs = append(dirs, RecentDir{
			Path:     e.Name(),
			LastUsed: newest,
		})
	}

	// Sort by most recent first
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].LastUsed.After(dirs[j].LastUsed)
	})

	if max > 0 && len(dirs) > max {
		dirs = dirs[:max]
	}
	return dirs
}

// SpawnCommand builds the exec.Cmd to launch a new Claude session.
//
// Flags:
//   - --model <model> if model is set and not "default"
//   - --dangerously-skip-permissions if mode == "bypass"
//   - --permission-mode plan if mode == "plan"
func (c *Claude) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("claude")
	var args []string

	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}

	switch mode {
	case "bypass":
		args = append(args, "--dangerously-skip-permissions")
	case "plan":
		args = append(args, "--permission-mode", "plan")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns the available models and modes for launching Claude.
func (c *Claude) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "opus", "sonnet", "haiku"},
		Modes:  []string{"default", "bypass", "plan"},
	}
}

func findBinary(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}
