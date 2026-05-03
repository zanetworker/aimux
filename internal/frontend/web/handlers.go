package web

import (
	"encoding/json"
	"fmt"
	"net/http"
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

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	// Find agent by sessionID from current discovery
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

	if s.traceParseFn == nil {
		http.Error(w, "trace parsing not configured", http.StatusServiceUnavailable)
		return
	}

	turns, err := s.traceParseFn(sessionFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"turns": turns})
}
