package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/badge"
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

	// Dedup by SessionID and filter ephemeral subagent processes
	seen := make(map[string]int) // sessionID -> index in deduped
	var deduped []agent.Agent
	for _, a := range agents {
		// Filter ephemeral automation subagents (session analyzers, hooks)
		// but keep sessions launched by aimux (tmux session starts with "aimux-")
		// and always keep remote sandbox agents (no cost/token data by design).
		if a.Location != "remote" && a.EstCostUSD == 0 && a.TokensIn < 1000 &&
			!strings.Contains(a.Model, "opus") &&
			!strings.HasPrefix(a.TMuxSession, "aimux-") {
			continue
		}
		if a.SessionID == "" {
			deduped = append(deduped, a)
			continue
		}
		if idx, ok := seen[a.SessionID]; ok {
			if a.StartTime.Before(deduped[idx].StartTime) {
				deduped[idx] = a
			}
			continue
		}
		seen[a.SessionID] = len(deduped)
		deduped = append(deduped, a)
	}

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
			enriched[i].Title = meta.Title
			if enriched[i].Title == "" {
				enriched[i].Title = firstPromptFromJSONL(a.SessionFile)
			}
			enriched[i].Starred = meta.Starred
		}
	}

	// Evaluate configurable badges for each agent.
	if len(s.cfg.Badges) > 0 {
		rules := make([]badge.Rule, len(s.cfg.Badges))
		for i, b := range s.cfg.Badges {
			rules[i] = badge.Rule{Path: b.Path, JSONPath: b.JSONPath, Label: b.Label, Color: b.Color}
		}
		for i := range enriched {
			if enriched[i].WorkingDir != "" {
				badges := badge.Evaluate(enriched[i].WorkingDir, rules)
				enriched[i].Badges = make([]agent.BadgeValue, len(badges))
				for j, b := range badges {
					enriched[i].Badges[j] = agent.BadgeValue{
						Label: b.Label, Value: b.Value, Color: b.Color,
					}
				}
			}
		}
	}

	data, err := json.Marshal(map[string]any{"agents": enriched})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: agents\ndata: %s\n\n", data)
	flusher.Flush()
}

func firstPromptFromJSONL(sessionFile string) string {
	f, err := os.Open(sessionFile) // #nosec G304
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

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
