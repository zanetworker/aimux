package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
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
	dir := r.URL.Query().Get("dir")
	opts := history.DiscoverOpts{Dir: dir}
	sessions, err := history.Discover(opts, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []history.Session{}
	}

	result := make([]map[string]any, len(sessions))
	for i, s := range sessions {
		result[i] = map[string]any{
			"id":          s.ID,
			"provider":    s.Provider,
			"project":     s.Project,
			"filePath":    s.FilePath,
			"startTime":   s.StartTime.Format(time.RFC3339),
			"lastActive":  s.LastActive.Format(time.RFC3339),
			"turnCount":   s.TurnCount,
			"tokensIn":    s.TokensIn,
			"tokensOut":   s.TokensOut,
			"costUSD":     s.CostUSD,
			"firstPrompt": s.FirstPrompt,
			"title":       s.Title,
			"resumable":   s.Resumable,
			"annotation":  s.Annotation,
			"tags":        s.Tags,
			"isSubagent":  s.IsSubagent,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"sessions": result})
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
	if !strings.HasSuffix(file, ".jsonl") && !strings.HasSuffix(file, ".json") {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		providerName = "claude"
	}
	if s.providerLookupFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	p := s.providerLookupFn(providerName)
	if p == nil {
		http.Error(w, "unknown provider", http.StatusInternalServerError)
		return
	}
	turns, err := p.ParseTrace(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"turns": turnsToJSON(turns)})
}

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if s.discoverFn == nil || s.providerLookupFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	agents, err := s.discoverFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var sessionFile, providerName string
	for _, a := range agents {
		if a.SessionID == sessionID || fmt.Sprintf("%d", a.PID) == sessionID {
			sessionFile = a.SessionFile
			providerName = a.ProviderName
			break
		}
	}
	if sessionFile == "" {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	p := s.providerLookupFn(providerName)
	if p == nil {
		http.Error(w, "unknown provider", http.StatusInternalServerError)
		return
	}
	turns, err := p.ParseTrace(sessionFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"turns": turnsToJSON(turns)})
}

func turnsToJSON(turns []trace.Turn) []map[string]any {
	result := make([]map[string]any, len(turns))
	for i, t := range turns {
		actions := make([]map[string]any, len(t.Actions))
		for j, a := range t.Actions {
			action := map[string]any{
				"name":     a.Name,
				"snippet":  a.Snippet,
				"success":  a.Success,
				"errorMsg": a.ErrorMsg,
			}
			if a.OldString != "" {
				action["oldString"] = a.OldString
			}
			if a.NewString != "" {
				action["newString"] = a.NewString
			}
			actions[j] = action
		}
		result[i] = map[string]any{
			"number":     t.Number,
			"timestamp":  t.Timestamp.Format(time.RFC3339),
			"userText":   strings.Join(t.UserLines, "\n"),
			"outputText": strings.Join(t.OutputLines, "\n"),
			"actions":    actions,
			"tokensIn":   t.TokensIn,
			"tokensOut":  t.TokensOut,
			"costUSD":    t.CostUSD,
			"model":      t.Model,
		}
	}
	return result
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
