package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/subagent"
)

// SourceType represents how an agent was launched.
type SourceType int

const (
	SourceCLI    SourceType = iota // launched from CLI
	SourceVSCode                   // launched from VS Code extension
	SourceSDK                      // launched from SDK
)

func (s SourceType) String() string {
	switch s {
	case SourceCLI:
		return "CLI"
	case SourceVSCode:
		return "VSCode"
	case SourceSDK:
		return "SDK"
	default:
		return "Unknown"
	}
}

// Status represents the current state of an agent.
type Status int

const (
	StatusActive            Status = iota // actively processing
	StatusIdle                            // idle, waiting for input
	StatusWaitingPermission               // blocked on permission prompt
	StatusError                           // crashed, context overflow, or process gone
	StatusUnknown                         // status could not be determined
)

func (s Status) String() string {
	switch s {
	case StatusActive:
		return "Active"
	case StatusIdle:
		return "Idle"
	case StatusWaitingPermission:
		return "Waiting"
	case StatusError:
		return "Error"
	case StatusUnknown:
		return "Unknown"
	default:
		return "Unknown"
	}
}

// Icon returns a single-character icon representing the status.
func (s Status) Icon() string {
	switch s {
	case StatusActive:
		return "▶"
	case StatusIdle:
		return "■"
	case StatusWaitingPermission:
		return "⏸"
	case StatusError:
		return "✕"
	case StatusUnknown:
		return "?"
	default:
		return "?"
	}
}

// Agent represents a running AI coding agent session.
type Agent struct {
	PID            int
	SessionID      string
	Name           string     // project name, derived from WorkingDir
	ProviderName   string     // "claude", "codex", "gemini"
	SessionFile    string     // path to conversation log
	Model          string     // e.g. "claude-opus-4-6[1m]"
	PermissionMode string
	WorkingDir     string
	Source         SourceType
	StartTime      time.Time
	Status         Status
	TMuxSession    string
	MemoryMB       uint64
	GitBranch      string
	TokensIn       int64
	TokensOut      int64
	EstCostUSD     float64
	TeamName       string
	TaskID         string
	TaskSubject    string
	LastActivity   time.Time
	GroupCount     int    // number of processes grouped into this entry (0 or 1 = single)
	GroupPIDs      []int  // PIDs of grouped processes (for drill-down)
	LastAction     string         // most recent tool call, e.g. "Ed main.go", "Sh go test"
	ParentPID      int            // process tree parent (0 = top-level)
	Subagent       subagent.Info  // from OTEL correlation
}

// IsSubagent returns true if this agent is nested under another agent.
func (a Agent) IsSubagent() bool {
	return a.ParentPID != 0
}

// ShortModel returns a human-friendly shortened model name.
//
// Examples:
//
//	"claude-opus-4-6[1m]"           -> "opus-4.6"
//	"claude-sonnet-4-5@20250929"    -> "sonnet-4.5"
//	"claude-haiku-3-5"              -> "haiku-3.5"
func (a Agent) ShortModel() string {
	m := a.Model
	if m == "" {
		return "default"
	}

	// Strip the "claude-" prefix.
	m = strings.TrimPrefix(m, "claude-")

	// Strip any context-window suffix like "[1m]".
	if idx := strings.Index(m, "["); idx != -1 {
		m = m[:idx]
	}

	// Strip any date suffix like "@20250929".
	if idx := strings.Index(m, "@"); idx != -1 {
		m = m[:idx]
	}

	// Split into segments: e.g. "opus-4-6" -> ["opus", "4", "6"]
	parts := strings.Split(m, "-")
	if len(parts) < 2 {
		return m
	}

	name := parts[0]
	version := strings.Join(parts[1:], ".")
	return name + "-" + version
}

// ShortProject returns the last path segment of WorkingDir.
func (a Agent) ShortProject() string {
	if a.WorkingDir == "" {
		return ""
	}
	return filepath.Base(a.WorkingDir)
}

// ShortDir returns the parent directory name — the directory containing
// the project. This disambiguates agents with the same project name in
// different locations. The full path is available in the preview pane.
//
// Examples:
//
//	"/Users/me/go/src/github.com/zanetworker/aimux" -> "zanetworker"
//	"/Users/me/projects/myapp"                          -> "projects"
//	"/tmp/test"                                         -> "tmp"
func (a Agent) ShortDir() string {
	if a.WorkingDir == "" {
		return ""
	}
	parent := filepath.Dir(a.WorkingDir)
	if parent == "" || parent == "." || parent == "/" {
		return filepath.Base(a.WorkingDir)
	}
	return filepath.Base(parent)
}

// FormatMemory returns a human-friendly memory string.
//
// Examples:
//
//	405  -> "405M"
//	1400 -> "1.4G"
//	0    -> "0M"
func (a Agent) FormatMemory() string {
	if a.MemoryMB >= 1000 {
		gb := float64(a.MemoryMB) / 1000.0
		return fmt.Sprintf("%.1fG", gb)
	}
	return fmt.Sprintf("%dM", a.MemoryMB)
}

// FormatCost returns the estimated cost formatted as a dollar amount.
//
// Examples:
//
//	0.82  -> "$0.82"
//	12.5  -> "$12.50"
//	0     -> "$0.00"
func (a Agent) FormatCost() string {
	return fmt.Sprintf("$%.2f", a.EstCostUSD)
}

// Icon returns the status icon for this agent.
func (a Agent) Icon() string {
	return a.Status.Icon()
}

// AgeTime returns the effective time used for age calculation: StartTime if
// available, otherwise LastActivity. Returns the zero time if neither is set.
func (a Agent) AgeTime() time.Time {
	if !a.StartTime.IsZero() {
		return a.StartTime
	}
	return a.LastActivity
}

// FormatAge returns a human-friendly age string based on StartTime or LastActivity.
func (a Agent) FormatAge() string {
	t := a.AgeTime()
	if t.IsZero() {
		return "-"
	}
	return formatDuration(time.Since(t))
}

// formatDuration formats a duration into a compact human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
