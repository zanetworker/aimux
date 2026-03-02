package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/trace"
)

// Gemini is a Provider implementation for the Google Gemini CLI.
type Gemini struct{}

func (g *Gemini) Name() string { return "gemini" }

// Discover finds running Gemini CLI processes and enriches them with
// session data from ~/.gemini/tmp/<project>/chats/.
func (g *Gemini) Discover() ([]agent.Agent, error) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return nil, fmt.Errorf("ps aux: %w", err)
	}

	tmuxSessions := discovery.ListTmuxSessions()
	projects := readGeminiProjects()

	var agents []agent.Agent
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" || !isGeminiProcess(line) {
			continue
		}
		a := g.parseProcess(line)
		if a == nil {
			continue
		}

		// Resolve CWD
		if a.WorkingDir == "" {
			if cwd, err := geminiGetCwd(a.PID); err == nil {
				a.WorkingDir = cwd
			}
		}

		// Match tmux session
		if a.WorkingDir != "" {
			a.TMuxSession = discovery.MatchTmuxSession(tmuxSessions, a.WorkingDir)
		}

		a.Name = a.ShortProject()

		// Enrich with session data
		g.enrichFromSession(a, projects)

		agents = append(agents, *a)
	}

	// Deduplicate: group by WorkingDir (Gemini spawns multiple node processes)
	agents = g.dedup(agents)

	return agents, nil
}

// dedup groups Gemini agents by WorkingDir, keeping one entry per project.
// Multiple node processes for the same session are merged into a single entry.
func (g *Gemini) dedup(agents []agent.Agent) []agent.Agent {
	type key struct{ dir string }
	groups := make(map[key]*agent.Agent)
	var order []key

	for i := range agents {
		a := &agents[i]
		k := key{a.WorkingDir}
		if existing, ok := groups[k]; ok {
			existing.GroupCount++
			existing.GroupPIDs = append(existing.GroupPIDs, a.PID)
			// Keep the one with more memory (main process vs wrapper)
			if a.MemoryMB > existing.MemoryMB {
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
			groups[k] = &copy
			order = append(order, k)
		}
	}

	result := make([]agent.Agent, 0, len(groups))
	for _, k := range order {
		result = append(result, *groups[k])
	}
	return result
}

// isGeminiProcess returns true if a ps line represents a Gemini CLI process.
func isGeminiProcess(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return false
	}

	cmd := strings.Join(fields[10:], " ")

	if !strings.Contains(cmd, "gemini") {
		return false
	}

	// Exclude non-session processes
	for _, exclude := range []string{"grep", "aimux", "mcp-server", "mcp ", "tmux"} {
		if strings.Contains(cmd, exclude) {
			return false
		}
	}

	binary := fields[10]
	isCLI := strings.HasSuffix(binary, "/gemini") || binary == "gemini"
	isNode := (binary == "node" || strings.HasSuffix(binary, "/node")) &&
		strings.Contains(cmd, "gemini")
	return isCLI || isNode
}

func (g *Gemini) parseProcess(line string) *agent.Agent {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return nil
	}

	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil
	}

	rss, _ := strconv.ParseUint(fields[5], 10, 64)
	cmd := strings.Join(fields[10:], " ")

	model := geminiExtractFlag(cmd, "--model")
	if model == "" {
		model = geminiExtractFlag(cmd, "-m")
	}

	perm := geminiExtractFlag(cmd, "--approval-mode")
	if strings.Contains(cmd, "--yolo") || strings.Contains(cmd, "-y") {
		perm = "yolo"
	}
	if perm == "" {
		perm = "default"
	}

	return &agent.Agent{
		PID:            pid,
		MemoryMB:       rss / 1024,
		Source:         agent.SourceCLI,
		Model:          model,
		ProviderName:   "gemini",
		PermissionMode: perm,
		Status:         agent.StatusUnknown,
		LastActivity:   time.Now(),
		GroupCount:     1,
		GroupPIDs:      []int{pid},
	}
}

// enrichFromSession finds the logs file for a running agent and extracts
// timestamps to determine active/idle status.
func (g *Gemini) enrichFromSession(a *agent.Agent, projects map[string]string) {
	if a.WorkingDir == "" {
		return
	}

	projectName, ok := projects[a.WorkingDir]
	if !ok {
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// Use logs.json as the session file (contains conversation history)
	logsPath := filepath.Join(home, ".gemini", "tmp", projectName, "logs.json")
	info, err := os.Stat(logsPath)
	if err != nil {
		// Fall back to newest session JSON for last activity time
		chatsDir := filepath.Join(home, ".gemini", "tmp", projectName, "chats")
		sf, lastUpdated := newestSessionJSON(chatsDir)
		if sf != "" && !lastUpdated.IsZero() {
			a.SessionFile = sf
			a.LastActivity = lastUpdated
			if time.Since(lastUpdated) < 30*time.Second {
				a.Status = agent.StatusActive
			} else {
				a.Status = agent.StatusIdle
			}
		}
		return
	}

	a.SessionFile = logsPath
	lastMod := info.ModTime()
	a.LastActivity = lastMod
	if time.Since(lastMod) < 30*time.Second {
		a.Status = agent.StatusActive
	} else {
		a.Status = agent.StatusIdle
	}
}

// CanEmbed returns false because Gemini's TUI cannot run inside an embedded PTY.
func (g *Gemini) CanEmbed() bool { return false }

// ResumeCommand builds the command to resume the latest Gemini session.
func (g *Gemini) ResumeCommand(a agent.Agent) *exec.Cmd {
	if a.WorkingDir == "" {
		return nil
	}
	bin := findBinary("gemini")
	cmd := exec.Command(bin, "--resume", "latest")
	cmd.Dir = a.WorkingDir
	return cmd
}

// FindSessionFile resolves the log file for a Gemini agent by looking up
// the project name in projects.json and returning the logs.json file.
// This file contains the conversation history (user prompts with timestamps).
func (g *Gemini) FindSessionFile(a agent.Agent) string {
	if a.WorkingDir == "" {
		return ""
	}

	projects := readGeminiProjects()
	projectName, ok := projects[a.WorkingDir]
	if !ok {
		return ""
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	logsPath := filepath.Join(home, ".gemini", "tmp", projectName, "logs.json")
	if _, err := os.Stat(logsPath); err != nil {
		return ""
	}
	return logsPath
}

// RecentDirs returns recently-used project directories from Gemini's
// projects.json, sorted by most recent session activity.
func (g *Gemini) RecentDirs(max int) []RecentDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	projects := readGeminiProjects()
	var dirs []RecentDir

	for absPath, projectName := range projects {
		chatsDir := filepath.Join(home, ".gemini", "tmp", projectName, "chats")
		_, lastMod := newestSessionJSON(chatsDir)
		if lastMod.IsZero() {
			continue
		}
		dirs = append(dirs, RecentDir{
			Path:     absPath,
			LastUsed: lastMod,
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].LastUsed.After(dirs[j].LastUsed)
	})

	if max > 0 && len(dirs) > max {
		dirs = dirs[:max]
	}
	return dirs
}

// SpawnCommand builds the exec.Cmd to launch a new Gemini session.
//
// Flags:
//   - --model <model> if model is set and not "default"
//   - --yolo if mode == "yolo"
//   - --approval-mode plan if mode == "plan"
func (g *Gemini) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("gemini")
	var args []string

	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}

	switch mode {
	case "yolo":
		args = append(args, "--yolo")
	case "plan":
		args = append(args, "--approval-mode", "plan")
	case "auto_edit":
		args = append(args, "--approval-mode", "auto_edit")
	case "sandbox":
		args = append(args, "--sandbox")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns the available models and modes for launching Gemini.
func (g *Gemini) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-3-pro", "gemini-3.1-flash"},
		Modes:  []string{"default", "yolo", "auto_edit", "plan", "sandbox"},
	}
}

// OTELEnv returns env vars to enable Gemini CLI's OTEL tracing.
// Gemini exports traces, metrics, and logs.
func (g *Gemini) OTELEnv(endpoint string) string {
	return fmt.Sprintf(
		"GEMINI_CLI_TELEMETRY_ENABLED=true "+
			"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf "+
			"OTEL_EXPORTER_OTLP_ENDPOINT=%s "+
			"OTEL_TRACES_EXPORTER=otlp "+
			"OTEL_LOGS_EXPORTER=otlp ",
		endpoint,
	)
}

// --- helpers ---

// geminiProjectsFile is the structure of ~/.gemini/projects.json.
type geminiProjectsFile struct {
	Projects map[string]string `json:"projects"`
}

// readGeminiProjects reads ~/.gemini/projects.json and returns a map of
// absolute path -> project name.
func readGeminiProjects() map[string]string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".gemini", "projects.json"))
	if err != nil {
		return nil
	}
	var f geminiProjectsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil
	}
	return f.Projects
}

// newestSessionJSON finds the newest session-*.json file in a chats directory.
// Returns the path and the lastUpdated time parsed from the JSON.
// Falls back to file mod time if lastUpdated can't be parsed.
func newestSessionJSON(chatsDir string) (string, time.Time) {
	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		return "", time.Time{}
	}

	var bestPath string
	var bestTime time.Time

	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "session-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestPath = filepath.Join(chatsDir, e.Name())
			bestTime = info.ModTime()
		}
	}

	if bestPath == "" {
		return "", time.Time{}
	}

	// Try to parse lastUpdated from the JSON for more accurate timing
	if t := parseGeminiSessionTime(bestPath); !t.IsZero() {
		bestTime = t
	}

	return bestPath, bestTime
}

// parseGeminiSessionTime reads lastUpdated from a Gemini session JSON file.
func parseGeminiSessionTime(path string) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var session struct {
		LastUpdated string `json:"lastUpdated"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, session.LastUpdated)
	if err != nil {
		return time.Time{}
	}
	return t
}

// geminiGetCwd resolves the current working directory for a PID.
func geminiGetCwd(pid int) (string, error) {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			return line[1:], nil
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}

// geminiExtractFlag extracts the value following a CLI flag from a command string.
func geminiExtractFlag(args, flag string) string {
	fields := strings.Fields(args)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// ParseTrace reads a Gemini logs.json file and parses it into trace turns.
func (g *Gemini) ParseTrace(filePath string) ([]trace.Turn, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read Gemini trace %s: %w", filePath, err)
	}
	return parseGeminiJSON(string(data)), nil
}

// --- Gemini JSON trace parsing ---

// geminiLogEntry is a single entry in Gemini's logs.json array.
type geminiLogEntry struct {
	SessionID string `json:"sessionId"`
	MessageID int    `json:"messageId"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// parseGeminiJSON parses Gemini's logs.json (JSON array of messages) into trace turns.
// Each user message becomes a turn. Gemini logs only store user prompts, not
// assistant responses (those are in the session JSON files).
func parseGeminiJSON(data string) []trace.Turn {
	var entries []geminiLogEntry
	if err := json.Unmarshal([]byte(data), &entries); err != nil {
		return nil
	}

	var turns []trace.Turn
	turnNum := 0

	for _, e := range entries {
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			ts = time.Time{}
		}

		if e.Type == "user" {
			turnNum++
			turn := trace.Turn{
				Number:    turnNum,
				Timestamp: ts,
				EndTime:   ts,
				UserLines: []string{e.Message},
			}
			turns = append(turns, turn)
		} else if e.Type == "model" || e.Type == "assistant" {
			if len(turns) > 0 {
				last := &turns[len(turns)-1]
				last.OutputLines = append(last.OutputLines, e.Message)
				last.EndTime = ts
			}
		} else if e.Type == "info" {
			if len(turns) > 0 {
				last := &turns[len(turns)-1]
				last.OutputLines = append(last.OutputLines, "[info] "+e.Message)
			}
		}
	}

	return turns
}
