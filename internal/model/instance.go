package model

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// SourceType represents how a Claude instance was launched.
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

// Status represents the current state of a Claude instance.
type Status int

const (
	StatusActive            Status = iota // actively processing
	StatusIdle                            // idle, waiting for input
	StatusWaitingPermission               // blocked on permission prompt
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
		return "●"
	case StatusIdle:
		return "○"
	case StatusWaitingPermission:
		return "◐"
	case StatusUnknown:
		return "?"
	default:
		return "?"
	}
}

// Instance represents a running Claude Code session.
type Instance struct {
	PID            int
	SessionID      string
	Model          string // e.g. "claude-opus-4-6[1m]"
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
}

// ShortModel returns a human-friendly shortened model name.
//
// Examples:
//
//	"claude-opus-4-6[1m]"           -> "opus-4.6"
//	"claude-sonnet-4-5@20250929"    -> "sonnet-4.5"
//	"claude-haiku-3-5"              -> "haiku-3.5"
func (i Instance) ShortModel() string {
	m := i.Model
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
func (i Instance) ShortProject() string {
	if i.WorkingDir == "" {
		return ""
	}
	return filepath.Base(i.WorkingDir)
}

// FormatMemory returns a human-friendly memory string.
//
// Examples:
//
//	405  -> "405M"
//	1400 -> "1.4G"
//	0    -> "0M"
func (i Instance) FormatMemory() string {
	if i.MemoryMB >= 1000 {
		gb := float64(i.MemoryMB) / 1000.0
		return fmt.Sprintf("%.1fG", gb)
	}
	return fmt.Sprintf("%dM", i.MemoryMB)
}

// FormatCost returns the estimated cost formatted as a dollar amount.
//
// Examples:
//
//	0.82  -> "$0.82"
//	12.5  -> "$12.50"
//	0     -> "$0.00"
func (i Instance) FormatCost() string {
	return fmt.Sprintf("$%.2f", i.EstCostUSD)
}

// Icon returns the status icon for this instance.
func (i Instance) Icon() string {
	return i.Status.Icon()
}
