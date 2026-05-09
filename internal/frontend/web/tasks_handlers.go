package web

import (
	"encoding/json"
	"net/http"

	"github.com/zanetworker/aimux/internal/debuglog"
)

func (s *Server) handleTaskLists(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	lists, err := s.taskProvider.ListTaskLists()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"lists": lists}); err != nil {
		debuglog.Log("encode task lists response: %v", err)
	}
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	listID := r.URL.Query().Get("list")
	if listID == "" {
		http.Error(w, "list parameter required", http.StatusBadRequest)
		return
	}
	items, err := s.taskProvider.ListTasks(listID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"tasks": items}); err != nil {
		debuglog.Log("encode tasks response: %v", err)
	}
}

func (s *Server) handleTaskComplete(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	taskID := r.PathValue("id")
	var req struct {
		ListID string `json:"listId"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.taskProvider.CompleteTask(req.ListID, taskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Note != "" {
		if err := s.taskProvider.AddNote(req.ListID, taskID, req.Note); err != nil {
			debuglog.Log("add task note failed: %v", err)
		}
	}
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "completed"}); err != nil {
		debuglog.Log("encode task complete response: %v", err)
	}
}

func (s *Server) handleTaskReopen(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	taskID := r.PathValue("id")
	var req struct {
		ListID string `json:"listId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.taskProvider.ReopenTask(req.ListID, taskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "reopened"}); err != nil {
		debuglog.Log("encode task reopen response: %v", err)
	}
}
