package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/correlator"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/trace"
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

	// Track assigned session files so no two agents share the same file.
	// When enrichAgent falls back to "newest file in dir", it skips files
	// that were already claimed by a prior agent.
	assignedFiles := make(map[string]bool)

	for i := range agents {
		c.enrichAgent(&agents[i], tmuxSessions, projectsDir, assignedFiles)
		agents[i].ProviderName = "claude"
		if agents[i].Name == "" {
			agents[i].Name = agents[i].ShortProject()
		}
	}

	// Tag subagent relationships from process tree before dedup.
	correlator.TagFromProcessTree(agents, discovery.GetParentPID)

	// Deduplicate: group SDK agents by (WorkingDir, Model), CLI by PID.
	// Subagents are excluded — they'll be nested under parents in the TUI.
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
// working directory paths by replacing "/" with "-". The result has a leading
// "-" (from the leading "/"). This decoding tries the naive replacement first,
// then progressively tries to fix common patterns (github.com, dots in paths)
// by checking if the decoded path exists on disk.
func decodeDirKey(key string) string {
	if !strings.HasPrefix(key, "-") {
		return ""
	}

	// Naive: replace all hyphens with /
	naive := strings.ReplaceAll(key, "-", "/")

	// Check if it exists
	if _, err := os.Stat(naive); err == nil {
		return naive
	}

	// Try common fix: "github/com" -> "github.com"
	fixed := strings.ReplaceAll(naive, "github/com", "github.com")
	if _, err := os.Stat(fixed); err == nil {
		return fixed
	}

	// Try reconstructing by walking from root. For each segment after the
	// leading /, check if the segment exists. If not, try joining it with
	// the next segment using "-" or "." instead of "/".
	parts := strings.Split(naive[1:], "/") // skip leading /
	path := "/"
	for i := 0; i < len(parts); i++ {
		candidate := filepath.Join(path, parts[i])
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			continue
		}
		// Try joining with next segment using hyphen
		if i+1 < len(parts) {
			hyphenJoin := filepath.Join(path, parts[i]+"-"+parts[i+1])
			if _, err := os.Stat(hyphenJoin); err == nil {
				path = hyphenJoin
				i++ // skip next part
				continue
			}
			// Try dot join
			dotJoin := filepath.Join(path, parts[i]+"."+parts[i+1])
			if _, err := os.Stat(dotJoin); err == nil {
				path = dotJoin
				i++
				continue
			}
		}
		// Give up on reconstruction, use what we have
		for j := i; j < len(parts); j++ {
			path = filepath.Join(path, parts[j])
		}
		break
	}

	if _, err := os.Stat(path); err == nil {
		return path
	}

	// Last resort: return the naive decode
	return naive
}

// deduplicateAgents groups SDK agents sharing the same (WorkingDir, Model) into
// single entries with a GroupCount. CLI agents are kept separate by PID since
// deduplicateAgents groups SDK/VSCode agents by (WorkingDir, Model) and keeps
// CLI agents as distinct entries by PID. Subagents (tagged by TagFromProcessTree)
// are passed through without dedup — they'll be nested in the TUI.
func deduplicateAgents(agents []agent.Agent) []agent.Agent {
	groups := make(map[string]*agent.Agent)
	order := make([]string, 0) // preserve discovery order
	var subagents []agent.Agent

	for i := range agents {
		a := &agents[i]

		// Subagents pass through — they'll be nested under parents in the TUI.
		if a.IsSubagent() {
			subagents = append(subagents, *a)
			continue
		}

		var key string
		switch a.Source {
		case agent.SourceSDK:
			key = fmt.Sprintf("sdk:%s:%s", a.WorkingDir, a.Model)
		case agent.SourceVSCode:
			key = fmt.Sprintf("vsc:%s:%s", a.WorkingDir, a.Model)
		default:
			key = fmt.Sprintf("cli:pid:%d", a.PID)
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

	result := make([]agent.Agent, 0, len(groups)+len(subagents))
	for _, key := range order {
		result = append(result, *groups[key])
	}
	result = append(result, subagents...)
	return result
}

// enrichAgent resolves the working directory, matches a tmux session,
// parses the session JSONL file, and calculates the estimated cost.
// assignedFiles tracks which session files have already been claimed by
// other agents, so that multiple sessions in the same directory each get
// a different file.
func (c *Claude) enrichAgent(inst *agent.Agent, tmuxSessions []discovery.TmuxSession, projectsDir string, assignedFiles map[string]bool) {
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

	// Set StartTime from the OS process start time so Age shows correctly.
	if inst.StartTime.IsZero() {
		inst.StartTime = getProcessStartTime(inst.PID)
	}

	// Find and parse session JSONL
	sessionFile := ""
	if inst.SessionID != "" {
		sessionFile = discovery.FindSessionFile(inst.SessionID, projectsDir)
	}
	if sessionFile == "" && inst.WorkingDir != "" {
		sessionFile = c.matchSessionFileByStartTime(inst.PID, inst.WorkingDir, assignedFiles)
	}

	if sessionFile != "" {
		assignedFiles[sessionFile] = true
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

// matchSessionFileByStartTime finds the session file whose first timestamp
// best matches this process's start time. This correctly pairs each Claude
// process with its own session file, even when multiple sessions share a
// directory. Falls back to newest-unassigned if start time can't be obtained.
func (c *Claude) matchSessionFileByStartTime(pid int, workingDir string, assignedFiles map[string]bool) string {
	files := discovery.SessionFilesForDir(workingDir)
	if len(files) == 0 {
		return ""
	}

	// Try timing-based match first.
	startTime := getProcessStartTime(pid)
	if !startTime.IsZero() {
		var bestFile string
		var bestDelta time.Duration = 1<<63 - 1

		for _, f := range files {
			if assignedFiles[f] {
				continue
			}
			firstTS := sessionFirstTimestamp(f)
			if firstTS.IsZero() {
				continue
			}
			delta := firstTS.Sub(startTime)
			if delta < 0 {
				delta = -delta
			}
			if delta < bestDelta {
				bestDelta = delta
				bestFile = f
			}
		}
		if bestFile != "" {
			return bestFile
		}
	}

	// Fallback: newest unassigned file.
	var newest string
	var newestTime time.Time
	for _, f := range files {
		if assignedFiles[f] {
			continue
		}
		info, err := os.Stat(f)
		if err == nil && info.ModTime().After(newestTime) {
			newest = f
			newestTime = info.ModTime()
		}
	}
	return newest
}

// sessionFirstTimestamp reads the first timestamp from a Claude JSONL file.
func sessionFirstTimestamp(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		var entry struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
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
		// Decode dir-key to absolute path
		absPath := decodeDirKey(e.Name())
		if absPath == "" {
			absPath = e.Name() // fallback to raw key
		}
		dirs = append(dirs, RecentDir{
			Path:     absPath,
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
	case "acceptEdits":
		args = append(args, "--permission-mode", "acceptEdits")
	case "dontAsk":
		args = append(args, "--permission-mode", "dontAsk")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns the available models and modes for launching Claude.
func (c *Claude) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "opus", "sonnet", "haiku"},
		Modes:  []string{"default", "plan", "acceptEdits", "bypass", "dontAsk"},
	}
}

// OTELEnv returns env vars to enable Claude Code's OTEL tracing.
// Sets both generic and signal-specific endpoints with explicit paths
// to avoid OTEL SDK path-appending issues across different Node.js
// SDK versions. Uses http/protobuf protocol (port 4318).
func (c *Claude) OTELEnv(endpoint string) string {
	return fmt.Sprintf(
		"CLAUDE_CODE_ENABLE_TELEMETRY=1 "+
			"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf "+
			"OTEL_EXPORTER_OTLP_ENDPOINT=%s "+
			"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=%s/v1/logs "+
			"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf "+
			"OTEL_METRICS_EXPORTER=otlp "+
			"OTEL_LOGS_EXPORTER=otlp "+
			"OTEL_LOG_USER_PROMPTS=1 "+
			"OTEL_LOG_TOOL_DETAILS=1 "+
			"OTEL_METRIC_EXPORT_INTERVAL=30000 "+
			"OTEL_LOGS_EXPORT_INTERVAL=2000 ",
		endpoint, endpoint,
	)
}

func (c *Claude) OTELServiceName() string { return "claude-code" }

// SubagentAttrKeys returns the OTEL attribute names Claude uses for subagent identity.
func (c *Claude) SubagentAttrKeys() subagent.AttrKeys {
	return subagent.AttrKeys{
		ID:       "agent_id",
		Type:     "agent_type",
		ParentID: "parent_agent_id",
	}
}

func findBinary(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}

// ParseTrace reads a Claude JSONL session file and parses it into trace turns.
func (c *Claude) ParseTrace(filePath string) ([]trace.Turn, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read Claude trace %s: %w", filePath, err)
	}
	return parseClaudeJSONL(string(data)), nil
}

// --- Claude JSONL trace parsing ---

type claudeContentBlock struct {
	blockType     string
	text          string
	toolName      string
	toolSnippet   string
	toolUseID     string
	editOldString string
	editNewString string
}

type claudeToolResultEntry struct {
	toolUseID string
	content   string
	isError   bool
}

type claudeJSONLEntry struct {
	entryType      string
	timestamp      time.Time
	isHumanMessage bool
	textContent    string
	blocks         []claudeContentBlock
	tokensIn       int64
	tokensOut      int64
	model          string
	hasToolResults bool
	toolResults    []claudeToolResultEntry
}

func parseClaudeRawEntry(line string) claudeJSONLEntry {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return claudeJSONLEntry{}
	}

	var e claudeJSONLEntry
	json.Unmarshal(raw["type"], &e.entryType)

	var tsStr string
	if err := json.Unmarshal(raw["timestamp"], &tsStr); err == nil {
		e.timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
	}

	switch e.entryType {
	case "user":
		e.parseClaudeUser(raw)
	case "assistant":
		e.parseClaudeAssistant(raw)
	}

	return e
}

func (e *claudeJSONLEntry) parseClaudeUser(raw map[string]json.RawMessage) {
	msgRaw := raw["message"]
	if msgRaw == nil {
		return
	}

	var msgObj map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msgObj); err != nil {
		return
	}

	contentRaw := msgObj["content"]
	if contentRaw == nil {
		return
	}

	// Try as simple string (human message)
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
		e.isHumanMessage = true
		e.textContent = contentStr
		return
	}

	// Try as array (could contain tool_result blocks)
	var contentArr []map[string]interface{}
	if err := json.Unmarshal(contentRaw, &contentArr); err == nil {
		for _, item := range contentArr {
			itemType, _ := item["type"].(string)
			if itemType == "tool_result" {
				e.hasToolResults = true
				tr := claudeToolResultEntry{}
				tr.toolUseID, _ = item["tool_use_id"].(string)
				if isErr, ok := item["is_error"].(bool); ok {
					tr.isError = isErr
				}
				switch c := item["content"].(type) {
				case string:
					tr.content = c
				case []interface{}:
					var parts []string
					for _, block := range c {
						if bm, ok := block.(map[string]interface{}); ok {
							if text, ok := bm["text"].(string); ok {
								parts = append(parts, text)
							}
						}
					}
					tr.content = strings.Join(parts, "\n")
				}
				e.toolResults = append(e.toolResults, tr)
			}
		}
	}

	e.isHumanMessage = false
}

func (e *claudeJSONLEntry) parseClaudeAssistant(raw map[string]json.RawMessage) {
	msgRaw := raw["message"]
	if msgRaw == nil {
		return
	}

	var msgObj map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msgObj); err != nil {
		return
	}

	// Parse model name
	if modelRaw := msgObj["model"]; modelRaw != nil {
		json.Unmarshal(modelRaw, &e.model)
	}

	if usageRaw := msgObj["usage"]; usageRaw != nil {
		var usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		}
		json.Unmarshal(usageRaw, &usage)
		e.tokensIn = usage.InputTokens
		e.tokensOut = usage.OutputTokens
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal(msgObj["content"], &blocks); err != nil {
		return
	}

	for _, block := range blocks {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			text, _ := block["text"].(string)
			text = strings.TrimSpace(text)
			if text != "" {
				e.blocks = append(e.blocks, claudeContentBlock{
					blockType: "text",
					text:      text,
				})
			}
		case "tool_use":
			name, _ := block["name"].(string)
			id, _ := block["id"].(string)
			snippet := ""
			var editOld, editNew string
			if input, ok := block["input"].(map[string]interface{}); ok {
				snippet = claudeToolInputSnippet(name, input)
				if name == "Edit" {
					if old, ok := input["old_string"].(string); ok {
						editOld = old
					}
					if ns, ok := input["new_string"].(string); ok {
						editNew = ns
					}
				}
			}
			e.blocks = append(e.blocks, claudeContentBlock{
				blockType:     "tool_use",
				toolName:      name,
				toolSnippet:   snippet,
				toolUseID:     id,
				editOldString: editOld,
				editNewString: editNew,
			})
		}
	}
}

func parseClaudeJSONL(data string) []trace.Turn {
	var entries []claudeJSONLEntry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e := parseClaudeRawEntry(line)
		if e.entryType != "" {
			entries = append(entries, e)
		}
	}

	var turns []trace.Turn
	var current *trace.Turn
	turnNum := 0

	// Index of tool_use_id -> pointer to the ToolSpan (across turns).
	pendingTools := make(map[string]*trace.ToolSpan)

	for _, e := range entries {
		switch e.entryType {
		case "user":
			if e.isHumanMessage && e.textContent != "" {
				if current != nil {
					turns = append(turns, *current)
				}
				turnNum++
				current = &trace.Turn{
					Number:    turnNum,
					Timestamp: e.timestamp,
				}
				for _, line := range strings.Split(e.textContent, "\n") {
					trimmed := strings.TrimSpace(line)
					if trimmed != "" {
						current.UserLines = append(current.UserLines, trimmed)
					}
				}
			} else if e.hasToolResults {
				for _, tr := range e.toolResults {
					if span, ok := pendingTools[tr.toolUseID]; ok {
						span.Success = !tr.isError
						if tr.isError {
							errMsg := tr.content
							if len(errMsg) > 200 {
								errMsg = errMsg[:200]
							}
							span.ErrorMsg = errMsg
						}
						delete(pendingTools, tr.toolUseID)
					}
				}
			}

		case "assistant":
			if current == nil {
				turnNum++
				current = &trace.Turn{
					Number:    turnNum,
					Timestamp: e.timestamp,
				}
			}
			current.TokensIn += e.tokensIn
			current.TokensOut += e.tokensOut

			if !e.timestamp.IsZero() {
				current.EndTime = e.timestamp
			}

			if e.model != "" && current.Model == "" {
				current.Model = e.model
			}

			for _, block := range e.blocks {
				switch block.blockType {
				case "text":
					for _, line := range strings.Split(block.text, "\n") {
						trimmed := strings.TrimRight(line, " \t\r")
						if strings.TrimSpace(trimmed) != "" {
							current.OutputLines = append(current.OutputLines, trimmed)
						}
					}
				case "tool_use":
					span := trace.ToolSpan{
						Name:      block.toolName,
						Snippet:   block.toolSnippet,
						Success:   true,
						ToolUseID: block.toolUseID,
						OldString: block.editOldString,
						NewString: block.editNewString,
					}
					current.Actions = append(current.Actions, span)
					if block.toolUseID != "" {
						idx := len(current.Actions) - 1
						pendingTools[block.toolUseID] = &current.Actions[idx]
					}
				}
			}
		}
	}

	if current != nil {
		turns = append(turns, *current)
	}

	// Calculate per-turn cost using the cost package
	for i := range turns {
		turns[i].CostUSD = trace.EstimateTurnCost(turns[i].Model, turns[i].TokensIn, turns[i].TokensOut)
	}

	return turns
}

// claudeToolInputSnippet extracts a human-readable snippet from tool input.
func claudeToolInputSnippet(toolName string, input map[string]interface{}) string {
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 60 {
				cmd = cmd[:57] + "..."
			}
			return "$ " + cmd
		}
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			return "/" + pattern + "/"
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return pattern
		}
	case "Task":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "WebSearch":
		if query, ok := input["query"].(string); ok {
			return query
		}
	case "WebFetch":
		if url, ok := input["url"].(string); ok {
			if len(url) > 50 {
				url = url[:47] + "..."
			}
			return url
		}
	}
	return ""
}
