package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/zanetworker/aimux/internal/history"
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
	sessionID := r.PathValue("id")

	if s.discoverFn == nil || s.killFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	agents, err := s.discoverFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, a := range agents {
		if a.SessionID == sessionID || fmt.Sprintf("%d", a.PID) == sessionID {
			if err := s.killFn(a.PID, a.TMuxSession); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "killed"})
			return
		}
	}
	http.Error(w, "agent not found", http.StatusNotFound)
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

	type turn struct {
		Number    int
		Timestamp string
		UserText  string
		AgentText string
		Tools     []map[string]string
		TokensIn  int64
		TokensOut int64
		CostUSD   float64
		Model     string
	}

	var turns []turn
	var current *turn
	turnNum := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		var entryType string
		json.Unmarshal(raw["type"], &entryType)

		var ts string
		json.Unmarshal(raw["timestamp"], &ts)

		msgRaw := raw["message"]
		if msgRaw == nil {
			continue
		}
		var msgObj map[string]json.RawMessage
		if err := json.Unmarshal(msgRaw, &msgObj); err != nil {
			continue
		}

		if entryType == "user" {
			contentRaw := msgObj["content"]
			if contentRaw == nil {
				continue
			}
			userText := extractText(contentRaw)
			if userText == "" {
				continue
			}
			if current != nil {
				turns = append(turns, *current)
			}
			turnNum++
			current = &turn{Number: turnNum, Timestamp: ts, UserText: userText}
		} else if entryType == "assistant" && current != nil {
			var usage struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			}
			if usageRaw := msgObj["usage"]; usageRaw != nil {
				json.Unmarshal(usageRaw, &usage)
				current.TokensIn += usage.InputTokens
				current.TokensOut += usage.OutputTokens
			}

			var model string
			if modelRaw := msgObj["model"]; modelRaw != nil {
				json.Unmarshal(modelRaw, &model)
				if model != "" && current.Model == "" {
					current.Model = model
				}
			}

			contentRaw := msgObj["content"]
			if contentRaw == nil {
				continue
			}

			var blocks []map[string]interface{}
			if err := json.Unmarshal(contentRaw, &blocks); err != nil {
				continue
			}

			for _, block := range blocks {
				blockType, _ := block["type"].(string)
				switch blockType {
				case "text":
					text, _ := block["text"].(string)
					text = strings.TrimSpace(text)
					if text != "" {
						if current.AgentText != "" {
							current.AgentText += "\n" + text
						} else {
							current.AgentText = text
						}
					}
				case "tool_use":
					name, _ := block["name"].(string)
					tool := map[string]string{
						"name":    name,
						"success": "true",
					}
					if input, ok := block["input"].(map[string]interface{}); ok {
						tool["snippet"] = toolSnippet(name, input)
						fillToolDetail(tool, name, input)
					}
					current.Tools = append(current.Tools, tool)
				}
			}
		}
	}

	if current != nil {
		turns = append(turns, *current)
	}

	if len(turns) > 30 {
		turns = turns[len(turns)-30:]
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
			"model":      t.Model,
		}
	}
	return result, nil
}

func toolSnippet(name string, input map[string]interface{}) string {
	switch name {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return "$ " + cmd
		}
	case "Read":
		if p, ok := input["file_path"].(string); ok {
			return p
		}
	case "Edit":
		if p, ok := input["file_path"].(string); ok {
			return p
		}
	case "Write":
		if p, ok := input["file_path"].(string); ok {
			return p
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Agent":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	}
	return ""
}

func fillToolDetail(tool map[string]string, name string, input map[string]interface{}) {
	if fp, ok := input["file_path"].(string); ok {
		tool["filePath"] = fp
	}
	switch name {
	case "Edit":
		if old, ok := input["old_string"].(string); ok {
			tool["oldString"] = truncate(old, 500)
		}
		if ns, ok := input["new_string"].(string); ok {
			tool["newString"] = truncate(ns, 500)
		}
	case "Write":
		if c, ok := input["content"].(string); ok {
			tool["content"] = truncate(c, 300)
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			tool["command"] = cmd
		}
		if desc, ok := input["description"].(string); ok {
			tool["description"] = desc
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			tool["pattern"] = p
		}
		if p, ok := input["path"].(string); ok {
			tool["searchPath"] = p
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			tool["pattern"] = p
		}
	case "Agent":
		if desc, ok := input["description"].(string); ok {
			tool["description"] = desc
		}
		if p, ok := input["prompt"].(string); ok {
			tool["prompt"] = truncate(p, 300)
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		return
	}
	matches, err := history.SearchContentWithSnippets(q, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results := make([]map[string]string, len(matches))
	for i, m := range matches {
		results[i] = map[string]string{
			"sessionId": m.SessionID,
			"filePath":  m.FilePath,
			"snippet":   m.Snippet,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
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
