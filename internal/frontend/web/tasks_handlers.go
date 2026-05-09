package web

import (
	"encoding/json"
	"net/http"
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"lists": lists})
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"tasks": items})
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
		_ = s.taskProvider.AddNote(req.ListID, taskID, req.Note)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "completed"})
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
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "reopened"})
}
