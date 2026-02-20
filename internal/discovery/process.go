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
// we want to track.
func isClaudeProcess(line string) bool {
	lower := strings.ToLower(line)
	// Must reference a claude binary or node running claude.
	if !strings.Contains(lower, "claude") {
		return false
	}
	// Exclude tmux wrappers, grep, and claudetopus itself.
	excludes := []string{"grep", "claudetopus", "tmux"}
	for _, ex := range excludes {
		if strings.Contains(lower, ex) {
			return false
		}
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
	return model.Instance{
		PID:            proc.PID,
		MemoryMB:       proc.MemoryKB / 1024,
		Source:         classifySource(proc.Command),
		Model:          extractFlag(proc.Command, "--model"),
		PermissionMode: extractFlag(proc.Command, "--permission-mode"),
		SessionID:      extractSessionID(proc.Command),
		Status:         model.StatusUnknown,
		LastActivity:   time.Now(),
	}
}
