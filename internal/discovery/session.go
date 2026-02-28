package discovery

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionInfo holds aggregated data parsed from a Claude JSONL session file.
type SessionInfo struct {
	SessionID        string
	GitBranch        string
	Model            string
	TokensIn         int64
	TokensOut        int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	MessageCount     int
	LastTimestamp     time.Time
	LastAction       string // most recent tool call, e.g. "Ed main.go"
}

// sessionLine is the minimal structure needed to parse JSONL entries.
type sessionLine struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId"`
	GitBranch string    `json:"gitBranch"`
	Timestamp time.Time `json:"timestamp"`
	Message   *struct {
		Model   string `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseSessionFile reads a JSONL session file and aggregates token usage
// across all messages.
func ParseSessionFile(path string) (SessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	var info SessionInfo
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		var entry sessionLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}

		info.MessageCount++

		if info.SessionID == "" && entry.SessionID != "" {
			info.SessionID = entry.SessionID
		}
		if entry.GitBranch != "" {
			info.GitBranch = entry.GitBranch
		}
		if !entry.Timestamp.IsZero() {
			info.LastTimestamp = entry.Timestamp
		}

		if entry.Message != nil {
			if info.Model == "" && entry.Message.Model != "" {
				info.Model = entry.Message.Model
			}
			if entry.Message.Usage != nil {
				u := entry.Message.Usage
				info.TokensIn += u.InputTokens
				info.TokensOut += u.OutputTokens
				info.CacheReadTokens += u.CacheReadInputTokens
				info.CacheWriteTokens += u.CacheCreationInputTokens
			}
			// Extract last tool call for LastAction
			if action := extractLastToolAction(entry.Message.Content); action != "" {
				info.LastAction = action
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return info, fmt.Errorf("scanning session file: %w", err)
	}

	return info, nil
}

// extractLastToolAction parses the content array of an assistant message to find
// the last tool_use block and return a short summary like "Ed main.go".
func extractLastToolAction(content json.RawMessage) string {
	if content == nil {
		return ""
	}
	var blocks []struct {
		Type  string                 `json:"type"`
		Name  string                 `json:"name"`
		Input map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}

	// Find the last tool_use block
	var lastTool string
	var lastInput map[string]interface{}
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name != "" {
			lastTool = b.Name
			lastInput = b.Input
		}
	}
	if lastTool == "" {
		return ""
	}

	short := shortToolLabel(lastTool)
	snippet := toolSnippetForAction(lastTool, lastInput)
	if snippet != "" {
		return short + " " + snippet
	}
	return short
}

// shortToolLabel returns a 2-3 char label for a tool name.
func shortToolLabel(name string) string {
	switch name {
	case "Read":
		return "Rd"
	case "Write":
		return "Wr"
	case "Edit":
		return "Ed"
	case "Bash":
		return "Sh"
	case "Grep":
		return "Gr"
	case "Glob":
		return "Gl"
	case "Task":
		return "Tk"
	default:
		if len(name) > 3 {
			return name[:3]
		}
		return name
	}
}

// toolSnippetForAction extracts a short identifier from tool input.
func toolSnippetForAction(name string, input map[string]interface{}) string {
	if input == nil {
		return ""
	}
	switch name {
	case "Read", "Write", "Edit":
		if path, ok := input["file_path"].(string); ok {
			return filepath.Base(path)
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 20 {
				cmd = cmd[:17] + "..."
			}
			return cmd
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			if len(p) > 15 {
				p = p[:12] + "..."
			}
			return "/" + p + "/"
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	}
	return ""
}

// FindSessionFile searches for a session JSONL file matching the given session ID
// within the projects directory.
func FindSessionFile(sessionID, projectsDir string) string {
	pattern := filepath.Join(projectsDir, "*", sessionID+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// FindSessionFileDefault searches for a session file using the default
// ~/.claude/projects/ directory.
func FindSessionFileDefault(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return FindSessionFile(sessionID, filepath.Join(home, ".claude", "projects"))
}

// SessionFilesForDir finds all JSONL session files associated with a working
// directory. Claude converts directory paths by replacing "/" with "-" to form
// the project directory key.
func SessionFilesForDir(workingDir string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// Claude encodes the absolute path by replacing both "/" and "."
	// with hyphens, resulting in a leading hyphen (from the leading /).
	dirKey := strings.ReplaceAll(workingDir, "/", "-")
	dirKey = strings.ReplaceAll(dirKey, ".", "-")
	projectsDir := filepath.Join(home, ".claude", "projects", dirKey)

	pattern := filepath.Join(projectsDir, "*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}
