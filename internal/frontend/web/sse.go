package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	s.sendAgentEvent(w, flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			s.sendAgentEvent(w, flusher)
		}
	}
}

func (s *Server) sendAgentEvent(w http.ResponseWriter, flusher http.Flusher) {
	if s.discoverFn == nil {
		return
	}
	agents, err := s.discoverFn()
	if err != nil {
		return
	}
	data, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: agents\ndata: %s\n\n", data)
	flusher.Flush()
}
