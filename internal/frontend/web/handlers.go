package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type launchRequest struct {
	Provider string `json:"provider"`
	Dir      string `json:"dir"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req launchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if s.launchFn == nil {
		http.Error(w, "launch not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.launchFn(req.Provider, req.Dir, req.Model, req.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "launched"})
}

type annotateRequest struct {
	Turn  int    `json:"turn"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

func (s *Server) handleAnnotate(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req annotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if s.annotateFn == nil {
		http.Error(w, "annotate not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateFn(sessionID, req.Turn, req.Label, req.Note); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "annotated"})
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "archived"})
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "not implemented"})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
}

func (s *Server) handleTraceSubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "subscribed"})
}

func (s *Server) handleTraceUnsubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "unsubscribed"})
}

func (s *Server) handleFastTrace(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		http.Error(w, "missing file param", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(file, ".jsonl") {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}
	turns, err := parseTailTurns(file, 128*1024)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"turns": turns})
}

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if s.discoverFn == nil {
		http.Error(w, "discovery not configured", http.StatusServiceUnavailable)
		return
	}
	agents, err := s.discoverFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var sessionFile string
	for _, a := range agents {
		if a.SessionID == sessionID || fmt.Sprintf("%d", a.PID) == sessionID {
			sessionFile = a.SessionFile
			break
		}
	}
	if sessionFile == "" {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	turns, err := parseTailTurns(sessionFile, 128*1024)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"turns": turns})
}

// parseTailTurns reads the last tailBytes of a JSONL session file and
// extracts conversation turns directly, without the full provider parser.
func parseTailTurns(sessionFile string, tailBytes int64) ([]map[string]any, error) {
	f, err := os.Open(sessionFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	seeked := false
	if info.Size() > tailBytes {
		f.Seek(info.Size()-tailBytes, io.SeekStart)
		seeked = true
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	if seeked && len(lines) > 1 {
		lines = lines[1:] // skip partial first line
	}

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type messageEntry struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	}
	type toolEntry struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	}
	type entry struct {
		Type      string        `json:"type"`
		Subtype   string        `json:"subtype"`
		Message   *messageEntry `json:"message"`
		Tool      *toolEntry    `json:"tool"`
		Timestamp string        `json:"timestamp"`
	}

	type turn struct {
		Number    int
		Timestamp string
		UserText  string
		AgentText string
		Tools     []map[string]string
		TokensIn  int64
		TokensOut int64
		CostUSD   float64
	}

	var turns []turn
	var current *turn
	turnNum := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}

		if e.Type == "user" && e.Message != nil && e.Message.Role == "user" {
			turnNum++
			t := turn{Number: turnNum, Timestamp: e.Timestamp}
			t.UserText = extractText(e.Message.Content)
			turns = append(turns, t)
			current = &turns[len(turns)-1]
		} else if e.Type == "assistant" && e.Message != nil && current != nil {
			text := extractText(e.Message.Content)
			if text != "" {
				current.AgentText = text
			}
		} else if e.Type == "tool_use" && current != nil {
			name := ""
			snippet := ""
			if e.Tool != nil {
				name = e.Tool.Name
				snippet = e.Tool.Input
			}
			// Also try extracting from raw line
			if name == "" {
				var raw map[string]any
				json.Unmarshal([]byte(line), &raw)
				if n, ok := raw["name"].(string); ok {
					name = n
				}
				if s, ok := raw["input"].(string); ok && snippet == "" {
					snippet = s
				}
			}
			if len(snippet) > 60 {
				snippet = snippet[:57] + "..."
			}
			current.Tools = append(current.Tools, map[string]string{
				"name":    name,
				"snippet": snippet,
				"success": "true",
			})
		}
	}

	// Keep last 15 turns
	if len(turns) > 15 {
		turns = turns[len(turns)-15:]
	}

	result := make([]map[string]any, len(turns))
	for i, t := range turns {
		result[i] = map[string]any{
			"number":     t.Number,
			"timestamp":  t.Timestamp,
			"userText":   t.UserText,
			"outputText": t.AgentText,
			"actions":    t.Tools,
			"tokensIn":   t.TokensIn,
			"tokensOut":  t.TokensOut,
			"costUSD":    t.CostUSD,
		}
	}
	return result, nil
}

func extractText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	// Try string first
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	// Try array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil {
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}
