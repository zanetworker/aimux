package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
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

	// Enrich with titles
	type enrichedAgent struct {
		agent.Agent
		Title string
	}

	enriched := make([]enrichedAgent, len(agents))
	for i, a := range agents {
		enriched[i] = enrichedAgent{Agent: a}
		if a.SessionFile != "" {
			enriched[i].Title = history.TitleForSessionFile(a.SessionFile)
		}
	}

	data, err := json.Marshal(map[string]any{"agents": enriched})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: agents\ndata: %s\n\n", data)
	flusher.Flush()
}
