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

	"github.com/zanetworker/aimux/internal/cost"
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
	TokensIn         int64     `json:"tokens_in"`
	TokensOut        int64     `json:"tokens_out"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	FirstPrompt string    `json:"first_prompt"`  // first user message (cleaned, for display)
	Title       string    `json:"title"`         // LLM-generated title (from meta, or empty)
	Resumable   bool      `json:"resumable"`     // true if provider supports resume
	Annotation  string    `json:"annotation"`    // achieved/partial/failed/abandoned
	Note        string    `json:"note"`          // free-text rationale
	Tags        []string  `json:"tags"`          // failure mode tags
	IsSubagent     bool   `json:"is_subagent"`
	PermissionMode string `json:"permission_mode"`
}

// Meta holds session-level annotation data stored in sidecar .meta.json files.
type Meta struct {
	Annotation string   `json:"annotation,omitempty"`
	Note       string   `json:"note,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Title      string   `json:"title,omitempty"` // LLM-generated summary title
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
			s.Title = meta.Title

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
	humanTurnCount := 0

	// Parse all lines to accumulate tokens/cost accurately.
	// First 10 lines also extract the first user prompt.
	var model string
	for scanner.Scan() {
		lineCount++
		raw := make(json.RawMessage, len(scanner.Bytes()))
		copy(raw, scanner.Bytes())

		extractPrompt := lineCount <= 10
		m, isHuman := parseSessionLine(raw, &s, extractPrompt)
		if m != "" {
			model = m
		}
		if isHuman {
			humanTurnCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return Session{}, fmt.Errorf("scan session file %s: %w", filePath, err)
	}

	// Apply subagent detection logic
	if humanTurnCount == 0 {
		if s.PermissionMode == "bypassPermissions" {
			s.IsSubagent = true
		} else if !s.StartTime.IsZero() && !s.LastActive.IsZero() &&
			s.LastActive.Sub(s.StartTime) < 5*time.Minute {
			s.IsSubagent = true
		}
	}

	// Compute cost from accumulated tokens
	if model != "" {
		s.CostUSD = cost.Calculate(model, s.TokensIn, s.TokensOut, s.CacheReadTokens, s.CacheWriteTokens)
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
	Type           string    `json:"type"`
	Timestamp      time.Time `json:"timestamp"`
	GitBranch      string    `json:"gitBranch"`
	PermissionMode string    `json:"permissionMode"`
	Message        *struct {
		Model   string          `json:"model"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// isHumanMessage returns true if the content appears to be a human-written message.
// Returns false for tool_result-only content, system-generated content with markers
// like <local-command-caveat> or <command-name>, or empty content.
func isHumanMessage(content json.RawMessage) bool {
	if content == nil {
		return false
	}

	// Try parsing as plain string first
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		if text == "" {
			return false
		}
		// Check for system markers
		if strings.Contains(text, "<local-command-caveat>") || strings.Contains(text, "<command-name>") {
			return false
		}
		return true
	}

	// Try parsing as array of blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				// Check for system markers in text blocks
				if strings.Contains(b.Text, "<local-command-caveat>") || strings.Contains(b.Text, "<command-name>") {
					return false
				}
				return true
			}
		}
		// All blocks are non-text (e.g., tool_result)
		return false
	}

	return false
}

// parseSessionLine extracts metadata from a single JSONL entry.
// If extractPrompt is true, it also looks for the first user message.
// Returns the model name if found in this entry, and a boolean indicating if this is a human message.
func parseSessionLine(raw json.RawMessage, s *Session, extractPrompt bool) (string, bool) {
	var entry sessionEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", false
	}

	if !entry.Timestamp.IsZero() {
		if s.StartTime.IsZero() || entry.Timestamp.Before(s.StartTime) {
			s.StartTime = entry.Timestamp
		}
		if entry.Timestamp.After(s.LastActive) {
			s.LastActive = entry.Timestamp
		}
	}

	// Track permission mode (first one wins)
	if entry.PermissionMode != "" && s.PermissionMode == "" {
		s.PermissionMode = entry.PermissionMode
	}

	if entry.Message == nil {
		return "", false
	}

	var isHuman bool
	if entry.Message.Role == "user" {
		isHuman = isHumanMessage(entry.Message.Content)
	}

	var model string
	if entry.Message.Model != "" {
		model = entry.Message.Model
	}

	if entry.Message.Usage != nil {
		s.TokensIn += entry.Message.Usage.InputTokens
		s.TokensOut += entry.Message.Usage.OutputTokens
		s.CacheReadTokens += entry.Message.Usage.CacheReadInputTokens
		s.CacheWriteTokens += entry.Message.Usage.CacheCreationInputTokens
	}

	// Extract first meaningful user prompt (skip image-only or noise-only messages)
	if extractPrompt && (s.FirstPrompt == "" || s.FirstPrompt == "(no prompt)") && entry.Message.Role == "user" {
		if text := extractUserText(entry.Message.Content); text != "" && text != "(no prompt)" {
			s.FirstPrompt = text
		}
	}

	return model, isHuman
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
// Extracts the first meaningful sentence, strips noise like pasted
// content, markdown headers, Slack messages, and system prompts.
func cleanPrompt(text string) string {
	// Split into lines and find the first meaningful one
	lines := strings.Split(text, "\n")
	var best string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip noise lines
		if isNoiseLine(line) {
			continue
		}
		// Strip markdown header prefix
		line = strings.TrimLeft(line, "# ")
		line = strings.TrimSpace(line)
		if len(line) < 5 {
			continue
		}
		best = line
		break
	}

	if best == "" {
		// Fallback: collapse non-noise lines
		var fallbackParts []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !isNoiseLine(line) {
				fallbackParts = append(fallbackParts, line)
			}
		}
		if len(fallbackParts) > 0 {
			best = strings.Join(fallbackParts, " ")
		} else {
			return "(no prompt)"
		}
	}

	// Strip any remaining XML-like tags
	for strings.Contains(best, "<") && strings.Contains(best, ">") {
		start := strings.Index(best, "<")
		end := strings.Index(best[start:], ">")
		if end < 0 {
			break
		}
		best = best[:start] + best[start+end+1:]
	}

	// Collapse whitespace
	for strings.Contains(best, "  ") {
		best = strings.ReplaceAll(best, "  ", " ")
	}
	best = strings.TrimSpace(best)

	// Take first sentence if there are multiple
	for _, sep := range []string{". ", "? ", "! "} {
		if idx := strings.Index(best, sep); idx > 10 && idx < 100 {
			best = best[:idx+1]
			break
		}
	}

	if len(best) > 80 {
		best = best[:77] + "..."
	}
	return best
}

// isNoiseLine returns true for lines that are likely pasted content,
// system prompts, or other noise rather than a real user request.
func isNoiseLine(line string) bool {
	lower := strings.ToLower(line)

	// Pasted Slack/chat messages
	if strings.Contains(line, "PM]") || strings.Contains(line, "AM]") {
		return true
	}
	// Calendar/leave data
	if strings.Contains(lower, "annual leave") || strings.Contains(lower, "approved") {
		return true
	}
	// System/eval prompts
	if strings.HasPrefix(lower, "# session evaluation") || strings.HasPrefix(lower, "analyze this claude") {
		return true
	}
	// XML-like tags from system messages
	if strings.HasPrefix(line, "<") {
		return true
	}
	// Box-drawing characters (pasted terminal output)
	if strings.ContainsAny(line[:minInt(3, len(line))], "┌┐└┘├┤┬┴│─╭╮╰╯║═") {
		return true
	}
	// Lines starting with special characters (shell prompts, unicode markers)
	if len(line) > 0 {
		first := rune(line[0])
		if first == 0x276F || first == 0x25CF { // ❯ ●
			return true
		}
	}
	if strings.HasPrefix(line, "❯") || strings.HasPrefix(line, "⏺") {
		return true
	}
	// Very short or numeric-only lines
	if len(strings.TrimSpace(line)) < 3 {
		return true
	}
	// Date-only lines
	if len(line) <= 12 && (strings.Contains(line, "/") || strings.Contains(line, "-")) {
		digits := 0
		for _, r := range line {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits > len(line)/2 {
			return true
		}
	}
	return false
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// FindEmpty returns sessions with very low activity (<=2 turns and $0 cost).
func FindEmpty(sessions []Session) []Session {
	var result []Session
	for _, s := range sessions {
		if s.TurnCount <= 2 && s.CostUSD == 0 {
			result = append(result, s)
		}
	}
	return result
}

// FindDuplicates returns duplicate session candidates. Sessions with the same
// FirstPrompt (or Title) within the same project are grouped; the one with the
// most turns is kept, the rest are returned as duplicates.
func FindDuplicates(sessions []Session) []Session {
	type groupKey struct {
		project string
		prompt  string
	}

	groups := make(map[groupKey][]Session)
	for _, s := range sessions {
		prompt := s.Title
		if prompt == "" {
			prompt = s.FirstPrompt
		}
		if prompt == "" || prompt == "(no prompt)" {
			continue
		}
		key := groupKey{project: s.Project, prompt: prompt}
		groups[key] = append(groups[key], s)
	}

	var dupes []Session
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		bestIdx := 0
		for i, s := range group {
			if s.TurnCount > group[bestIdx].TurnCount {
				bestIdx = i
			}
		}
		for i, s := range group {
			if i != bestIdx {
				dupes = append(dupes, s)
			}
		}
	}
	return dupes
}

// TitleForSessionFile returns the LLM-generated title from the sidecar
// .meta.json file, or "" if none exists.
func TitleForSessionFile(sessionFilePath string) string {
	if sessionFilePath == "" {
		return ""
	}
	meta := LoadMeta(sessionFilePath)
	return meta.Title
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
