package discovery

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/zanetworker/claudetopus/internal/model"
)

// rawProcess holds fields parsed from ps output.
type rawProcess struct {
	PID      int
	MemoryKB uint64
	Command  string
}

// parseProcessLine parses one line of `ps aux` output.
// Format: USER PID %CPU %MEM VSZ RSS TT STAT STARTED TIME COMMAND...
func parseProcessLine(line string) (rawProcess, error) {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return rawProcess{}, fmt.Errorf("too few fields: %d", len(fields))
	}

	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return rawProcess{}, fmt.Errorf("invalid PID %q: %w", fields[1], err)
	}

	rss, err := strconv.ParseUint(fields[5], 10, 64)
	if err != nil {
		return rawProcess{}, fmt.Errorf("invalid RSS %q: %w", fields[5], err)
	}

	// Command is everything from field 10 onwards, preserving spaces.
	cmdStart := 0
	fieldIdx := 0
	for i, ch := range line {
		if ch == ' ' || ch == '\t' {
			if i > 0 && line[i-1] != ' ' && line[i-1] != '\t' {
				fieldIdx++
			}
		} else if fieldIdx == 10 {
			cmdStart = i
			break
		}
	}
	cmd := line[cmdStart:]

	return rawProcess{
		PID:      pid,
		MemoryKB: rss,
		Command:  cmd,
	}, nil
}

// classifySource detects how a Claude instance was launched.
func classifySource(cmd string) model.SourceType {
	if strings.Contains(cmd, ".vscode/extensions/") || strings.Contains(cmd, ".vscode-server/") {
		return model.SourceVSCode
	}
	if strings.Contains(cmd, "claude_agent_sdk") {
		return model.SourceSDK
	}
	return model.SourceCLI
}

// extractFlag extracts the value following a CLI flag from a command string.
// For example, extractFlag("claude --model opus", "--model") returns "opus".
func extractFlag(args, flag string) string {
	fields := strings.Fields(args)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// extractSessionID extracts a session ID from --resume or --session-id flags.
func extractSessionID(args string) string {
	if id := extractFlag(args, "--resume"); id != "" {
		return id
	}
	return extractFlag(args, "--session-id")
}

// isClaudeProcess returns true if the ps line represents a Claude Code process
// we want to track. Must be an actual claude binary invocation, not a wrapper
// or helper process.
func isClaudeProcess(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return false
	}

	// The binary is fields[10] (first word of the command)
	binary := fields[10]

	// Must be the actual claude binary (bare "claude" or path ending in "/claude")
	isClaude := binary == "claude" || strings.HasSuffix(binary, "/claude")
	if !isClaude {
		return false
	}

	// Exclude processes that aren't actual Claude Code sessions:
	cmd := strings.Join(fields[10:], " ")

	// Chrome native host helper from Claude desktop app
	if strings.Contains(cmd, "chrome-native-host") || strings.Contains(cmd, "Claude.app/Contents/Helpers") {
		return false
	}

	// Shell subprocesses spawned by Claude (zsh -c, bash -c)
	if strings.Contains(binary, "/zsh") || strings.Contains(binary, "/bash") {
		return false
	}

	// tmux wrapper processes
	if strings.Contains(cmd, "tmux") {
		return false
	}

	// grep/claudetopus itself
	if strings.Contains(cmd, "grep") || strings.Contains(cmd, "claudetopus") {
		return false
	}

	return true
}

// ScanProcesses runs `ps aux`, parses each line, and returns Instance stubs
// for every detected Claude process.
func ScanProcesses() ([]model.Instance, error) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return nil, fmt.Errorf("ps aux: %w", err)
	}

	var instances []model.Instance
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] { // skip header
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !isClaudeProcess(line) {
			continue
		}
		proc, err := parseProcessLine(line)
		if err != nil {
			continue
		}
		instances = append(instances, buildInstance(proc))
	}
	return instances, nil
}

// buildInstance creates a model.Instance from a rawProcess.
func buildInstance(proc rawProcess) model.Instance {
	perm := extractFlag(proc.Command, "--permission-mode")
	// Detect bypass from --dangerously-skip-permissions flag
	if perm == "" && strings.Contains(proc.Command, "--dangerously-skip-permissions") {
		perm = "bypass"
	}
	if perm == "bypassPermissions" {
		perm = "bypass"
	}
	if perm == "" {
		perm = "default"
	}

	return model.Instance{
		PID:            proc.PID,
		MemoryMB:       proc.MemoryKB / 1024,
		Source:         classifySource(proc.Command),
		Model:          extractFlag(proc.Command, "--model"),
		PermissionMode: perm,
		SessionID:      extractSessionID(proc.Command),
		Status:         model.StatusUnknown,
		LastActivity:   time.Now(),
	}
}
