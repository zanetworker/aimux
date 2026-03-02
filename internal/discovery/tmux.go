package discovery

import (
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
	target := "claude-" + project

	for _, s := range sessions {
		if s.Name == target {
			return s.Name
		}
	}
	return ""
}
