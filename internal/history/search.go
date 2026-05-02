package history

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ContentMatch represents a session whose JSONL content matched a search query.
type ContentMatch struct {
	SessionID string // UUID extracted from the filename
	FilePath  string // full path to the .jsonl file
	Snippet   string // first matching line (cleaned for display)
}

// SearchContent uses ripgrep to search inside session JSONL files for the
// given query string. Falls back to grep if rg is not available.
// Returns one ContentMatch per matching session file, with a snippet from the
// first match. The projectsDir parameter overrides ~/.claude/projects/ (for testing).
func SearchContent(query, projectsDir string) ([]ContentMatch, error) {
	if query == "" {
		return nil, nil
	}

	if projectsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home directory: %w", err)
		}
		projectsDir = filepath.Join(home, ".claude", "projects")
	}

	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil, nil
	}

	// Use ripgrep for speed; fall back to grep
	bin, args := searchCommand(query, projectsDir)
	if bin == "" {
		return nil, fmt.Errorf("neither rg nor grep found in PATH")
	}

	cmd := exec.Command(bin, args...)
	out, err := cmd.Output()
	if err != nil {
		// Exit code 1 = no matches (not an error)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("search command failed: %w", err)
	}

	return parseSearchOutput(string(out)), nil
}

// searchCommand returns the binary and args for content search.
// Prefers rg (ripgrep) for speed, falls back to grep.
func searchCommand(query, projectsDir string) (string, []string) {
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return rgPath, []string{
			"--ignore-case",
			"--files-with-matches",
			"--glob", "*.jsonl",
			"--max-count", "1", // stop after first match per file
			query,
			projectsDir,
		}
	}
	if grepPath, err := exec.LookPath("grep"); err == nil {
		return grepPath, []string{
			"-r", "-i", "-l",
			"--include=*.jsonl",
			"-m", "1",
			query,
			projectsDir,
		}
	}
	return "", nil
}

// parseSearchOutput extracts session IDs from rg/grep --files-with-matches output.
// Each line is a file path like /path/to/<session-id>.jsonl.
func parseSearchOutput(output string) []ContentMatch {
	var matches []ContentMatch
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		filePath := strings.TrimSpace(scanner.Text())
		if filePath == "" || !strings.HasSuffix(filePath, ".jsonl") {
			continue
		}

		base := filepath.Base(filePath)
		sessionID := strings.TrimSuffix(base, ".jsonl")

		if seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		matches = append(matches, ContentMatch{
			SessionID: sessionID,
			FilePath:  filePath,
		})
	}
	return matches
}

// SearchContentWithSnippets runs SearchContent and then extracts a short
// snippet from the first matching line of each session file.
func SearchContentWithSnippets(query, projectsDir string) ([]ContentMatch, error) {
	matches, err := SearchContent(query, projectsDir)
	if err != nil {
		return nil, err
	}

	for i := range matches {
		matches[i].Snippet = extractSnippet(matches[i].FilePath, query)
	}
	return matches, nil
}

// extractSnippet finds the first line in a JSONL file containing the query
// and returns a cleaned, truncated snippet for display.
func extractSnippet(filePath, query string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	needle := strings.ToLower(query)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}

		// Extract text content from the JSONL line
		snippet := cleanSnippet(line, needle)
		if snippet != "" {
			return snippet
		}
	}
	return ""
}

// cleanSnippet extracts readable text around the match from a JSONL line.
func cleanSnippet(line, needle string) string {
	lower := strings.ToLower(line)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}

	// Find context window around match
	start := idx - 40
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + 40
	if end > len(line) {
		end = len(line)
	}

	snippet := line[start:end]

	// Clean JSON artifacts
	snippet = strings.ReplaceAll(snippet, `\"`, `"`)
	snippet = strings.ReplaceAll(snippet, `\n`, " ")
	snippet = strings.ReplaceAll(snippet, `\t`, " ")

	// Collapse whitespace
	for strings.Contains(snippet, "  ") {
		snippet = strings.ReplaceAll(snippet, "  ", " ")
	}
	snippet = strings.TrimSpace(snippet)

	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(line) {
		snippet = snippet + "..."
	}

	if len(snippet) > 100 {
		snippet = snippet[:97] + "..."
	}

	return snippet
}

// SearchFile counts matches of query in a single JSONL file.
// Returns the count and a snippet from the first match.
// Used by cross-session search to check each agent's session file individually.
func SearchFile(filePath, query string) (count int, snippet string) {
	if filePath == "" || query == "" {
		return 0, ""
	}
	f, err := os.Open(filePath)
	if err != nil {
		return 0, ""
	}
	defer f.Close()

	needle := strings.ToLower(query)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), needle) {
			count++
			if snippet == "" {
				snippet = cleanSnippet(line, needle)
			}
		}
	}
	return count, snippet
}
