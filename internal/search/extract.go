// Package search builds a persistent, ranked index over Claude Code session
// transcripts: full-text (SQLite FTS5, BM25) plus optional embeddings for
// semantic ranking. Automated sessions (SDK/cron runs) are flagged so they can
// be filtered out of results.
package search

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Doc is one session transcript reduced to searchable text.
type Doc struct {
	SessionID   string
	Path        string
	CWD         string
	Title       string // custom title > latest ai-title > first prompt
	FirstPrompt string
	Automated   bool
	ModTime     time.Time
	Chunks      []Chunk
}

// Chunk is one exchange: a human message plus the assistant text, tool calls
// and tool output that followed it.
type Chunk struct {
	Seq    int
	Prompt string // the human message that opened the exchange, one line
	Text   string // searchable: messages, tool calls, trimmed tool output (first MaxChunkChars)
	More   string // the rest of a long exchange (up to MaxTextChars), searched at a lower weight
	Prose  string // only what was said (human + assistant text), for embeddings
}

// ExtractOpts controls what gets indexed and how much of it.
type ExtractOpts struct {
	AutomatedPrefixes  []string // first-prompt prefixes that mark a session as automated
	MaxChunkChars      int      // embedding budget; prose is kept within half of it
	MaxTextChars       int      // searchable text per exchange (full-text index)
	MaxToolOutputChars int
}

// DefaultExtractOpts returns limits sized for embedding (~1k tokens per chunk).
func DefaultExtractOpts() ExtractOpts {
	return ExtractOpts{
		AutomatedPrefixes: []string{
			"YOU ARE A SESSION ANALYZER", "Evaluate session", "Tag each library item",
			"LINKS ARE MANDATORY", "Read the JSON file", "Run a prompt audit",
		},
		MaxChunkChars:      4000,
		MaxTextChars:       64000,
		MaxToolOutputChars: 300,
	}
}

var tempDirPrefixes = []string{"/private/var/folders/", "/var/folders/", "/tmp/", "/private/tmp/"}

// tool_use input fields worth indexing; bulky fields (old_string, content) are skipped.
var toolInputFields = []string{"command", "file_path", "path", "pattern", "query", "url", "description", "prompt"}

type record struct {
	Type        string `json:"type"`
	Entrypoint  string `json:"entrypoint"`
	CWD         string `json:"cwd"`
	IsMeta      bool   `json:"isMeta"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type part struct {
	Type    string                 `json:"type"`
	Text    string                 `json:"text"`
	Name    string                 `json:"name"`
	Input   map[string]interface{} `json:"input"`
	Content json.RawMessage        `json:"content"`
}

// ExtractFile parses a session JSONL file. Malformed lines are skipped.
func ExtractFile(path string, opts ExtractOpts) (Doc, error) {
	f, err := os.Open(path) // #nosec G304 -- caller passes session files under the projects dir
	if err != nil {
		return Doc{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Doc{}, err
	}

	doc := Doc{SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Path: path, ModTime: st.ModTime()}
	var cur *strings.Builder
	var curPrompt string
	var prose *strings.Builder
	var aiTitle, customTitle, entrypoint string

	flush := func() {
		if cur != nil && cur.Len() > 0 {
			full := strings.TrimSpace(cur.String())
			text := truncate(full, opts.MaxChunkChars)
			more := strings.TrimSpace(truncate(full[len(text):], max(opts.MaxTextChars-len(text), 0)))
			doc.Chunks = append(doc.Chunks, Chunk{Seq: len(doc.Chunks), Prompt: curPrompt, Text: text, More: more,
				Prose: truncate(strings.TrimSpace(prose.String()), opts.MaxChunkChars/2)})
		}
		cur = nil
	}
	addProse := func(s string) {
		if s = strings.TrimSpace(s); s == "" || prose == nil {
			return
		}
		if prose.Len() > 0 {
			prose.WriteString("\n")
		}
		prose.WriteString(s)
	}
	add := func(s string) {
		if s = strings.TrimSpace(s); s == "" || cur == nil {
			return
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(s)
	}

	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			var rec record
			if json.Unmarshal(line, &rec) == nil {
				if doc.CWD == "" && rec.CWD != "" {
					doc.CWD = rec.CWD
				}
				if entrypoint == "" && rec.Entrypoint != "" {
					entrypoint = rec.Entrypoint
				}
				switch rec.Type {
				case "ai-title":
					if rec.AITitle != "" {
						aiTitle = rec.AITitle
					}
				case "custom-title":
					if rec.CustomTitle != "" {
						customTitle = rec.CustomTitle
					}
				case "user":
					if rec.IsMeta {
						break
					}
					if text, human := humanText(rec.Message.Content); human {
						flush()
						cur, prose = &strings.Builder{}, &strings.Builder{}
						curPrompt = oneLine(text, 200)
						addProse(text)
						if doc.FirstPrompt == "" {
							doc.FirstPrompt = oneLine(text, 120)
						}
						add(text)
					} else {
						add(truncate(toolResultText(rec.Message.Content), opts.MaxToolOutputChars))
					}
				case "assistant":
					add(assistantText(rec.Message.Content))
					addProse(assistantProse(rec.Message.Content))
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return Doc{}, readErr
		}
	}
	flush()

	switch {
	case customTitle != "":
		doc.Title = customTitle
	case aiTitle != "":
		doc.Title = aiTitle
	default:
		doc.Title = doc.FirstPrompt
	}
	doc.Automated = isAutomated(entrypoint, doc.CWD, doc.FirstPrompt, opts.AutomatedPrefixes)
	return doc, nil
}

// humanText returns the typed text of a user record and whether it is a human
// turn (as opposed to tool output or a slash-command echo).
func humanText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t := strings.TrimSpace(s)
		for _, p := range []string{"<command-", "<local-command", "<task-notification", "<system-reminder"} {
			if strings.HasPrefix(t, p) {
				return "", false
			}
		}
		return t, t != ""
	}
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var texts []string
	for _, p := range parts {
		switch p.Type {
		case "tool_result":
			return "", false
		case "text":
			texts = append(texts, p.Text)
		}
	}
	t := strings.TrimSpace(strings.Join(texts, " "))
	return t, t != ""
}

func toolResultText(raw json.RawMessage) string {
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type != "tool_result" {
			continue
		}
		var s string
		if json.Unmarshal(p.Content, &s) == nil {
			out = append(out, s)
			continue
		}
		var inner []part
		if json.Unmarshal(p.Content, &inner) == nil {
			for _, in := range inner {
				if in.Type == "text" {
					out = append(out, in.Text)
				}
			}
		}
	}
	return strings.Join(out, " ")
}

func assistantText(raw json.RawMessage) string {
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, p.Text)
		case "tool_use":
			fields := []string{p.Name}
			for _, k := range toolInputFields {
				if v, ok := p.Input[k].(string); ok && v != "" {
					fields = append(fields, v)
				}
			}
			out = append(out, strings.Join(fields, " "))
		}
	}
	return strings.Join(out, "\n")
}

// assistantProse keeps only the assistant's text parts (no tool calls).
func assistantProse(raw json.RawMessage) string {
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type == "text" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

func isAutomated(entrypoint, cwd, firstPrompt string, prefixes []string) bool {
	if strings.HasPrefix(entrypoint, "sdk") {
		return true
	}
	for _, p := range tempDirPrefixes {
		if strings.HasPrefix(cwd, p) {
			return true
		}
	}
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(firstPrompt, p) {
			return true
		}
	}
	return false
}

func oneLine(s string, max int) string {
	return truncate(strings.Join(strings.Fields(s), " "), max)
}

// truncate cuts s to at most max bytes without splitting a UTF-8 rune.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "")
}
