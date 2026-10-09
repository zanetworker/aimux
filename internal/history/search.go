package history

import "strings"

// ContentMatch is one session matched by content search (internal/search),
// as handed to frontends; matches are ordered best first.
type ContentMatch struct {
	SessionID string // session UUID
	FilePath  string // full path to the .jsonl file
	Snippet   string // matching text, terms marked with [brackets]
}

// FilterByPrompt returns sessions whose Title or FirstPrompt contains the
// query string (case-insensitive). An empty query returns all sessions.
func FilterByPrompt(sessions []Session, query string) []Session {
	if query == "" {
		result := make([]Session, len(sessions))
		copy(result, sessions)
		return result
	}
	needle := strings.ToLower(query)
	var result []Session
	for _, s := range sessions {
		if strings.Contains(strings.ToLower(s.Title), needle) ||
			strings.Contains(strings.ToLower(s.FirstPrompt), needle) {
			result = append(result, s)
		}
	}
	return result
}
