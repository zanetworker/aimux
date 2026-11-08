package discovery

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/jsonl"
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
// across all messages. Live transcripts reach hundreds of MB and discovery
// asks every few seconds, so it remembers what it parsed per file and reads
// only what was appended since: an unchanged file (size and mtime) is not
// read at all, a file that shrank or was replaced is read from the start.
// The result is always that of a parse of the whole file.
func ParseSessionFile(path string) (SessionInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return parseSessionFileFull(path) // its error names the open failure
	}
	e := sessionParseFor(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fi != nil && os.SameFile(e.fi, fi) && fi.Size() == e.fi.Size() && fi.ModTime().Equal(e.fi.ModTime()) {
		return e.result, nil
	}
	if e.fi == nil || !os.SameFile(e.fi, fi) || fi.Size() < e.offset {
		e.reset()
	}
	result, err := e.readFrom(path)
	if err != nil {
		e.reset()
		return parseSessionFileFull(path)
	}
	e.fi, e.result = fi, result // fi is from before the read: later appends show as a change
	return result, nil
}

// parseSessionFileFull is ParseSessionFile without the per-file memory.
func parseSessionFileFull(path string) (SessionInfo, error) {
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return SessionInfo{}, fmt.Errorf("open session file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var agg sessionAgg
	scanner := jsonl.NewScanner(f)
	for scanner.Scan() {
		agg.add(scanner.Bytes())
	}
	info := agg.finish()
	if err := scanner.Err(); err != nil {
		return info, fmt.Errorf("scanning session file: %w", err)
	}
	return info, nil
}

// sessionAgg is the running aggregate of a session's lines.
type sessionAgg struct {
	info SessionInfo
	// Defer expensive content parsing: keep the last assistant content
	// and only call extractLastToolAction once, in finish.
	lastContent json.RawMessage
}

// add folds one JSONL line into the aggregate; malformed lines are skipped.
func (g *sessionAgg) add(line []byte) {
	var entry sessionLine
	if err := json.Unmarshal(line, &entry); err != nil {
		return // skip malformed lines
	}
	info := &g.info
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
		if entry.Message.Content != nil {
			g.lastContent = make(json.RawMessage, len(entry.Message.Content))
			copy(g.lastContent, entry.Message.Content)
		}
	}
}

// finish returns the aggregate with the last tool action filled in.
func (g *sessionAgg) finish() SessionInfo {
	info := g.info
	if action := extractLastToolAction(g.lastContent); action != "" {
		info.LastAction = action
	}
	return info
}

// sessionParsesMax bounds the per-file memory; the oldest file goes first.
const sessionParsesMax = 512

// sessionParse is what has been parsed of one session file: the complete
// lines up to offset, and the result as of fi (the file when last read).
type sessionParse struct {
	mu     sync.Mutex
	offset int64
	agg    sessionAgg
	fi     os.FileInfo
	result SessionInfo
}

// reset forgets what was parsed (the caller holds mu).
func (e *sessionParse) reset() {
	e.offset, e.agg, e.fi, e.result = 0, sessionAgg{}, nil, SessionInfo{}
}

var sessionParses = struct {
	sync.Mutex
	m     map[string]*sessionParse
	order []string // insertion order, oldest first
}{m: map[string]*sessionParse{}}

func sessionParseFor(path string) *sessionParse {
	sessionParses.Lock()
	defer sessionParses.Unlock()
	if e := sessionParses.m[path]; e != nil {
		return e
	}
	e := &sessionParse{}
	sessionParses.m[path] = e
	sessionParses.order = append(sessionParses.order, path)
	for len(sessionParses.m) > sessionParsesMax && len(sessionParses.order) > 0 {
		oldest := sessionParses.order[0]
		sessionParses.order = sessionParses.order[1:]
		if oldest != path {
			delete(sessionParses.m, oldest)
		}
	}
	return e
}

// readFrom folds in the complete lines after offset and returns the result
// including a last line without its newline, as a whole-file parse counts
// it; that line is not kept, since it may still be being written.
func (e *sessionParse) readFrom(path string) (SessionInfo, error) {
	f, err := os.Open(path) // #nosec G304 -- a session file path from discovery
	if err != nil {
		return SessionInfo{}, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(e.offset, io.SeekStart); err != nil {
		return SessionInfo{}, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := readLine(r)
		if err == io.EOF {
			if len(line) == 0 {
				return e.agg.finish(), nil
			}
			tail := e.agg // the unfinished line, on a copy
			tail.add(bytes.TrimSuffix(line, []byte("\r")))
			return tail.finish(), nil
		}
		if err != nil {
			return SessionInfo{}, err
		}
		e.offset += int64(len(line)) + 1
		e.agg.add(bytes.TrimSuffix(line, []byte("\r")))
	}
}

// readLine returns the next line without its newline, or the remaining
// bytes and io.EOF when no newline follows. Lines longer than
// jsonl.MaxLineSize are an error, as for the whole-file scanner.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > jsonl.MaxLineSize {
			return nil, bufio.ErrTooLong
		}
		switch err {
		case nil:
			return line[:len(line)-1], nil
		case bufio.ErrBufferFull:
			continue
		default:
			return line, err
		}
	}
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

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ValidSessionID reports whether id looks like an agent session id (letters,
// digits, dash, underscore). Anything else, such as a path separator, ".."
// or a glob character, is refused before it reaches the filesystem.
func ValidSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// FindSessionFile searches for a session JSONL file matching the given session ID
// within the projects directory. A found path is remembered (a session's
// file does not move) and only stat'ed on later calls; the search reads
// every project directory, and there are thousands.
func FindSessionFile(sessionID, projectsDir string) string {
	if !ValidSessionID(sessionID) {
		return "" // never let an id add path segments or glob patterns
	}
	key := projectsDir + "\x00" + sessionID
	foundSessionFiles.Lock()
	known := foundSessionFiles.m[key]
	foundSessionFiles.Unlock()
	if known != "" {
		if _, err := os.Stat(known); err == nil {
			return known
		}
	}
	pattern := filepath.Join(projectsDir, "*", sessionID+".jsonl")
	matches, err := filepath.Glob(pattern)
	found := ""
	if err == nil && len(matches) > 0 {
		found = matches[0]
	}
	foundSessionFiles.Lock()
	defer foundSessionFiles.Unlock()
	if found == "" {
		delete(foundSessionFiles.m, key)
		return ""
	}
	if len(foundSessionFiles.m) >= foundSessionFilesMax {
		foundSessionFiles.m = map[string]string{} // rare: start over
	}
	foundSessionFiles.m[key] = found
	return found
}

// foundSessionFilesMax bounds FindSessionFile's memory of found paths.
const foundSessionFilesMax = 4096

var foundSessionFiles = struct {
	sync.Mutex
	m map[string]string // projectsDir NUL session id -> file
}{m: map[string]string{}}

// FindSessionFileAt is FindSessionFile for a session believed to belong to
// cwd: it first checks that directory's project folder, one stat, and
// searches all of them only when the file is not there.
func FindSessionFileAt(sessionID, cwd, projectsDir string) string {
	if !ValidSessionID(sessionID) {
		return ""
	}
	if key := ProjectDirKey(cwd); key != "" {
		path := filepath.Join(projectsDir, key, sessionID+".jsonl")
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			return path
		}
	}
	return FindSessionFile(sessionID, projectsDir)
}

// ProjectDirKey is the name Claude Code gives a working directory's folder
// under ~/.claude/projects: every character other than an ASCII letter or
// digit becomes "-". "" for no directory.
func ProjectDirKey(cwd string) string {
	if cwd == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
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
