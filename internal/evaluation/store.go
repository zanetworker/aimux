package evaluation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Annotation represents a human evaluation label applied to a specific
// conversation turn. Labels classify turns as "good", "bad", "wasteful",
// or "error" with an optional free-text note.
type Annotation struct {
	Turn      int       `json:"turn"`
	Label     string    `json:"label"`
	Note      string    `json:"note,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ExportAction describes a single tool invocation within a conversation turn.
type ExportAction struct {
	Tool    string `json:"tool"`
	Input   string `json:"input"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ExportTurn is the full enriched representation of a conversation turn,
// combining raw trace data with evaluation annotations. Used for serializing
// evaluated sessions to JSONL for downstream analysis.
type ExportTurn struct {
	Turn       int            `json:"turn"`
	Timestamp  string         `json:"timestamp"`
	Input      string         `json:"input"`
	Output     string         `json:"output"`
	Actions    []ExportAction `json:"actions"`
	TokensIn   int64          `json:"tokens_in"`
	TokensOut  int64          `json:"tokens_out"`
	CostUSD    float64        `json:"cost_usd"`
	DurationMs int64          `json:"duration_ms"`
	Label      string         `json:"label,omitempty"`
	Note       string         `json:"note,omitempty"`
}

// ExportSessionMeta holds session-level evaluation metadata written as the
// first line of a JSONL export file (type: "session_meta").
type ExportSessionMeta struct {
	Type         string   `json:"type"`                    // always "session_meta"
	SessionID    string   `json:"session_id"`
	Annotation   string   `json:"annotation,omitempty"`    // achieved/partial/failed/abandoned
	FailureModes []string `json:"failure_modes,omitempty"` // failure-mode tags
	Note         string   `json:"note,omitempty"`          // free-text rationale
	Title        string   `json:"title,omitempty"`         // LLM-generated title
}

// Store manages annotation persistence for a single agent session.
// Annotations are stored as JSONL files under ~/.aimux/evaluations/.
type Store struct {
	sessionID string
	dir       string
}

// NewStore creates a Store for the given session ID. The backing directory
// (~/.aimux/evaluations/) is created lazily on the first write.
func NewStore(sessionID string) *Store {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to current directory if home is unavailable.
		home = "."
	}
	return &Store{
		sessionID: sessionID,
		dir:       filepath.Join(home, ".aimux", "evaluations"),
	}
}

// path returns the JSONL file path for this session's annotations.
func (s *Store) path() string {
	return filepath.Join(s.dir, s.sessionID+".jsonl")
}

// ensureDir creates the evaluations directory if it does not exist.
func (s *Store) ensureDir() error {
	return os.MkdirAll(s.dir, 0o755)
}

// Save appends an annotation as a single JSONL line. The directory is created
// on the first call. Writes are performed atomically by appending to the file
// with O_APPEND to avoid partial writes on concurrent access.
func (s *Store) Save(a Annotation) error {
	if err := s.ensureDir(); err != nil {
		return fmt.Errorf("create evaluations dir: %w", err)
	}

	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal annotation: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open annotations file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write annotation: %w", err)
	}
	return nil
}

// Load reads all annotations for this session from the JSONL file.
// Returns an empty slice (not an error) if the file does not exist.
func (s *Store) Load() ([]Annotation, error) {
	f, err := os.Open(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open annotations file: %w", err)
	}
	defer f.Close()

	var annotations []Annotation
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var a Annotation
		if err := json.Unmarshal(line, &a); err != nil {
			continue // skip malformed lines
		}
		annotations = append(annotations, a)
	}

	if err := scanner.Err(); err != nil {
		return annotations, fmt.Errorf("scanning annotations file: %w", err)
	}
	return annotations, nil
}

// GetForTurn returns the latest annotation for the given turn number,
// or nil if no annotation exists for that turn.
func (s *Store) GetForTurn(turn int) *Annotation {
	annotations, err := s.Load()
	if err != nil || len(annotations) == 0 {
		return nil
	}

	var latest *Annotation
	for i := range annotations {
		if annotations[i].Turn == turn {
			latest = &annotations[i]
		}
	}
	return latest
}

// Remove deletes all annotations for the given turn by rewriting the JSONL
// file without that turn's entries. The rewrite is performed atomically:
// data is written to a temporary file first, then renamed over the original.
func (s *Store) Remove(turn int) error {
	annotations, err := s.Load()
	if err != nil {
		return err
	}

	// Filter out annotations for the target turn.
	var kept []Annotation
	for _, a := range annotations {
		if a.Turn != turn {
			kept = append(kept, a)
		}
	}

	// If nothing changed, nothing to do.
	if len(kept) == len(annotations) {
		return nil
	}

	// If no annotations remain, remove the file entirely.
	if len(kept) == 0 {
		if err := os.Remove(s.path()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove annotations file: %w", err)
		}
		return nil
	}

	// Atomic rewrite: write to temp file, then rename.
	if err := s.ensureDir(); err != nil {
		return fmt.Errorf("create evaluations dir: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, s.sessionID+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	for _, a := range kept {
		data, err := json.Marshal(a)
		if err != nil {
			return fmt.Errorf("marshal annotation: %w", err)
		}
		data = append(data, '\n')
		if _, err := tmp.Write(data); err != nil {
			return fmt.Errorf("write temp file: %w", err)
		}
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path()); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	success = true
	return nil
}

// ExportPath returns the filesystem path for an exported evaluation JSONL
// file. Exports are stored under ~/.aimux/exports/.
func ExportPath(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".aimux", "exports", sessionID+"-export.jsonl")
}

// WriteExport writes all enriched turns as JSONL to the given path. The parent
// directory is created if it does not exist. If sessionMeta is non-nil, it is
// written as the first line (type: "session_meta"). Writes are performed
// atomically via a temp file and rename.
func WriteExport(path string, turns []ExportTurn, sessionMeta *ExportSessionMeta) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create export dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "export.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	// Write session metadata as first line if provided
	if sessionMeta != nil {
		sessionMeta.Type = "session_meta"
		metaData, err := json.Marshal(sessionMeta)
		if err != nil {
			return fmt.Errorf("marshal session meta: %w", err)
		}
		metaData = append(metaData, '\n')
		if _, err := tmp.Write(metaData); err != nil {
			return fmt.Errorf("write session meta: %w", err)
		}
	}

	for _, t := range turns {
		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("marshal export turn: %w", err)
		}
		data = append(data, '\n')
		if _, err := tmp.Write(data); err != nil {
			return fmt.Errorf("write export turn: %w", err)
		}
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	success = true
	return nil
}
