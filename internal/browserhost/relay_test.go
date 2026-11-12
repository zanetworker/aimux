package browserhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/profilerun"
)

// v1Call is one request the fake v1 server received.
type v1Call struct {
	Method, Path, Query, Host string
	Body                      map[string]any
}

// fakeV1 is a v1 server on a unix socket that records every request.
type fakeV1 struct {
	mu    sync.Mutex
	calls []v1Call
	mux   *http.ServeMux
}

func newFakeV1() *fakeV1 { return &fakeV1{mux: http.NewServeMux()} }

func (f *fakeV1) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := v1Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Host: r.Host}
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c.Body) // GETs have no body
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	f.mux.ServeHTTP(w, r)
}

func (f *fakeV1) find(method, path string) []v1Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []v1Call
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// sockPath is a socket path short enough for macOS (104 bytes).
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "wingman.sock")
}

// listenV1 serves h on sock until the test ends.
func listenV1(t *testing.T, sock string, h http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// sseEv is one v1 event written by writeSSE.
type sseEv struct {
	Seq        int64
	Turn, Kind string
	Data       any
}

func writeSSE(w http.ResponseWriter, evs ...sseEv) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, e := range evs {
		data, _ := json.Marshal(map[string]any{"seq": e.Seq, "turn": e.Turn, "kind": e.Kind, "time": time.Unix(0, 0), "data": e.Data})
		_, _ = fmt.Fprintf(w, ": keepalive\n\nid: L.%d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, data)
	}
	w.(http.Flusher).Flush()
}

func chunk(text string) map[string]any {
	return map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// panel is a fake side panel connected to a running relay.
type panel struct {
	in     *io.PipeWriter
	events chan Event
	done   chan error
}

func startPanel(t *testing.T, r *Relay) *panel {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &panel{in: inW, events: make(chan Event, 100), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		p.done <- r.Serve(ctx, inR, outW)
		_ = inR.Close() // a later send fails instead of blocking
		_ = outW.Close()
	}()
	go func() {
		for {
			msg, err := ReadMessage(outR)
			if err != nil {
				close(p.events)
				return
			}
			var e Event
			if err := json.Unmarshal(msg, &e); err != nil {
				t.Errorf("bad event %q: %v", msg, err)
			}
			p.events <- e
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		cancel()
	})
	return p
}

func (p *panel) send(t *testing.T, req Request) {
	t.Helper()
	b, _ := json.Marshal(req)
	if err := WriteMessage(p.in, b); err != nil {
		t.Logf("send %s: %v", req.ID, err) // the relay is gone; until reports it
	}
}

// until returns id's events up to and including its first done or error.
func (p *panel) until(t *testing.T, id string) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-p.events:
			if !ok {
				t.Fatalf("panel closed; got %+v", out)
			}
			if e.ID != id {
				continue
			}
			out = append(out, e)
			if e.Type == "done" || e.Type == "error" {
				return out
			}
		case <-timeout:
			t.Fatalf("no terminal event for %s; got %+v", id, out)
		}
	}
}

// first returns id's first event.
func (p *panel) first(t *testing.T, id string) Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-p.events:
			if !ok {
				t.Fatal("panel closed")
			}
			if e.ID == id {
				return e
			}
		case <-timeout:
			t.Fatalf("no event for %s", id)
		}
	}
}

func TestRelay_AskPageMapsToProfileSession(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "S1", "turn": "T1", "after": 3, "events": "/v1/sessions/S1/events?after=3"})
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			sseEv{4, "T1", "turn_started", map[string]any{"text": "what?"}},
			sseEv{5, "T0", "update", chunk("OLD")}, // another turn: not this ask's
			sseEv{6, "T1", "update", chunk("Hel")},
			sseEv{7, "T1", "update", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "wiki_search"}},
			sseEv{8, "T1", "update", chunk("lo")},
			sseEv{9, "T1", "turn_done", map[string]any{"stopReason": "end_turn"}},
		)
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "a1", Type: "ask", Lane: "page", URL: "https://a.example/x#sec", Title: "T", Page: "PAGE", Question: "what?"})
	evs := p.until(t, "a1")

	want := []Event{{ID: "a1", Type: "delta", Text: "Hel"}, {ID: "a1", Type: "tool_call", Tool: "wiki_search"}, {ID: "a1", Type: "delta", Text: "lo"}, {ID: "a1", Type: "done"}}
	if fmt.Sprint(evs) != fmt.Sprint(want) {
		t.Fatalf("events = %+v, want %+v", evs, want)
	}
	posts := f.find("POST", "/v1/sessions")
	if len(posts) != 1 {
		t.Fatalf("POST /v1/sessions calls = %d", len(posts))
	}
	b := posts[0].Body
	if b["profile"] != "page" || b["url"] != "https://a.example/x" || b["title"] != "T" || b["pageText"] != "PAGE" || b["text"] != "what?" {
		t.Fatalf("body = %v", b)
	}
	if posts[0].Host != "aimux.local" {
		t.Fatalf("Host = %q", posts[0].Host)
	}
	gets := f.find("GET", "/v1/sessions/S1/events")
	if len(gets) != 1 || gets[0].Query != "after=3" {
		t.Fatalf("events calls = %+v", gets)
	}
}

// acceptOnce answers POST /v1/sessions with session S1 turn T1 and streams
// a short answer.
func acceptOnce(f *fakeV1) {
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "S1", "turn": "T1", "after": 0, "events": "/v1/sessions/S1/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEv{1, "T1", "update", chunk("ok")}, sseEv{2, "T1", "turn_done", map[string]any{"stopReason": "end_turn"}})
	})
}

func TestRelay_AskWorkUsesWorkReadonly(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	acceptOnce(f)
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "w1", Type: "ask", Lane: "work", URL: "https://a.example/", Question: "q"})
	if evs := p.until(t, "w1"); evs[len(evs)-1].Type != "done" {
		t.Fatalf("events = %+v", evs)
	}
	posts := f.find("POST", "/v1/sessions")
	if len(posts) != 1 || posts[0].Body["profile"] != "work-readonly" {
		t.Fatalf("posts = %+v", posts)
	}
	// No lane or an unknown one is an error; nothing reaches the server.
	for id, lane := range map[string]string{"n1": "", "n2": "host"} {
		p.send(t, Request{ID: id, Type: "ask", Lane: lane, URL: "https://a.example/", Question: "q"})
		if evs := p.until(t, id); evs[0].Type != "error" {
			t.Fatalf("lane %q: %+v", lane, evs)
		}
	}
	if n := len(f.find("POST", "/v1/sessions")); n != 1 {
		t.Fatalf("posts after bad lanes = %d", n)
	}
}

// v1 error bodies reach the panel as error events: plain text for a
// refused profile, the server's message otherwise. Also a turn_error.
func TestRelay_ErrorsShowServerMessage(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		switch b["text"] {
		case "forbidden":
			writeJSONStatus(w, http.StatusForbidden, map[string]any{"error": "this client may not use that profile", "code": "profile_not_allowed"})
		case "unknown":
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no profile by that name (GET /v1/profiles lists them)", "code": "unknown_profile"})
		default:
			writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "S1", "turn": "T1", "after": 0, "events": "/v1/sessions/S1/events?after=0"})
		}
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEv{1, "T1", "update", chunk("part")}, sseEv{2, "T1", "turn_error", map[string]any{"error": "the adapter exited"}})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	for id, want := range map[string]string{
		"forbidden": "Wingman is not allowed to use this profile",
		"unknown":   "no profile by that name (GET /v1/profiles lists them)",
		"turnfail":  "the adapter exited",
	} {
		p.send(t, Request{ID: id, Type: "ask", Lane: "page", URL: "https://a.example/", Question: id})
		evs := p.until(t, id)
		if last := evs[len(evs)-1]; last.Type != "error" || last.Error != want {
			t.Errorf("%s: events = %+v, want error %q", id, evs, want)
		}
	}
}

func TestRelay_LimitErrorShown(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		writeJSONStatus(w, http.StatusTooManyRequests, map[string]any{"error": "this client is running as many questions as it may; try again shortly", "code": "client_limit"})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "l1", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	evs := p.until(t, "l1")
	if len(evs) != 1 || evs[0].Error != "Too many Wingman questions at once; try again in a moment" {
		t.Fatalf("events = %+v", evs)
	}
}

// cancel posts the v1 cancel for the ask's session and answers done once;
// nothing more arrives for that id. Cancelling an unknown id is done.
func TestRelay_CancelMaps(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	streaming := make(chan struct{})
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "S1", "turn": "T1", "after": 0, "events": "/v1/sessions/S1/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEv{1, "T1", "update", chunk("thinking")})
		close(streaming)
		<-r.Context().Done()
	})
	cancelled := make(chan struct{}, 1)
	f.mux.HandleFunc("POST /v1/sessions/S1/cancel", func(w http.ResponseWriter, r *http.Request) {
		cancelled <- struct{}{}
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "cancelling"})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "a1", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	<-streaming
	p.send(t, Request{ID: "a1", Type: "cancel"})
	evs := p.until(t, "a1")
	if last := evs[len(evs)-1]; last.Type != "done" {
		t.Fatalf("events = %+v", evs)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("no POST /v1/sessions/S1/cancel")
	}
	p.send(t, Request{ID: "zz", Type: "cancel"})
	for _, e := range p.until(t, "zz") {
		if e.Type != "done" {
			t.Fatalf("unknown id cancel: %+v", e)
		}
	}
	select {
	case e := <-p.events:
		t.Fatalf("event after cancel: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

// session is a v1 session list entry started under a profile.
func session(id, profile, pageURL, title string, at time.Time) map[string]any {
	return map[string]any{"id": id, "provider": "claude", "title": title, "lastActive": at, "state": "idle",
		"profile": profile, "client": "wingman", "page": map[string]any{"url": pageURL, "title": title}}
}

func TestRelay_HistoryListGroupsByURL(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	f.mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"sessions": []any{
			session("w1", "work-readonly", "https://a.example/x", "A new", t0.Add(3*time.Hour)),
			session("p2", "page", "https://b.example/", "Bee", t0.Add(2*time.Hour)),
			session("p1", "page", "https://a.example/x", "A old", t0),
			map[string]any{"id": "nopage", "provider": "claude", "title": "t", "lastActive": t0, "state": "idle"},
		}})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "h1", Type: "history.list"})
	e := p.first(t, "h1")
	if e.Type != "history" || len(e.Pages) != 2 {
		t.Fatalf("event = %+v", e)
	}
	a, b := e.Pages[0], e.Pages[1]
	if a.URL != "https://a.example/x" || a.Title != "A new" || !a.FirstAsked.Equal(t0) || !a.LastAsked.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("page a = %+v", a)
	}
	if b.URL != "https://b.example/" || !b.LastAsked.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("page b = %+v", b)
	}
	gets := f.find("GET", "/v1/sessions")
	if len(gets) != 1 || !strings.Contains(gets[0].Query, "client=wingman") {
		t.Fatalf("list calls = %+v", gets)
	}

	// A query filters on title or url, case-insensitively.
	p.send(t, Request{ID: "h2", Type: "history.list", Query: "BEE"})
	if e := p.first(t, "h2"); len(e.Pages) != 1 || e.Pages[0].URL != "https://b.example/" {
		t.Fatalf("filtered = %+v", e)
	}
}

func TestRelay_HistoryGetMergesTurns(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	const pageURL = "https://a.example/x"
	f.mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") != pageURL {
			writeJSONStatus(w, http.StatusOK, map[string]any{"sessions": []any{}})
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{"sessions": []any{
			session("w1", "work-readonly", pageURL, "A", t0.Add(time.Hour)),
			session("gone", "page", pageURL, "A", t0.Add(30*time.Minute)),
			session("p1", "page", pageURL, "A", t0),
		}})
	})
	turn := func(n int, at time.Time, user, reply string, tools ...string) map[string]any {
		ts := []any{}
		for _, name := range tools {
			ts = append(ts, map[string]any{"name": name, "ok": true})
		}
		return map[string]any{"number": n, "time": at, "user": user, "reply": reply, "tools": ts}
	}
	pagePrompt := profilerun.Prompt(profilerun.AskRequest{URL: pageURL, Title: "A", Page: "Question: not this one", Question: "first?"})
	f.mux.HandleFunc("GET /v1/sessions/p1", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"id": "p1", "turns": []any{
			turn(1, t0, pagePrompt, "A1"),
			turn(2, t0.Add(2*time.Hour), profilerun.Prompt(profilerun.AskRequest{Question: "third?"}), "A3"),
		}})
	})
	f.mux.HandleFunc("GET /v1/sessions/w1", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"id": "w1", "turns": []any{
			turn(1, t0.Add(time.Hour), "second?", "A2", "jira_search"), // the engine's own framing is covered in profilerun
		}})
	})
	f.mux.HandleFunc("GET /v1/sessions/gone", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "no such session", "code": "unknown_session"})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "g1", Type: "history.get", URL: pageURL + "#frag"})
	e := p.first(t, "g1")
	if e.Type != "history" || e.Page == nil {
		t.Fatalf("event = %+v", e)
	}
	var got []string
	for _, tr := range e.Page.Turns {
		got = append(got, tr.Lane+"|"+tr.Question+"|"+tr.Answer+"|"+strings.Join(tr.ToolCalls, ","))
	}
	want := []string{"page|first?|A1|", "work|second?|A2|jira_search", "page|third?|A3|"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("turns =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if e.Page.URL != pageURL || e.Page.PageSessionID != "gone" || e.Page.WorkSessionID != "w1" {
		t.Fatalf("page = %+v", e.Page)
	}
	for _, c := range f.find("GET", "/v1/sessions/p1") {
		if c.Query != "turns=50" {
			t.Fatalf("detail query = %q", c.Query)
		}
	}

	// A page with no sessions: history with page null, then done.
	p.send(t, Request{ID: "g2", Type: "history.get", URL: "https://none.example/"})
	if e := p.first(t, "g2"); e.Type != "history" || e.Page != nil {
		t.Fatalf("missing page = %+v", e)
	}
	if e := p.first(t, "g2"); e.Type != "done" {
		t.Fatalf("missing page second event = %+v", e)
	}
}

func TestCapPage(t *testing.T) {
	mk := func(n, size int) Page {
		p := Page{URL: "u"}
		for i := 0; i < n; i++ {
			p.Turns = append(p.Turns, Turn{Question: fmt.Sprint(i), Answer: strings.Repeat("x", size)})
		}
		return p
	}
	if p := capPage(mk(maxHistoryTurns, 10)); len(p.Turns) != maxHistoryTurns || p.Truncated {
		t.Fatalf("at the cap: %d turns, truncated %v", len(p.Turns), p.Truncated)
	}
	if p := capPage(mk(maxHistoryTurns+1, 10)); len(p.Turns) != maxHistoryTurns || !p.Truncated || p.Turns[0].Question != "1" {
		t.Fatalf("over the cap: %d turns, truncated %v, first %q", len(p.Turns), p.Truncated, p.Turns[0].Question)
	}
	p := capPage(mk(5, 200*1024)) // 1 MB of answers: only the newest fit
	b, _ := json.Marshal(p)
	if len(b) > maxHistoryBytes || !p.Truncated || len(p.Turns) == 0 || p.Turns[len(p.Turns)-1].Question != "4" {
		t.Fatalf("bytes: %d, truncated %v, turns %d", len(b), p.Truncated, len(p.Turns))
	}
}

// history.delete hides every Wingman session on the page, then answers done.
func TestRelay_HistoryDeleteHidesPageSessions(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	const pageURL = "https://a.example/x"
	t0 := time.Unix(1000, 0)
	f.mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"sessions": []any{
			session("p1", "page", pageURL, "A", t0), session("w1", "work-readonly", pageURL, "A", t0),
		}})
	})
	f.mux.HandleFunc("PATCH /v1/sessions/{id}/meta", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, map[string]any{"title": "A", "hidden": true})
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "d1", Type: "history.delete", URL: pageURL})
	if evs := p.until(t, "d1"); len(evs) != 1 || evs[0].Type != "done" {
		t.Fatalf("events = %+v", evs)
	}
	for _, id := range []string{"p1", "w1"} {
		c := f.find("PATCH", "/v1/sessions/"+id+"/meta")
		if len(c) != 1 || c[0].Body["hidden"] != true {
			t.Fatalf("%s meta calls = %+v", id, c)
		}
	}
	if q := f.find("GET", "/v1/sessions")[0].Query; !strings.Contains(q, "url=https%3A%2F%2Fa.example%2Fx") {
		t.Fatalf("list query = %q", q)
	}
}

// Two relays that find no server start it once and both get answers; a
// failing start reaches the panel as an error.
func TestRelay_StartsServerOnceWhenSocketMissing(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	acceptOnce(f)
	var mu sync.Mutex
	starts := 0
	srv := &http.Server{Handler: f, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = srv.Close() })
	start := func(context.Context) error {
		mu.Lock()
		starts++
		mu.Unlock()
		go func() {
			time.Sleep(50 * time.Millisecond) // a server takes a moment to listen
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Errorf("listen: %v", err)
				return
			}
			_ = srv.Serve(ln)
		}()
		return nil
	}
	a := startPanel(t, &Relay{Socket: sock, Start: start})
	b := startPanel(t, &Relay{Socket: sock, Start: start})
	a.send(t, Request{ID: "a", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	b.send(t, Request{ID: "b", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	for id, p := range map[string]*panel{"a": a, "b": b} {
		if evs := p.until(t, id); evs[len(evs)-1].Type != "done" {
			t.Fatalf("%s: %+v", id, evs)
		}
	}
	mu.Lock()
	n := starts
	mu.Unlock()
	if n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}

	failing := startPanel(t, &Relay{Socket: sockPath(t), Start: func(context.Context) error { return errors.New("exec: no such file") }})
	failing.send(t, Request{ID: "f", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	evs := failing.until(t, "f")
	if last := evs[len(evs)-1]; last.Type != "error" || !strings.Contains(last.Error, "aimux remote serve") || !strings.Contains(last.Error, "exec: no such file") {
		t.Fatalf("failing start: %+v", evs)
	}
}

// A stream the server closes mid-turn (a subscriber that fell behind) is
// resumed after the last event seen; one that keeps ending with nothing new
// becomes an error.
func TestRelay_ResumesClosedStream(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		id := "S1"
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["text"] == "stuck" {
			id = "S2"
		}
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": id, "turn": "T1", "after": 0, "events": "/v1/sessions/" + id + "/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/S1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "0" {
			writeSSE(w, sseEv{1, "T1", "update", chunk("one ")}) // then the server drops the stream
			return
		}
		writeSSE(w, sseEv{2, "T1", "update", chunk("two")}, sseEv{3, "T1", "turn_done", map[string]any{"stopReason": "end_turn"}})
	})
	f.mux.HandleFunc("GET /v1/sessions/S2/events", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w) // never anything
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "r1", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	want := []Event{{ID: "r1", Type: "delta", Text: "one "}, {ID: "r1", Type: "delta", Text: "two"}, {ID: "r1", Type: "done"}}
	if evs := p.until(t, "r1"); fmt.Sprint(evs) != fmt.Sprint(want) {
		t.Fatalf("events = %+v", evs)
	}
	gets := f.find("GET", "/v1/sessions/S1/events")
	if len(gets) != 2 || gets[1].Query != "after=1" {
		t.Fatalf("event calls = %+v", gets)
	}
	p.send(t, Request{ID: "r2", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "stuck"})
	if evs := p.until(t, "r2"); evs[len(evs)-1].Type != "error" {
		t.Fatalf("stuck stream: %+v", evs)
	}
}

// The panel's model choice is forwarded; none sends no model.
func TestRelay_ForwardsModel(t *testing.T) {
	sock := sockPath(t)
	f := newFakeV1()
	acceptOnce(f)
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "m1", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q", Model: "opus"})
	p.until(t, "m1")
	p.send(t, Request{ID: "m2", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	p.until(t, "m2")
	posts := f.find("POST", "/v1/sessions")
	if len(posts) != 2 || posts[0].Body["model"] != "opus" {
		t.Fatalf("posts = %+v", posts)
	}
	if _, has := posts[1].Body["model"]; has {
		t.Fatalf("model sent without a choice: %v", posts[1].Body)
	}
}

// A turn resumed on a server that restarted (its session unknown, 404, or
// a stream silent past resumeIdle) ends with an error instead of hanging.
func TestRelay_ResumeAfterServerRestartEnds(t *testing.T) {
	old := resumeIdle
	resumeIdle = 200 * time.Millisecond
	t.Cleanup(func() { resumeIdle = old })
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		id := b["text"].(string) // "idle" or "gone"
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": id, "turn": "T1", "after": 0, "events": "/v1/sessions/" + id + "/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "0" {
			writeSSE(w, sseEv{1, "T1", "update", chunk("part")}) // then the server goes away
			return
		}
		if r.PathValue("id") == "gone" {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "no such session for this client", "code": "not_found"})
			return
		}
		writeSSE(w) // a stream that carries no frame at all, not even keepalives
		<-r.Context().Done()
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	for _, id := range []string{"idle", "gone"} {
		p.send(t, Request{ID: id, Type: "ask", Lane: "page", URL: "https://a.example/", Question: id})
		evs := p.until(t, id)
		if last := evs[len(evs)-1]; last.Type != "error" || last.Error != "the Wingman server restarted; ask again" {
			t.Fatalf("%s: events = %+v", id, evs)
		}
	}
}

// Keepalives keep a resumed stream alive: a live turn that thinks longer
// than resumeIdle after a reopen is not reported as a restart.
func TestRelay_ResumedStreamKeptAliveByKeepalives(t *testing.T) {
	old := resumeIdle
	resumeIdle = 200 * time.Millisecond
	t.Cleanup(func() { resumeIdle = old })
	sock := sockPath(t)
	f := newFakeV1()
	f.mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": "K1", "turn": "T1", "after": 0, "events": "/v1/sessions/K1/events?after=0"})
	})
	f.mux.HandleFunc("GET /v1/sessions/K1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "0" {
			writeSSE(w, sseEv{1, "T1", "update", chunk("part")}) // then the stream drops
			return
		}
		writeSSE(w)
		for end := time.Now().Add(3 * resumeIdle); time.Now().Before(end); {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
				_, _ = fmt.Fprint(w, ": keepalive\n\n")
				w.(http.Flusher).Flush()
			}
		}
		data, _ := json.Marshal(map[string]any{"seq": 2, "turn": "T1", "kind": "turn_done", "time": time.Unix(0, 0), "data": map[string]any{"stopReason": "end_turn"}})
		_, _ = fmt.Fprintf(w, "id: L.2\nevent: turn_done\ndata: %s\n\n", data)
		w.(http.Flusher).Flush()
	})
	listenV1(t, sock, f)
	p := startPanel(t, &Relay{Socket: sock})
	p.send(t, Request{ID: "k", Type: "ask", Lane: "page", URL: "https://a.example/", Question: "q"})
	evs := p.until(t, "k")
	if last := evs[len(evs)-1]; last.Type != "done" {
		t.Fatalf("thinking turn kept alive by keepalives ended %+v", evs)
	}
}

func TestPageKey_GmailRouteKeepsFragment(t *testing.T) {
	a := pageKey("https://mail.google.com/mail/u/0/#inbox/FMfcgzQXJWDsKpLm")
	b := pageKey("https://mail.google.com/mail/u/0/#inbox/FMfcgzQXJWDsKpLz")
	if a != "https://mail.google.com/mail/u/0/#inbox/FMfcgzQXJWDsKpLm" {
		t.Fatalf("pageKey dropped the email route: %q", a)
	}
	if a == b {
		t.Fatalf("two emails share one page key %q", a)
	}
	if got := pageKey("https://app.example/#/settings/profile"); got != "https://app.example/#/settings/profile" {
		t.Fatalf("hash-router route dropped: %q", got)
	}
}

func TestPageKey_AnchorDropped(t *testing.T) {
	cases := map[string]string{
		"https://docs.example/guide#install":      "https://docs.example/guide",
		"https://docs.example/guide#":             "https://docs.example/guide",
		"https://mail.google.com/mail/u/0/#inbox": "https://mail.google.com/mail/u/0/",
		"https://docs.example/guide":              "https://docs.example/guide",
		"":                                        "",
	}
	for in, want := range cases {
		if got := pageKey(in); got != want {
			t.Errorf("pageKey(%q) = %q, want %q", in, got, want)
		}
	}
}
