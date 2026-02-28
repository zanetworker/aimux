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

	"github.com/zanetworker/agentmux/internal/agent"
	"github.com/zanetworker/agentmux/internal/discovery"
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

	return agents, nil
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
	for _, exclude := range []string{"grep", "agentmux", "mcp-server", "mcp ", "tmux"} {
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

// enrichFromSession finds the session file for a running agent and extracts
// lastUpdated to determine active/idle status.
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

	chatsDir := filepath.Join(home, ".gemini", "tmp", projectName, "chats")
	sessionFile, lastUpdated := newestSessionJSON(chatsDir)
	if sessionFile == "" {
		return
	}

	a.SessionFile = sessionFile

	if !lastUpdated.IsZero() {
		a.LastActivity = lastUpdated
		if time.Since(lastUpdated) < 30*time.Second {
			a.Status = agent.StatusActive
		} else {
			a.Status = agent.StatusIdle
		}
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

// FindSessionFile resolves the session file for a Gemini agent by looking up
// the project name in projects.json and finding the newest session in chats/.
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

	chatsDir := filepath.Join(home, ".gemini", "tmp", projectName, "chats")
	path, _ := newestSessionJSON(chatsDir)
	return path
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
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns the available models and modes for launching Gemini.
func (g *Gemini) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-3-pro", "gemini-3.1-flash"},
		Modes:  []string{"default", "yolo", "plan"},
	}
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
