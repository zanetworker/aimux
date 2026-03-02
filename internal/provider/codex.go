package provider

import (
	"bufio"
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
	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/trace"
)

// Codex is a Provider implementation for the OpenAI Codex CLI.
type Codex struct{}

func (c *Codex) Name() string { return "codex" }

// Discover finds running Codex CLI processes and recent session files.
// Running processes are discovered via ps; recent sessions (last 24h) without
// a running process are shown as idle so they can be resumed.
func (c *Codex) Discover() ([]agent.Agent, error) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return nil, fmt.Errorf("ps aux: %w", err)
	}

	tmuxSessions := discovery.ListTmuxSessions()

	var agents []agent.Agent
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !isCodexProcess(line) {
			continue
		}
		a := c.parseProcess(line)
		if a != nil {
			agents = append(agents, *a)
		}
	}

	// Deduplicate: keep only the native binary, not the node wrapper
	agents = c.dedup(agents)

	// Enrich with session data
	for i := range agents {
		c.enrichAgent(&agents[i], tmuxSessions)
	}

	return agents, nil
}

// discoverRecentSessions finds Codex session files from the last 24 hours that
// don't have a corresponding running process. These appear as idle/resumable.
func (c *Codex) discoverRecentSessions(running []agent.Agent) []agent.Agent {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	sessionsDir := filepath.Join(home, ".codex", "sessions")
	cutoff := time.Now().Add(-24 * time.Hour)

	// Collect session IDs and working dirs that are already running
	runningIDs := make(map[string]bool)
	runningDirs := make(map[string]bool)
	for _, a := range running {
		if a.SessionID != "" {
			runningIDs[a.SessionID] = true
		}
		if a.WorkingDir != "" {
			runningDirs[a.WorkingDir] = true
		}
	}

	var agents []agent.Agent
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}

		meta := c.readSessionMeta(path)
		if meta.sessionID == "" {
			return nil
		}
		if runningIDs[meta.sessionID] {
			return nil // already shown as running
		}
		if meta.cwd != "" && runningDirs[meta.cwd] {
			return nil // running process in same directory
		}

		// Parse full session for last activity
		sessionInfo := c.parseSession(path)

		a := agent.Agent{
			PID:          0,
			SessionID:    meta.sessionID,
			ProviderName: "codex",
			WorkingDir:   meta.cwd,
			SessionFile:  path,
			Status:       agent.StatusIdle,
			Source:       agent.SourceCLI,
			GroupCount:   1,
			GroupPIDs:    []int{},
		}

		if meta.cwd != "" {
			a.Name = filepath.Base(meta.cwd)
		} else {
			a.Name = "codex"
		}

		if !sessionInfo.lastTimestamp.IsZero() {
			a.LastActivity = sessionInfo.lastTimestamp
			a.StartTime = sessionInfo.lastTimestamp
		} else {
			a.LastActivity = info.ModTime()
			a.StartTime = info.ModTime()
		}

		agents = append(agents, a)
		return nil
	})

	return agents
}

// isCodexProcess returns true if a ps line represents a Codex CLI process
// worth tracking.
func isCodexProcess(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return false
	}

	cmd := strings.Join(fields[10:], " ")

	// Must contain "codex" somewhere in the command
	if !strings.Contains(cmd, "codex") {
		return false
	}

	// Exclude non-session processes
	if strings.Contains(cmd, "app-server") {
		return false
	}
	if strings.Contains(cmd, "mcp-server") {
		return false
	}
	if strings.Contains(cmd, "grep") || strings.Contains(cmd, "aimux") {
		return false
	}
	// Exclude tmux sessions
	if strings.Contains(cmd, "tmux") {
		return false
	}

	// Must be an actual codex binary (not a random script mentioning codex)
	binary := fields[10]
	isCLI := strings.HasSuffix(binary, "/codex") || binary == "codex"
	isNode := (binary == "node" || strings.HasSuffix(binary, "/node")) &&
		strings.Contains(cmd, "/codex")
	if !isCLI && !isNode {
		return false
	}

	return true
}

func (c *Codex) parseProcess(line string) *agent.Agent {
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
	binary := fields[10]

	// Determine source
	source := agent.SourceCLI
	if strings.Contains(cmd, ".vscode") {
		source = agent.SourceVSCode
	}

	// Detect if this is the node wrapper vs native binary
	isNodeWrapper := (binary == "node" || strings.HasSuffix(binary, "/node"))

	// Extract model if specified
	model := codexExtractFlag(cmd, "--model")
	if model == "" {
		model = codexExtractFlag(cmd, "-m")
	}

	// Extract sandbox mode
	perm := codexExtractFlag(cmd, "--sandbox")
	if perm == "" {
		perm = codexExtractFlag(cmd, "-s")
	}
	if strings.Contains(cmd, "--dangerously-bypass-approvals-and-sandbox") {
		perm = "bypass"
	}
	if strings.Contains(cmd, "--full-auto") {
		perm = "full-auto"
	}
	if perm == "" {
		perm = "default"
	}

	return &agent.Agent{
		PID:            pid,
		MemoryMB:       rss / 1024,
		Source:         source,
		Model:          model,
		ProviderName:   "codex",
		PermissionMode: perm,
		Status:         agent.StatusUnknown,
		LastActivity:   time.Now(),
		Name:           fmt.Sprintf("codex-%s", func() string {
			if isNodeWrapper {
				return "node"
			}
			return "native"
		}()),
	}
}

// dedup removes node wrapper processes when the native binary is also present
// (same session), and groups by WorkingDir+Model.
func (c *Codex) dedup(agents []agent.Agent) []agent.Agent {
	// First resolve CWDs
	for i := range agents {
		if agents[i].WorkingDir == "" {
			if cwd, err := exec.Command("lsof", "-p", strconv.Itoa(agents[i].PID), "-Fn").Output(); err == nil {
				for _, line := range strings.Split(string(cwd), "\n") {
					if strings.HasPrefix(line, "n") && strings.HasPrefix(line[1:], "/") {
						// lsof cwd line
						agents[i].WorkingDir = line[1:]
						break
					}
				}
			}
			// Fallback to /proc or pwdx-style
			if agents[i].WorkingDir == "" {
				if cwd, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(agents[i].PID)).Output(); err == nil {
					if cd := codexExtractFlag(string(cwd), "-C"); cd != "" {
						agents[i].WorkingDir = cd
					} else if cd := codexExtractFlag(string(cwd), "--cd"); cd != "" {
						agents[i].WorkingDir = cd
					}
				}
			}
		}
	}

	// Group by (WorkingDir, Model) — keep one per group
	type key struct{ dir, model string }
	groups := make(map[key]*agent.Agent)
	var order []key

	for i := range agents {
		a := &agents[i]
		k := key{a.WorkingDir, a.Model}
		if existing, ok := groups[k]; ok {
			existing.GroupCount++
			existing.GroupPIDs = append(existing.GroupPIDs, a.PID)
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

// enrichAgent resolves CWD, matches a tmux session, and finds the latest session file.
func (c *Codex) enrichAgent(a *agent.Agent, tmuxSessions []discovery.TmuxSession) {
	// Resolve CWD if not already set
	if a.WorkingDir == "" {
		if cwd, err := getCwd(a.PID); err == nil {
			a.WorkingDir = cwd
		}
	}

	// Match tmux session
	if a.WorkingDir != "" {
		a.TMuxSession = discovery.MatchTmuxSession(tmuxSessions, a.WorkingDir)
	}

	a.Name = a.ShortProject()

	// Find the most recent session file for this CWD
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sessionsDir := filepath.Join(home, ".codex", "sessions")

	sessionFile := c.findSessionFile(sessionsDir, a.WorkingDir)
	if sessionFile == "" {
		return
	}

	// Don't show stale session files for recently started agents.
	// If the session file was last modified more than 30s before the agent
	// started, it belongs to a previous session.
	if info, err := os.Stat(sessionFile); err == nil {
		if !a.StartTime.IsZero() && info.ModTime().Before(a.StartTime.Add(-30*time.Second)) {
			return
		}
	}

	a.SessionFile = sessionFile

	// Parse session for metadata
	info := c.parseSession(sessionFile)
	if info.sessionID != "" {
		a.SessionID = info.sessionID
	}
	if info.model != "" && a.Model == "" {
		a.Model = info.model
	}
	if info.tokensIn > 0 || info.tokensOut > 0 {
		a.TokensIn = info.tokensIn
		a.TokensOut = info.tokensOut
		a.EstCostUSD = cost.Calculate(
			a.Model,
			info.tokensIn,
			info.tokensOut,
			info.cachedIn,
			0,
		)
	}
	if !info.lastTimestamp.IsZero() {
		a.LastActivity = info.lastTimestamp
		if time.Since(info.lastTimestamp) < 30*time.Second {
			a.Status = agent.StatusActive
		} else {
			a.Status = agent.StatusIdle
		}
	}
	if info.cwd != "" && a.WorkingDir == "" {
		a.WorkingDir = info.cwd
	}
}

// getCwd resolves the current working directory for a PID.
func getCwd(pid int) (string, error) {
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

type codexSessionInfo struct {
	sessionID     string
	cwd           string
	model         string
	lastTimestamp time.Time
	tokensIn      int64
	tokensOut     int64
	cachedIn      int64
}

// findSessionFile finds the most recent Codex session file matching a CWD.
// Only considers files modified in the last 24 hours. Returns "" if no
// matching recent file is found.
func (c *Codex) findSessionFile(sessionsDir, workingDir string) string {
	cutoff := time.Now().Add(-24 * time.Hour)

	var candidates []string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		// Only consider recent files
		if info.ModTime().Before(cutoff) {
			return nil
		}
		candidates = append(candidates, path)
		return nil
	})

	if len(candidates) == 0 {
		return ""
	}

	// Find the newest file that matches the CWD
	var best string
	var bestTime time.Time

	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		// Must match the working directory
		if workingDir != "" {
			meta := c.readSessionMeta(path)
			if meta.cwd != "" && meta.cwd != workingDir {
				continue
			}
		}

		if info.ModTime().After(bestTime) {
			best = path
			bestTime = info.ModTime()
		}
	}

	// No fallback — only return files that actually match the CWD

	return best
}

// readSessionMeta reads just the first session_meta line from a Codex JSONL.
func (c *Codex) readSessionMeta(path string) codexSessionInfo {
	f, err := os.Open(path)
	if err != nil {
		return codexSessionInfo{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				ID  string `json:"id"`
				CWD string `json:"cwd"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "session_meta" {
			return codexSessionInfo{
				sessionID: entry.Payload.ID,
				cwd:       entry.Payload.CWD,
			}
		}
	}
	return codexSessionInfo{}
}

// parseSession reads a Codex JSONL session for metadata including token usage.
func (c *Codex) parseSession(path string) codexSessionInfo {
	f, err := os.Open(path)
	if err != nil {
		return codexSessionInfo{}
	}
	defer f.Close()

	var info codexSessionInfo
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	for scanner.Scan() {
		var entry struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		if entry.Type == "session_meta" {
			var meta struct {
				ID    string `json:"id"`
				CWD   string `json:"cwd"`
				Model string `json:"model"`
			}
			if json.Unmarshal(entry.Payload, &meta) == nil {
				info.sessionID = meta.ID
				info.cwd = meta.CWD
				if meta.Model != "" {
					info.model = meta.Model
				}
			}
		}

		// Extract token counts from event_msg with type=token_count
		if entry.Type == "event_msg" {
			var evt struct {
				Type string `json:"type"`
				Info struct {
					TotalTokenUsage struct {
						InputTokens       int64 `json:"input_tokens"`
						CachedInputTokens int64 `json:"cached_input_tokens"`
						OutputTokens      int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			}
			if json.Unmarshal(entry.Payload, &evt) == nil && evt.Type == "token_count" {
				info.tokensIn = evt.Info.TotalTokenUsage.InputTokens
				info.tokensOut = evt.Info.TotalTokenUsage.OutputTokens
				info.cachedIn = evt.Info.TotalTokenUsage.CachedInputTokens
			}
		}

		// Extract model from response entries
		var modelEntry struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(scanner.Bytes(), &modelEntry) == nil && modelEntry.Model != "" {
			info.model = modelEntry.Model
		}

		if entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				info.lastTimestamp = ts
			}
		}
	}

	return info
}

func (c *Codex) ResumeCommand(a agent.Agent) *exec.Cmd {
	bin := findBinary("codex")
	// --no-alt-screen prevents Codex's TUI from fighting with aimux's
	// Bubble Tea for the alternate screen buffer.
	if a.SessionID != "" {
		cmd := exec.Command(bin, "resume", "--no-alt-screen", a.SessionID)
		if a.WorkingDir != "" {
			cmd.Dir = a.WorkingDir
		}
		return cmd
	}
	if a.WorkingDir != "" {
		cmd := exec.Command(bin, "resume", "--no-alt-screen", "--last")
		cmd.Dir = a.WorkingDir
		return cmd
	}
	return nil
}

// CanEmbed returns false — Codex's TUI hangs inside an embedded PTY even
// with --no-alt-screen. Use trace view + J to jump out instead.
func (c *Codex) CanEmbed() bool { return false }

// FindSessionFile resolves the session/trace file for a Codex agent.
// Returns "" if the agent has no WorkingDir or if the session file is stale
// (not modified in the last 24 hours).
func (c *Codex) FindSessionFile(a agent.Agent) string {
	if a.WorkingDir == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	sessionsDir := filepath.Join(home, ".codex", "sessions")
	sf := c.findSessionFile(sessionsDir, a.WorkingDir)
	if sf == "" {
		return ""
	}
	// Stale file check: files must be modified in last 24h
	info, err := os.Stat(sf)
	if err != nil {
		return ""
	}
	if time.Since(info.ModTime()) > 24*time.Hour {
		return ""
	}
	return sf
}

// RecentDirs returns recently-used project directories from Codex's
// session history (~/.codex/sessions/). Scans recursively for .jsonl
// files from the last 30 days, reads session_meta for CWD, deduplicates
// by path, sorts by newest first, and caps at max.
func (c *Codex) RecentDirs(max int) []RecentDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	sessionsDir := filepath.Join(home, ".codex", "sessions")
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	byPath := make(map[string]*RecentDir)

	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}

		cwd := extractCodexCWD(path)
		if cwd == "" {
			return nil
		}

		if existing, ok := byPath[cwd]; ok {
			if info.ModTime().After(existing.LastUsed) {
				existing.LastUsed = info.ModTime()
			}
		} else {
			byPath[cwd] = &RecentDir{
				Path:     cwd,
				LastUsed: info.ModTime(),
			}
		}
		return nil
	})

	result := make([]RecentDir, 0, len(byPath))
	for _, rd := range byPath {
		result = append(result, *rd)
	}

	// Sort by most recent first
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastUsed.After(result[j].LastUsed)
	})

	if max > 0 && len(result) > max {
		result = result[:max]
	}
	return result
}

// SpawnCommand builds the exec.Cmd to launch a new Codex session.
//
// Flags:
//   - --no-alt-screen always
//   - --model <model> if model is set and not "default"
//   - --full-auto if mode == "full-auto"
//   - --sandbox workspace-write if mode is empty or "default"
func (c *Codex) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("codex")
	args := []string{"--no-alt-screen"}

	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}

	switch mode {
	case "full-auto":
		args = append(args, "--full-auto")
	case "full-access":
		args = append(args, "--sandbox", "danger-full-access", "--ask-for-approval", "never")
	case "read-only":
		args = append(args, "--sandbox", "read-only")
	case "", "default":
		args = append(args, "--sandbox", "workspace-write")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns the available models and modes for launching Codex.
func (c *Codex) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "o3", "o4-mini"},
		Modes:  []string{"default", "full-auto", "full-access", "read-only"},
	}
}

// OTELEnv returns env vars to enable Codex CLI's OTEL tracing.
// Codex exports both traces (session spans) and log events.
func (c *Codex) OTELEnv(endpoint string) string {
	return fmt.Sprintf(
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf "+
			"OTEL_EXPORTER_OTLP_ENDPOINT=%s "+
			"OTEL_TRACES_EXPORTER=otlp "+
			"OTEL_LOGS_EXPORTER=otlp ",
		endpoint,
	)
}

// codexExtractFlag extracts the value following a CLI flag from a command string.
func codexExtractFlag(args, flag string) string {
	fields := strings.Fields(args)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// ParseTrace reads a Codex JSONL session file and parses it into trace turns.
func (c *Codex) ParseTrace(filePath string) ([]trace.Turn, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read Codex trace %s: %w", filePath, err)
	}
	return parseCodexJSONL(string(data)), nil
}

// --- Codex JSONL trace parsing ---

// parseCodexJSONL parses Codex CLI session JSONL into trace turns.
// Codex format uses: session_meta, event_msg (user_message, token_count),
// response_item (message role=assistant, function_call, function_call_output).
func parseCodexJSONL(data string) []trace.Turn {
	var turns []trace.Turn
	var current *trace.Turn
	turnNum := 0
	pendingCalls := make(map[string]*trace.ToolSpan) // call_id -> span

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		var entryType string
		json.Unmarshal(raw["type"], &entryType)

		var ts time.Time
		var tsStr string
		if err := json.Unmarshal(raw["timestamp"], &tsStr); err == nil {
			ts, _ = time.Parse(time.RFC3339Nano, tsStr)
		}

		switch entryType {
		case "event_msg":
			var payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Info    *struct {
					TotalTokenUsage *struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			}
			json.Unmarshal(raw["payload"], &payload)

			if payload.Type == "user_message" && payload.Message != "" {
				if current != nil {
					turns = append(turns, *current)
				}
				turnNum++
				current = &trace.Turn{
					Number:    turnNum,
					Timestamp: ts,
				}
				for _, l := range strings.Split(payload.Message, "\n") {
					trimmed := strings.TrimSpace(l)
					if trimmed != "" {
						current.UserLines = append(current.UserLines, trimmed)
					}
				}
			}

			if payload.Type == "token_count" && payload.Info != nil && payload.Info.TotalTokenUsage != nil && current != nil {
				current.TokensIn = payload.Info.TotalTokenUsage.InputTokens
				current.TokensOut = payload.Info.TotalTokenUsage.OutputTokens
			}

		case "response_item":
			var payload struct {
				Type      string          `json:"type"`
				Role      string          `json:"role"`
				Name      string          `json:"name"`
				CallID    string          `json:"call_id"`
				Arguments string          `json:"arguments"`
				Output    string          `json:"output"`
				Content   json.RawMessage `json:"content"`
			}
			json.Unmarshal(raw["payload"], &payload)

			if current == nil {
				turnNum++
				current = &trace.Turn{Number: turnNum, Timestamp: ts}
			}

			if !ts.IsZero() {
				current.EndTime = ts
			}

			switch payload.Type {
			case "message":
				if payload.Role == "assistant" {
					var contentBlocks []map[string]interface{}
					if err := json.Unmarshal(payload.Content, &contentBlocks); err == nil {
						for _, block := range contentBlocks {
							if blockType, _ := block["type"].(string); blockType == "output_text" {
								if text, _ := block["text"].(string); text != "" {
									for _, l := range strings.Split(text, "\n") {
										trimmed := strings.TrimSpace(l)
										if trimmed != "" {
											current.OutputLines = append(current.OutputLines, trimmed)
										}
									}
								}
							}
						}
					}
				}

			case "function_call":
				name := payload.Name
				snippet := ""
				if payload.Arguments != "" {
					var args map[string]interface{}
					if err := json.Unmarshal([]byte(payload.Arguments), &args); err == nil {
						if cmd, ok := args["cmd"].(string); ok {
							cmd = strings.TrimSpace(cmd)
							if len(cmd) > 60 {
								cmd = cmd[:57] + "..."
							}
							snippet = "$ " + cmd
						} else if path, ok := args["file_path"].(string); ok {
							snippet = path
						}
					} else {
						s := payload.Arguments
						if len(s) > 60 {
							s = s[:57] + "..."
						}
						snippet = s
					}
				}

				displayName := codexToolName(name)

				span := trace.ToolSpan{
					Name:      displayName,
					Snippet:   snippet,
					Success:   true,
					ToolUseID: payload.CallID,
				}
				current.Actions = append(current.Actions, span)
				if payload.CallID != "" {
					idx := len(current.Actions) - 1
					pendingCalls[payload.CallID] = &current.Actions[idx]
				}

			case "function_call_output":
				if payload.CallID != "" {
					if span, ok := pendingCalls[payload.CallID]; ok {
						output := payload.Output
						if strings.Contains(strings.ToLower(output), "error") ||
							strings.Contains(output, "Process exited with code 1") {
							span.Success = false
							if len(output) > 200 {
								output = output[:200]
							}
							span.ErrorMsg = output
						}
						delete(pendingCalls, payload.CallID)
					}
				}
			}
		}
	}

	if current != nil {
		turns = append(turns, *current)
	}

	// Calculate per-turn cost
	for i := range turns {
		turns[i].CostUSD = trace.EstimateTurnCost(turns[i].Model, turns[i].TokensIn, turns[i].TokensOut)
	}

	return turns
}

// codexToolName maps Codex function names to shorter display names.
func codexToolName(name string) string {
	switch name {
	case "exec_command", "shell":
		return "Bash"
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "apply_patch", "edit_file":
		return "Edit"
	case "search_files", "grep":
		return "Grep"
	case "list_directory", "ls":
		return "Ls"
	default:
		if len(name) > 12 {
			return name[:12]
		}
		return name
	}
}
