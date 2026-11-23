package modelproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Record is one answered request, as the proxy's history file keeps it. It holds how the request went, never what
// it said: no prompt, no answer, no tool arguments.
type Record struct {
	At      time.Time
	Outcome string
	Detail  string
	// Session is the conversation's tag from SessionTag, empty when the request carried no session id.
	Session  string
	Model    string
	Effort   string
	Status   int
	TTFB     time.Duration
	Duration time.Duration
	Usage    Usage
	Error    string
}

// line is Record as one JSON object, with durations in milliseconds so the file reads well under jq.
type line struct {
	At                string `json:"at"`
	Outcome           string `json:"outcome"`
	Detail            string `json:"detail,omitempty"`
	Session           string `json:"session,omitempty"`
	Model             string `json:"model,omitempty"`
	Effort            string `json:"effort,omitempty"`
	Status            int    `json:"status"`
	TTFBms            int64  `json:"ttfb_ms"`
	DurationMs        int64  `json:"duration_ms"`
	InputTokens       int    `json:"input_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	Estimated         bool   `json:"estimated,omitempty"`
	Error             string `json:"error,omitempty"`
}

// RequestLog appends one JSON line per request to a file, rolling the file over at a size limit so an unattended
// proxy cannot fill the disk. A nil *RequestLog records nothing, so a proxy without one still serves requests.
type RequestLog struct {
	mu    sync.Mutex
	file  *os.File
	path  string
	limit int64
	size  int64
}

// OpenRequestLog appends to path, creating it but not its folder: a missing folder is a mistake worth reporting
// rather than a tree to invent. At limit bytes the file becomes path+".1", replacing the previous rollover.
func OpenRequestLog(path string, limit int64) (*RequestLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- the owner's configured log path
	if err != nil {
		return nil, fmt.Errorf("open the model proxy request log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("read the model proxy request log: %w", err)
	}
	return &RequestLog{file: file, path: path, limit: limit, size: info.Size()}, nil
}

// Record writes one request. A write that fails is dropped: history is worth less than the answer in flight.
func (l *RequestLog) Record(record Record) {
	if l == nil {
		return
	}
	at := record.At
	if at.IsZero() {
		at = time.Now()
	}
	data, err := json.Marshal(line{
		At: at.UTC().Format(time.RFC3339), Outcome: bounded(record.Outcome), Detail: bounded(record.Detail), Session: bounded(record.Session),
		Model: bounded(record.Model), Effort: bounded(record.Effort), Status: record.Status,
		TTFBms: record.TTFB.Milliseconds(), DurationMs: record.Duration.Milliseconds(),
		InputTokens: record.Usage.InputTokens, CachedInputTokens: record.Usage.CacheReadInputTokens,
		OutputTokens: record.Usage.OutputTokens, Estimated: record.Usage.estimated, Error: bounded(record.Error),
	})
	if err != nil {
		return
	}
	data = append(data, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		if reopened, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil { // #nosec G304 -- the owner's configured log path
			l.file = reopened
			if info, err := reopened.Stat(); err == nil {
				l.size = info.Size()
			}
		} else {
			return
		}
	}
	if l.limit > 0 && l.size+int64(len(data)) > l.limit {
		l.rollOver()
		// Rotation failed and the file is already full: drop this record rather than grow without end.
		if l.size+int64(len(data)) > l.limit {
			return
		}
	}
	if l.file == nil {
		return
	}
	before := l.size
	written, err := l.file.Write(data)
	l.size += int64(written)
	if err != nil || written != len(data) {
		// A half-written line would join the next one and lose both, so cut back to the last whole record.
		if l.file.Truncate(before) == nil {
			l.size = before
		}
	}
}

// bounded keeps one field short enough that a record always fits a reader's line buffer, whatever a client sent
// as its model name.
func bounded(value string) string {
	const maxField = 300
	if len(value) <= maxField {
		return value
	}
	return value[:maxField] + "…"
}

// rollOver keeps one previous file. The caller holds the mutex. Whatever fails, it ends with a handle worth
// writing to when one can be opened, so a passing filesystem error does not disable history until a restart.
func (l *RequestLog) rollOver() {
	_ = l.file.Close()
	rotated := os.Rename(l.path, l.path+".1") == nil
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- the owner's configured log path
	if err != nil {
		// Nothing can be written now; the next Record tries again rather than writing to a closed file.
		l.file, l.size = nil, 0
		return
	}
	l.file = file
	if rotated {
		l.size = 0
		return
	}
	if info, err := file.Stat(); err == nil {
		l.size = info.Size()
	}
}

func (l *RequestLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}
