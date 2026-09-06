package session

import "time"

// SessionStatus represents the lifecycle state of a managed session.
type SessionStatus string

const (
	StatusCreated    SessionStatus = "created"
	StatusRunning    SessionStatus = "running"
	StatusIdle       SessionStatus = "idle"
	StatusTerminated SessionStatus = "terminated"
	StatusError      SessionStatus = "error"
)

// Session tracks a managed agent session across its full lifecycle.
type Session struct {
	ID           string        `json:"id"`
	AgentConfig  string        `json:"agent_config,omitempty"`
	Environment  string        `json:"environment"`
	SandboxName  string        `json:"sandbox_name,omitempty"`
	Provider     string        `json:"provider"`
	Status       SessionStatus `json:"status"`
	Model        string        `json:"model,omitempty"`
	WorkingDir   string        `json:"working_dir"`
	StartTime    time.Time     `json:"start_time"`
	LastActivity time.Time     `json:"last_activity"`
	TokensIn     int64         `json:"tokens_in"`
	TokensOut    int64         `json:"tokens_out"`
	CostUSD      float64       `json:"cost_usd"`
}

// Store defines CRUD operations for session persistence.
type Store interface {
	Create(s *Session) error
	Get(id string) (*Session, error)
	List() ([]*Session, error)
	Update(s *Session) error
	Delete(id string) error
}
