package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
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
	agents, err := s.cachedDiscover()
	if err != nil {
		return
	}

	// Drop ephemeral automation subagents, then one agent per session.
	deduped := controller.DedupBySession(controller.DropEphemeral(agents, time.Now()))
	controller.ApplyBadges(deduped, s.cfg.Badges)

	// Enrich with titles
	type enrichedAgent struct {
		agent.Agent
		Title   string
		Starred bool
	}

	enriched := make([]enrichedAgent, len(deduped))
	for i, a := range deduped {
		enriched[i] = enrichedAgent{Agent: a}
		if a.SessionFile != "" {
			meta := history.LoadMeta(a.SessionFile)
			// cards clamp to two lines; no need to send a whole first message
			enriched[i].Title = history.Shorten(history.ShownTitleForFileMeta(a.SessionFile, meta), 120)
			enriched[i].Starred = meta.Starred
		}
	}

	// hooksHint: why a permission prompt cannot read "needs you" (the
	// hooks are missing); "" once they are installed.
	hooksHint := ""
	if !s.activity.HooksInstalled() {
		hooksHint = controller.HooksHint
	}
	data, err := json.Marshal(map[string]any{"agents": enriched, "hooksHint": hooksHint})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: agents\ndata: %s\n\n", data)
	flusher.Flush()
}
