package provider

import (
	"os/exec"
	"time"

	"github.com/zanetworker/agentmux/internal/agent"
)

// Provider discovers and manages AI CLI agents of a specific type.
type Provider interface {
	Name() string
	Discover() ([]agent.Agent, error)
	ResumeCommand(a agent.Agent) *exec.Cmd
	ParseConversation(sessionPath string) ([]Segment, error)
}

// Segment is a single conversation turn, provider-agnostic.
type Segment struct {
	Time    time.Time
	Role    Role
	Content string
	Tool    string // tool name if Role==RoleTool
	Detail  string // e.g., file path, command snippet
}

// Role identifies who produced a conversation segment.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleTool
	RoleSystem
)

func (r Role) String() string {
	switch r {
	case RoleUser:
		return "User"
	case RoleAssistant:
		return "Assistant"
	case RoleTool:
		return "Tool"
	case RoleSystem:
		return "System"
	default:
		return "Unknown"
	}
}
