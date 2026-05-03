package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
			if enriched[i].Title == "" {
				enriched[i].Title = firstPromptFromJSONL(a.SessionFile)
			}
		}
	}

	data, err := json.Marshal(map[string]any{"agents": enriched})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: agents\ndata: %s\n\n", data)
	flusher.Flush()
}

func firstPromptFromJSONL(sessionFile string) string {
	f, err := os.Open(sessionFile)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "user" && entry.Message != nil && entry.Message.Role == "user" {
			// Handle both string content and array content
			var text string

			// Try parsing as string first
			var contentStr string
			if err := json.Unmarshal(entry.Message.Content, &contentStr); err == nil {
				text = contentStr
			} else {
				// Try parsing as array of content blocks
				var contentBlocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(entry.Message.Content, &contentBlocks); err == nil {
					for _, block := range contentBlocks {
						if block.Type == "text" && block.Text != "" {
							text = block.Text
							break
						}
					}
				}
			}

			if len(text) > 120 {
				text = text[:117] + "..."
			}
			return text
		}
	}
	return ""
}
