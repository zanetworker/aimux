package discovery

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// TmuxSession represents a tmux session with its name and attachment state.
type TmuxSession struct {
	Name     string
	Attached bool
}

// parseTmuxLine parses a single line of `tmux list-sessions` output.
// Format: "name: N windows (created Mon Feb 20 10:00:00 2026) (attached)"
func parseTmuxLine(line string) (name string, attached bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}

	colonIdx := strings.Index(line, ":")
	if colonIdx < 0 {
		return line, false
	}

	name = line[:colonIdx]
	attached = strings.HasSuffix(line, "(attached)")
	return name, attached
}

// ListTmuxSessions runs `tmux list-sessions` and returns the parsed results.
// Returns nil if tmux is not running or not installed.
func ListTmuxSessions() []TmuxSession {
	out, err := exec.Command("tmux", "list-sessions").Output()
	if err != nil {
		return nil
	}

	var sessions []TmuxSession
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, attached := parseTmuxLine(line)
		if name != "" {
			sessions = append(sessions, TmuxSession{
				Name:     name,
				Attached: attached,
			})
		}
	}
	return sessions
}

// MatchTmuxSession finds a tmux session matching the "claude-<project>" naming
// convention based on the working directory's base name.
func MatchTmuxSession(sessions []TmuxSession, workingDir string) string {
	if workingDir == "" {
		return ""
	}
	project := filepath.Base(workingDir)
	targets := []string{
		"claude-" + project,
		"aimux-claude-" + project,
		"aimux-codex-" + project,
		"aimux-gemini-" + project,
	}

	for _, s := range sessions {
		for _, t := range targets {
			if s.Name == t {
				return s.Name
			}
		}
	}
	return ""
}

// MatchTmuxSessionByPID finds an aimux-prefixed tmux session whose pane
// contains the given PID. Falls back to this when MatchTmuxSession fails
// because the process CWD changed after launch.
func MatchTmuxSessionByPID(sessions []TmuxSession, pid int) string {
	if pid <= 0 {
		return ""
	}
	pidStr := fmt.Sprintf("%d", pid)
	for _, s := range sessions {
		if !strings.HasPrefix(s.Name, "aimux-") {
			continue
		}
		out, err := exec.Command("tmux", "list-panes", "-t", s.Name, "-F", "#{pane_pid}").Output() // #nosec G204
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == pidStr {
				return s.Name
			}
		}
	}
	return ""
}
