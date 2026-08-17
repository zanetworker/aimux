package mcpserver

// TaskResult is the structured JSON returned by task execution.
type TaskResult struct {
	Type         string `json:"type"`
	Summary      string `json:"summary"`
	FullText     string `json:"full_text,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Commit       string `json:"commit,omitempty"`
	FilesChanged int    `json:"files_changed,omitempty"`
	Tokens       int    `json:"tokens_used,omitempty"`
	Duration     int    `json:"duration_seconds,omitempty"`
}
