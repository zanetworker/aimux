package browserhost

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

// The panel's both lane asks under research-with-work, and the turn's
// links reach the panel on its done event.
func TestRelay_BothLaneUsesResearchWithWork(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	var mu sync.Mutex
	var profile string
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		profile, _ = b["profile"].(string)
		mu.Unlock()
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "S1", "turn": "T1", "after": 0, "events": "/v1/sessions/S1/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEv{1, "T1", "turn_done", map[string]any{"stopReason": "end_turn", "links": []string{"https://github.com/NVIDIA/OpenShell/releases"}}})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "q1", Type: "ask", Lane: "both", URL: "https://a.example/", Question: "compare"})
	evs := p.until(t, "q1")
	mu.Lock()
	got := profile
	mu.Unlock()
	if got != "research-with-work" {
		t.Fatalf("both lane asked under %q", got)
	}
	last := evs[len(evs)-1]
	if last.Type != "done" || !reflect.DeepEqual(last.Links, []string{"https://github.com/NVIDIA/OpenShell/releases"}) {
		t.Fatalf("last event %+v, want done with the links", last)
	}
}

// A done event without links (any other lane) carries none.
func TestRelay_ForwardsLinks(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "S1", "turn": "T1", "after": 0, "events": "/v1/sessions/S1/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEv{1, "T1", "turn_done", map[string]any{"stopReason": "end_turn"}})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "q1", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	evs := p.until(t, "q1")
	if last := evs[len(evs)-1]; last.Type != "done" || last.Links != nil {
		t.Fatalf("last event %+v, want done without links", last)
	}
}

// A history turn's link set reaches the panel, so a reopened Both answer
// keeps its fetched links.
func TestRelay_HistoryForwardsTurnLinks(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	const pageURL = "https://a.example/both"
	f.mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"sessions": []any{session("r1", "research-with-work", pageURL, "A", t0)}})
	})
	f.mux.HandleFunc("GET /v1/sessions/r1", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"id": "r1", "turns": []any{
			map[string]any{"number": 1, "time": t0, "user": "compare?", "reply": "See [x](https://github.com/x).", "tools": []any{}, "links": []any{"https://github.com/x"}},
		}})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "g", Type: "history.get", URL: pageURL})
	e := p.first(t, "g")
	if e.Page == nil || len(e.Page.Turns) != 1 || e.Page.Turns[0].Lane != "both" {
		t.Fatalf("event = %+v", e)
	}
	if !reflect.DeepEqual(e.Page.Turns[0].Links, []string{"https://github.com/x"}) {
		t.Fatalf("links = %v", e.Page.Turns[0].Links)
	}
}
