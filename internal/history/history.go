// Package history discovers and manages past AI agent sessions.
// It scans provider session directories (starting with Claude's
// ~/.claude/projects/) and builds a unified list of past sessions
// with metadata for browsing, resuming, and eval annotation.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session represents a past agent session discovered from session files.
type Session struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Project     string    `json:"project"`      // decoded directory path
	FilePath    string    `json:"file_path"`     // full path to session file
	StartTime   time.Time `json:"start_time"`    // first entry timestamp
	LastActive  time.Time `json:"last_active"`   // last entry timestamp
	TurnCount   int       `json:"turn_count"`    // approximate conversation turns
	TokensIn    int64     `json:"tokens_in"`
	TokensOut   int64     `json:"tokens_out"`
	CostUSD     float64   `json:"cost_usd"`
	FirstPrompt string    `json:"first_prompt"`  // first user message (for display)
	Resumable   bool      `json:"resumable"`     // true if provider supports resume
	Annotation  string    `json:"annotation"`    // achieved/partial/failed/abandoned
	Note        string    `json:"note"`          // free-text rationale
	Tags        []string  `json:"tags"`          // failure mode tags
}

// Meta holds session-level annotation data stored in sidecar .meta.json files.
type Meta struct {
	Annotation string   `json:"annotation,omitempty"`
	Note       string   `json:"note,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

// DiscoverOpts controls the scope of session discovery.
type DiscoverOpts struct {
	Dir      string // scope to this working directory ("" = all)
	Provider string // filter by provider ("" = all)
	Limit    int    // max results (0 = unlimited)
}

// Discover scans session directories and returns past sessions sorted
// by LastActive descending (most recent first).
//
// Currently supports Claude sessions in ~/.claude/projects/.
// The projectsDir parameter overrides the default location (for testing).
func Discover(opts DiscoverOpts, projectsDir string) ([]Session, error) {
	if projectsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home directory: %w", err)
		}
		projectsDir = filepath.Join(home, ".claude", "projects")
	}

	if opts.Provider != "" && opts.Provider != "claude" {
		// Only Claude is supported for now
		return nil, nil
	}

	var sessions []Session

	// Find all project directories
	projectDirs, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects directory %s: %w", projectsDir, err)
	}

	// Encode the filter directory the same way Claude encodes project paths,
	// so we compare encoded-to-encoded (avoids ambiguous decode).
	var encodedDir string
	if opts.Dir != "" {
		encodedDir = encodeProjectDir(opts.Dir)
	}

	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}

		// Try to resolve the real filesystem path; fall back to decoded name for display
		projectPath := ResolveProjectDir(pd.Name())
		if projectPath == "" {
			projectPath = decodeProjectDir(pd.Name())
		}

		// If scoped to a directory, compare encoded names
		if encodedDir != "" && pd.Name() != encodedDir {
			continue
		}

		dirPath := filepath.Join(projectsDir, pd.Name())
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}

			sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
			filePath := filepath.Join(dirPath, e.Name())

			s, err := scanSession(sessionID, filePath, projectPath)
			if err != nil {
				continue // skip unreadable sessions
			}

			// Load sidecar metadata
			meta := LoadMeta(filePath)
			s.Annotation = meta.Annotation
			s.Note = meta.Note
			s.Tags = meta.Tags

			sessions = append(sessions, s)
		}
	}

	// Sort by LastActive descending
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})

	if opts.Limit > 0 && len(sessions) > opts.Limit {
		sessions = sessions[:opts.Limit]
	}

	return sessions, nil
}

// scanSession reads the first and last few lines of a session JSONL file
// to extract metadata without parsing the entire file.
func scanSession(id, filePath, project string) (Session, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Session{}, fmt.Errorf("open session file %s: %w", filePath, err)
	}
	defer f.Close()

	s := Session{
		ID:        id,
		Provider:  "claude",
		Project:   project,
		FilePath:  filePath,
		Resumable: true,
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	lineCount := 0
	var firstLines []json.RawMessage
	var allLines []json.RawMessage

	// Read all lines but keep only what we need
	for scanner.Scan() {
		lineCount++
		raw := make(json.RawMessage, len(scanner.Bytes()))
		copy(raw, scanner.Bytes())
		allLines = append(allLines, raw)

		if lineCount <= 10 {
			firstLines = append(firstLines, raw)
		}
	}

	if err := scanner.Err(); err != nil {
		return Session{}, fmt.Errorf("scan session file %s: %w", filePath, err)
	}

	if len(allLines) == 0 {
		return s, nil
	}

	// Parse first lines for start time and first prompt
	for _, raw := range firstLines {
		parseSessionLine(raw, &s, true)
	}

	// Parse last few lines for end time and token totals
	lastStart := len(allLines) - 10
	if lastStart < 0 {
		lastStart = 0
	}
	// Skip lines we already parsed from firstLines
	for i := lastStart; i < len(allLines); i++ {
		if i < len(firstLines) {
			continue
		}
		parseSessionLine(allLines[i], &s, false)
	}

	// Approximate turn count from message count (rough: ~2 messages per turn)
	s.TurnCount = lineCount / 4
	if s.TurnCount < 1 && lineCount > 0 {
		s.TurnCount = 1
	}

	return s, nil
}

// sessionEntry is the minimal structure for fast-scanning JSONL entries.
type sessionEntry struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// parseSessionLine extracts metadata from a single JSONL entry.
// If extractPrompt is true, it also looks for the first user message.
func parseSessionLine(raw json.RawMessage, s *Session, extractPrompt bool) {
	var entry sessionEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return
	}

	if !entry.Timestamp.IsZero() {
		if s.StartTime.IsZero() || entry.Timestamp.Before(s.StartTime) {
			s.StartTime = entry.Timestamp
		}
		if entry.Timestamp.After(s.LastActive) {
			s.LastActive = entry.Timestamp
		}
	}

	if entry.Message == nil {
		return
	}

	if entry.Message.Usage != nil {
		s.TokensIn += entry.Message.Usage.InputTokens
		s.TokensOut += entry.Message.Usage.OutputTokens
	}

	// Extract first user prompt
	if extractPrompt && s.FirstPrompt == "" && entry.Message.Role == "user" {
		s.FirstPrompt = extractUserText(entry.Message.Content)
	}
}

// extractUserText pulls the text from a user message content array.
// Returns a single-line, truncated preview suitable for display.
func extractUserText(content json.RawMessage) string {
	if content == nil {
		return ""
	}

	var raw string

	// Try as array of blocks first (Claude format)
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				raw = b.Text
				break
			}
		}
	}

	// Try as plain string
	if raw == "" {
		var text string
		if err := json.Unmarshal(content, &text); err == nil {
			raw = text
		}
	}

	if raw == "" {
		return ""
	}

	return cleanPrompt(raw)
}

// cleanPrompt normalizes a user prompt for single-line display.
// Collapses newlines, strips markdown headers, and truncates.
func cleanPrompt(text string) string {
	// Collapse newlines and extra whitespace into single spaces
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	// Collapse multiple spaces
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	text = strings.TrimSpace(text)

	if len(text) > 120 {
		text = text[:117] + "..."
	}
	return text
}

// encodeProjectDir converts an absolute path to a Claude project directory name.
// This matches Claude's encoding: replace "/" and "." with "-".
// e.g., "/Users/foo/my.project" → "-Users-foo-my-project"
func encodeProjectDir(path string) string {
	s := strings.ReplaceAll(path, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}

// decodeProjectDir converts a Claude project directory name back to an
// absolute path. Claude encodes paths by replacing "/" and "." with "-".
// e.g., "-Users-foo-my-project" → "/Users/foo/my-project"
//
// This is a best-effort decode: since both "/" and "." map to "-", the
// original path cannot be perfectly reconstructed. We restore the leading
// "/" and leave the rest hyphenated, which is good enough for display.
func decodeProjectDir(dirName string) string {
	if strings.HasPrefix(dirName, "-") {
		// Restore leading slash (the "-" comes from the leading "/")
		return "/" + strings.TrimPrefix(dirName, "-")
	}
	return dirName
}

// ResolveProjectDir attempts to reconstruct the real filesystem path from
// a Claude-encoded project directory name by verifying each path component
// against the filesystem.
//
// Claude encodes "/" and "." as "-", making reconstruction ambiguous.
// This function walks the encoded segments and at each step tries three
// interpretations: "/" separator, "." join, or "-" literal (real hyphen).
// It greedily picks the first existing directory at each level.
//
// Returns the real path if found, or "" if reconstruction fails.
func ResolveProjectDir(encodedName string) string {
	if !strings.HasPrefix(encodedName, "-") {
		return ""
	}

	inner := strings.TrimPrefix(encodedName, "-")
	segments := strings.Split(inner, "-")
	if len(segments) == 0 {
		return ""
	}

	result := resolveSegments(segments, "")
	if result != "" && dirExists(result) {
		return result
	}
	return ""
}

// resolveSegments recursively tries to reconstruct a real path from
// encoded segments. At each position, it tries consuming one or more
// segments joined with "-" (real hyphen) or "." (encoded dot) before
// adding a "/" separator.
func resolveSegments(segments []string, prefix string) string {
	if len(segments) == 0 {
		return prefix
	}

	// Try consuming 1, 2, 3... segments as a single directory component
	// joined by different separators (real hyphen or dot)
	for take := 1; take <= len(segments); take++ {
		consumed := segments[:take]
		rest := segments[take:]

		// Try different join strategies for the consumed segments
		candidates := buildCandidates(consumed)

		for _, component := range candidates {
			path := prefix + "/" + component
			if dirExists(path) {
				if len(rest) == 0 {
					return path
				}
				// Recurse with remaining segments
				result := resolveSegments(rest, path)
				if result != "" {
					return result
				}
			}
		}
	}
	return ""
}

// buildCandidates generates possible directory names from segments.
// For ["github", "com"] it produces: "github" (just first), "github-com", "github.com"
// For ["azaalouk"] it produces: "azaalouk"
// For ["azaalouk", "marketplace"] it produces: "azaalouk-marketplace", "azaalouk.marketplace"
func buildCandidates(segments []string) []string {
	if len(segments) == 1 {
		return []string{segments[0]}
	}

	// Join all with hyphen (real hyphen in name)
	hyphenJoin := strings.Join(segments, "-")
	// Join all with dot (encoded dot)
	dotJoin := strings.Join(segments, ".")

	candidates := []string{hyphenJoin}
	if dotJoin != hyphenJoin {
		candidates = append(candidates, dotJoin)
	}
	return candidates
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// MetaPath returns the sidecar metadata file path for a session file.
// e.g., /path/to/abc123.jsonl → /path/to/abc123.meta.json
func MetaPath(sessionFilePath string) string {
	base := strings.TrimSuffix(sessionFilePath, filepath.Ext(sessionFilePath))
	return base + ".meta.json"
}

// LoadMeta reads session metadata from a sidecar .meta.json file.
// Returns an empty Meta if the file doesn't exist.
func LoadMeta(sessionFilePath string) Meta {
	metaPath := MetaPath(sessionFilePath)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return Meta{}
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}
	}
	return m
}

// SaveMeta writes session metadata to a sidecar .meta.json file.
// Uses atomic write (temp file + rename) to prevent corruption.
func SaveMeta(sessionFilePath string, m Meta) error {
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session metadata: %w", err)
	}
	data = append(data, '\n')

	metaPath := MetaPath(sessionFilePath)
	dir := filepath.Dir(metaPath)

	tmp, err := os.CreateTemp(dir, "meta.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file for session metadata: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp metadata file: %w", err)
	}
	if err := os.Rename(tmpPath, metaPath); err != nil {
		return fmt.Errorf("rename temp metadata file: %w", err)
	}

	success = true
	return nil
}

// CollectTags returns a deduplicated, sorted list of all tags used across
// all session metadata files in the given projects directory. This builds
// the autocomplete vocabulary for the tag input.
func CollectTags(projectsDir string) []string {
	if projectsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		projectsDir = filepath.Join(home, ".claude", "projects")
	}

	tagSet := make(map[string]bool)

	pattern := filepath.Join(projectsDir, "*", "*.meta.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	for _, metaPath := range matches {
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m Meta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		for _, tag := range m.Tags {
			tagSet[tag] = true
		}
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}
