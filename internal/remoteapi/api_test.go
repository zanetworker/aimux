package remoteapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/terminput"
)

const testLogin = "me@example.com"

func newTestAPI(t *testing.T, f *fakeBackend) (*httptest.Server, *Broker) {
	t.Helper()
	var b *Broker
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: 5 * time.Second})
	api := &API{
		Broker: b,
		Sessions: func(ctx context.Context, limit int, automated bool) ([]Session, error) {
			return []Session{{ID: "s1", Title: "Fix the cache", State: StateIdle}, {ID: "s2", State: StateOpenInTerminal}}[:min(limit, 2)], nil
		},
		Session: func(ctx context.Context, id string, turns int) (SessionDetail, error) {
			if id != "s1" {
				return SessionDetail{}, acprun.ErrUnknownSession
			}
			return SessionDetail{Session: Session{ID: "s1", State: StateIdle}, Turns: []Turn{{User: "hi"}}}, nil
		},
	}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(srv.Close)
	return srv, b
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestAPI_RequiresIdentity(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, err := http.Get(srv.URL + "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no identity: %d, want 401", resp.StatusCode)
	}
}

func TestAPI_HealthAndSessions(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := do(t, srv, "GET", "/v1/health", "")
	if resp.StatusCode != 200 || body["login"] != testLogin {
		t.Fatalf("health: %d %v", resp.StatusCode, body)
	}
	resp, body = do(t, srv, "GET", "/v1/sessions?limit=1", "")
	list, _ := body["sessions"].([]any)
	if resp.StatusCode != 200 || len(list) != 1 {
		t.Fatalf("sessions: %d %v", resp.StatusCode, body)
	}
	if first := list[0].(map[string]any); first["id"] != "s1" || first["state"] != StateIdle {
		t.Fatalf("first session %v", first)
	}
	if resp, body = do(t, srv, "GET", "/v1/sessions?limit=abc", ""); resp.StatusCode != 400 || body["code"] != "bad_request" {
		t.Fatalf("bad limit: %d %v", resp.StatusCode, body)
	}
}

func TestAPI_SessionDetail(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := do(t, srv, "GET", "/v1/sessions/s1", "")
	if resp.StatusCode != 200 || len(body["turns"].([]any)) != 1 {
		t.Fatalf("detail: %d %v", resp.StatusCode, body)
	}
	if _, ok := body["stats"]; ok {
		t.Fatalf("detail without stats carries %v, want the key omitted", body["stats"])
	}
	resp, body = do(t, srv, "GET", "/v1/sessions/nope", "")
	if resp.StatusCode != 404 || body["code"] != "unknown_session" {
		t.Fatalf("unknown: %d %v", resp.StatusCode, body)
	}
}

func TestAPI_SendErrors(t *testing.T) {
	tests := []struct {
		name     string
		check    error
		body     string
		wantCode int
		wantErr  string
	}{
		{"empty text", nil, `{"text":"  "}`, 400, "bad_request"},
		{"not json", nil, `hello`, 400, "bad_request"},
		{"unknown session", acprun.ErrUnknownSession, `{"text":"hi"}`, 404, "unknown_session"},
		{"open in a terminal", fmt.Errorf("%w (session s1)", acprun.ErrOpenInTerminal), `{"text":"hi"}`, 409, "open_in_terminal"},
		{"too many runs", acprun.ErrTooManyRuns, `{"text":"hi"}`, 503, "too_many_runs"},
		{"other", errors.New("boom"), `{"text":"hi"}`, 500, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{checkF: func(string) error { return tt.check }}
			srv, _ := newTestAPI(t, f)
			resp, body := do(t, srv, "POST", "/v1/sessions/s1/messages", tt.body)
			if resp.StatusCode != tt.wantCode || body["code"] != tt.wantErr {
				t.Fatalf("got %d %v, want %d %s", resp.StatusCode, body, tt.wantCode, tt.wantErr)
			}
		})
	}
}

func TestAPI_SendBusy(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		<-release
		return "end_turn", nil
	}}
	srv, _ := newTestAPI(t, f)
	if resp, _ := do(t, srv, "POST", "/v1/sessions/s1/messages", `{"text":"one","key":"a"}`); resp.StatusCode != 202 {
		t.Fatalf("first send: %d", resp.StatusCode)
	}
	if resp, body := do(t, srv, "POST", "/v1/sessions/s1/messages", `{"text":"two","key":"b"}`); resp.StatusCode != 409 || body["code"] != "busy" {
		t.Fatalf("second send: %d %v, want 409 busy", resp.StatusCode, body)
	}
}

// readSSE reads server-sent events until one of kind arrives.
func readSSE(t *testing.T, r io.Reader, until string) []Event {
	t.Helper()
	var events []Event
	sc := bufio.NewScanner(r)
	var data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && data != "":
			var e Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("bad event %q: %v", data, err)
			}
			events = append(events, e)
			data = ""
			if e.Kind == until {
				return events
			}
		}
	}
	t.Fatalf("stream ended before %s; got %v", until, kindsOf(events))
	return nil
}

func TestAPI_SendAndStreamEvents(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := do(t, srv, "POST", "/v1/sessions/s1/messages", `{"text":"hello","key":"k1"}`)
	if resp.StatusCode != 202 || body["turn"] == "" {
		t.Fatalf("send: %d %v", resp.StatusCode, body)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/sessions/s1/events?after=0", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	if ct := stream.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	evs := readSSE(t, stream.Body, KindTurnDone)
	if kinds := kindsOf(evs); len(kinds) != 3 || kinds[0] != KindTurnStarted || kinds[2] != KindTurnDone {
		t.Fatalf("streamed %v", kinds)
	}
	if evs[0].Turn != body["turn"] {
		t.Fatalf("events belong to %q, send returned %q", evs[0].Turn, body["turn"])
	}
}

func TestAPI_EventsReplayAfterLastEventID(t *testing.T) {
	srv, b := newTestAPI(t, &fakeBackend{})
	if _, err := b.Start("s1", "hello", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/sessions/s1/events?wait=0", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Seq != 3 || page.Events[0].Kind != KindTurnDone {
		t.Fatalf("replay after 2: %+v", page.Events)
	}
}

func TestAPI_ApprovalAndCancel(t *testing.T) {
	var b *Broker
	var told string
	f := &fakeBackend{}
	f.sendF = func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		told, _ = b.Approver()(ctx, permissionRequest())
		return "end_turn", nil
	}
	srv, broker := newTestAPI(t, f)
	b = broker
	if resp, _ := do(t, srv, "POST", "/v1/sessions/s1/messages", `{"text":"push"}`); resp.StatusCode != 202 {
		t.Fatal(resp.StatusCode)
	}
	evs := waitFor(t, b, "s1", KindApproval)
	var ap struct {
		ApprovalID string `json:"approvalId"`
	}
	_ = json.Unmarshal(evs[len(evs)-1].Data, &ap)

	if resp, body := do(t, srv, "POST", "/v1/approvals/"+ap.ApprovalID, `{"option":"sudo"}`); resp.StatusCode != 400 || body["code"] != "invalid_option" {
		t.Fatalf("invalid option: %d %v", resp.StatusCode, body)
	}
	if resp, _ := do(t, srv, "POST", "/v1/approvals/"+ap.ApprovalID, `{"option":"allow"}`); resp.StatusCode != 200 {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	waitFor(t, b, "s1", KindTurnDone)
	if told != "allow" {
		t.Fatalf("agent was told %q, want allow", told)
	}
	if resp, body := do(t, srv, "POST", "/v1/approvals/"+ap.ApprovalID, `{"option":"allow"}`); resp.StatusCode != 404 || body["code"] != "no_such_approval" {
		t.Fatalf("answered twice: %d %v", resp.StatusCode, body)
	}
	// The turn is over: cancel has nothing to stop and must not poke the
	// idle adapter.
	if resp, body := do(t, srv, "POST", "/v1/sessions/s1/cancel", ""); resp.StatusCode != 409 || body["code"] != "not_running" || f.cancels.Load() != 0 {
		t.Fatalf("cancel after the turn: %d %v, adapter cancels %d; want 409 not_running and no adapter call", resp.StatusCode, body, f.cancels.Load())
	}
}

func TestAPI_IncludeAutomatedParam(t *testing.T) {
	var gotAutomated bool
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}),
		Sessions: func(ctx context.Context, limit int, automated bool) ([]Session, error) {
			gotAutomated = automated
			return nil, nil
		}}
	s2 := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer s2.Close()
	if resp, body := do(t, s2, "GET", "/v1/sessions?include_automated=1", ""); resp.StatusCode != 200 || !gotAutomated {
		t.Fatalf("include_automated not passed: %d %v", resp.StatusCode, body)
	}
	if _, body := do(t, s2, "GET", "/v1/sessions", ""); body["sessions"] == nil {
		t.Fatalf("empty list encoded as null: %v", body)
	}
}

// A browser EventSource reconnects to the original ?after= URL and adds a
// newer Last-Event-ID; the header must win or every reconnect replays.
func TestAPI_LastEventIDBeatsTheInitialCursor(t *testing.T) {
	srv, b := newTestAPI(t, &fakeBackend{})
	if _, err := b.Start("s1", "hello", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/sessions/s1/events?after=0&wait=0", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Seq != 3 {
		t.Fatalf("got %d events from seq %v, want only seq 3 (after Last-Event-ID 2)", len(page.Events), page.Events)
	}
}

func TestAPI_CancelIdleSession(t *testing.T) {
	b := NewBroker(BrokerDeps{Check: func(string) error { return nil }, Cancel: func(string) error { return acprun.ErrNotRunning }}, BrokerOptions{})
	api := &API{Broker: b, Sessions: func(context.Context, int, bool) ([]Session, error) { return nil, nil }}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	if resp, body := do(t, srv, "POST", "/v1/sessions/s1/cancel", ""); resp.StatusCode != 409 || body["code"] != "not_running" {
		t.Fatalf("cancel with nothing running: %d %v, want 409 not_running", resp.StatusCode, body)
	}
}

func eventsPage(t *testing.T, srv *httptest.Server, path, lastID string) []Event {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	req.Header.Set("Last-Event-ID", lastID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page.Events
}

// Sequence numbers restart with the server, so an EventSource that
// reconnects with a cursor from before the restart must see the new log
// from the start, not wait until the new numbers pass its old cursor.
func TestAPI_LastEventIDFromAnEarlierLogReplaysTheNewLog(t *testing.T) {
	srv, b := newTestAPI(t, &fakeBackend{})
	if _, err := b.Start("s1", "hello", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?after=50&wait=0", "0123456789abcdef.50"); len(evs) != 3 {
		t.Fatalf("got %d events after a cursor from an earlier log, want all 3", len(evs))
	}
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?wait=0", b.LogID()+".2"); len(evs) != 1 || evs[0].Seq != 3 {
		t.Fatalf("cursor from this log: got %v, want only seq 3", evs)
	}
}

func TestAPI_StreamedEventIDsNameTheLog(t *testing.T) {
	srv, b := newTestAPI(t, &fakeBackend{})
	if _, err := b.Start("s1", "hello", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/sessions/s1/events", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if id, ok := strings.CutPrefix(sc.Text(), "id: "); ok {
			if id != b.LogID()+".1" {
				t.Fatalf("first event id %q, want %q", id, b.LogID()+".1")
			}
			return
		}
	}
	t.Fatal("no event id in the stream")
}

// The event page names its log, and a cursor (?after=) passed with another
// log's id (the server restarted in between) replays the new log in full.
func TestAPI_EventPageNamesItsLogAndScopesAfter(t *testing.T) {
	srv, b := newTestAPI(t, &fakeBackend{})
	if _, err := b.Start("s1", "hello", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	resp, body := do(t, srv, "GET", "/v1/sessions/s1/events?wait=0", "")
	if resp.StatusCode != 200 || body["log"] != b.LogID() {
		t.Fatalf("page log %v, want %q", body["log"], b.LogID())
	}
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?wait=0&after=2&log=0123456789abcdef", ""); len(evs) != 3 {
		t.Fatalf("after=2 from another log: got %d events, want all 3", len(evs))
	}
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?wait=0&after=2&log="+b.LogID(), ""); len(evs) != 1 {
		t.Fatalf("after=2 from this log: got %d events, want 1", len(evs))
	}
}

func TestAPI_Search(t *testing.T) {
	var gotQ string
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}),
		Search: func(ctx context.Context, q string, limit int) ([]SearchHit, error) {
			gotQ = q
			return []SearchHit{{Session: Session{ID: "s1", Title: "Fix the cache"}, Snippet: "the [cache]"}}, nil
		}}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	resp, body := do(t, srv, "GET", "/v1/search?q=cache+ttl", "")
	results, _ := body["results"].([]any)
	if resp.StatusCode != 200 || gotQ != "cache ttl" || len(results) != 1 {
		t.Fatalf("search: %d q=%q %v", resp.StatusCode, gotQ, body)
	}
	if hit := results[0].(map[string]any); hit["snippet"] != "the [cache]" || hit["id"] != "s1" {
		t.Fatalf("hit %v", hit)
	}
	if resp, body := do(t, srv, "GET", "/v1/search?q=+", ""); resp.StatusCode != 400 || body["code"] != "bad_request" {
		t.Fatalf("empty query: %d %v", resp.StatusCode, body)
	}
}

func newSessionAPI(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	creates := 0
	var b *Broker
	f := &fakeBackend{}
	b = NewBroker(f.deps(&b), BrokerOptions{})
	api := &API{Broker: b,
		Projects: func(ctx context.Context) ([]Project, error) {
			return []Project{{Path: "/w/app", Name: "app"}}, nil
		},
		Create: func(ctx context.Context, cwd, text string) (string, error) {
			creates++
			return fmt.Sprintf("new-%d", creates), nil
		},
	}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(srv.Close)
	return srv, &creates
}

func TestAPI_NewSession(t *testing.T) {
	srv, creates := newSessionAPI(t)
	resp, body := do(t, srv, "GET", "/v1/projects", "")
	if projects, _ := body["projects"].([]any); resp.StatusCode != 200 || len(projects) != 1 {
		t.Fatalf("projects: %d %v", resp.StatusCode, body)
	}
	resp, body = do(t, srv, "POST", "/v1/sessions", `{"cwd":"/w/app","text":"plan the release","key":"k1"}`)
	if resp.StatusCode != 202 || body["id"] != "new-1" || body["turn"] == "" {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	// A retry with the same key is the same session and turn.
	resp, again := do(t, srv, "POST", "/v1/sessions", `{"cwd":"/w/app","text":"plan the release","key":"k1"}`)
	if resp.StatusCode != 202 || again["id"] != "new-1" || again["turn"] != body["turn"] || *creates != 1 {
		t.Fatalf("retry: %d %v (creates %d)", resp.StatusCode, again, *creates)
	}
}

func TestAPI_NewSessionOnlyInRecentProjects(t *testing.T) {
	srv, creates := newSessionAPI(t)
	for _, body := range []string{
		`{"cwd":"/etc","text":"x"}`,
		`{"cwd":"/w/app/../../etc","text":"x"}`,
		`{"cwd":"/w/app","text":"  "}`,
		`not json`,
	} {
		if resp, out := do(t, srv, "POST", "/v1/sessions", body); resp.StatusCode != 400 || out["code"] != "bad_request" {
			t.Fatalf("%s: %d %v, want 400 bad_request", body, resp.StatusCode, out)
		}
	}
	if *creates != 0 {
		t.Fatalf("created %d sessions from refused requests", *creates)
	}
}

// A retry of a session already created answers from the key, even if the
// folder has left the recent projects since.
func TestAPI_NewSessionRetryAfterProjectsChange(t *testing.T) {
	projects := []Project{{Path: "/w/app", Name: "app"}}
	creates := 0
	var b *Broker
	b = NewBroker((&fakeBackend{}).deps(&b), BrokerOptions{})
	api := &API{Broker: b,
		Projects: func(ctx context.Context) ([]Project, error) { return projects, nil },
		Create:   func(ctx context.Context, cwd, text string) (string, error) { creates++; return "new-1", nil },
	}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	if resp, _ := do(t, srv, "POST", "/v1/sessions", `{"cwd":"/w/app","text":"go","key":"k9"}`); resp.StatusCode != 202 {
		t.Fatal(resp.StatusCode)
	}
	projects = nil // the folder is no longer among the recent ones
	resp, body := do(t, srv, "POST", "/v1/sessions", `{"cwd":"/w/app","text":"go","key":"k9"}`)
	if resp.StatusCode != 202 || body["id"] != "new-1" || creates != 1 {
		t.Fatalf("retry: %d %v (creates %d)", resp.StatusCode, body, creates)
	}
}

// A bare Last-Event-ID carries no log of its own; it belongs to the log the
// request names with ?log=. From an earlier log, the replay starts over.
func TestAPI_UnscopedLastEventIDFollowsTheLogParam(t *testing.T) {
	srv, b := newTestAPI(t, &fakeBackend{})
	if _, err := b.Start("s1", "hello", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?wait=0&after=2&log=0123456789abcdef", "2"); len(evs) != 3 {
		t.Fatalf("bare Last-Event-ID with an old ?log=: got %d events, want all 3", len(evs))
	}
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?wait=0&log="+b.LogID(), "2"); len(evs) != 1 {
		t.Fatalf("bare Last-Event-ID with the current ?log=: got %d events, want 1", len(evs))
	}
	if evs := eventsPage(t, srv, "/v1/sessions/s1/events?wait=0", "2"); len(evs) != 1 {
		t.Fatalf("bare Last-Event-ID without ?log=: got %d events, want 1 (taken as this log)", len(evs))
	}
}

// Clients detect what the server offers from /v1/health instead of probing.
func TestAPI_HealthReportsVersionAndCapabilities(t *testing.T) {
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Version: "1.2.3",
		Search: func(context.Context, string, int) ([]SearchHit, error) { return nil, nil }}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	resp, body := do(t, srv, "GET", "/v1/health", "")
	if resp.StatusCode != 200 || body["version"] != "1.2.3" || body["login"] != testLogin {
		t.Fatalf("health: %d %v", resp.StatusCode, body)
	}
	if v, ok := body["acpProtocolVersion"].(float64); !ok || v < 1 {
		t.Fatalf("acpProtocolVersion %v, want the ACP version update events follow", body["acpProtocolVersion"])
	}
	caps := map[string]bool{}
	for _, c := range body["capabilities"].([]any) {
		caps[c.(string)] = true
	}
	if !caps["search"] || caps["new_sessions"] {
		t.Fatalf("capabilities %v: want search (configured) and not new_sessions (not configured)", body["capabilities"])
	}
}

// Health carries the Jira settings only when a Jira base is configured.
func TestAPI_HealthLinks(t *testing.T) {
	health := func(links *Links) map[string]any {
		api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Links: links}
		srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
		defer srv.Close()
		_, body := do(t, srv, "GET", "/v1/health", "")
		return body
	}
	body := health(&Links{JiraBase: "https://jira.example.com", JiraProjects: []string{"WID", "OPS"}})
	links, _ := body["links"].(map[string]any)
	projects, _ := links["jiraProjects"].([]any)
	if links["jiraBase"] != "https://jira.example.com" || len(projects) != 2 || projects[0] != "WID" {
		t.Fatalf("health links %v, want {jiraBase, jiraProjects}", body["links"])
	}
	// No projects listed: the key is still an array, never null.
	body = health(&Links{JiraBase: "https://jira.example.com"})
	if p, ok := body["links"].(map[string]any)["jiraProjects"].([]any); !ok || len(p) != 0 {
		t.Fatalf("jiraProjects %v, want []", body["links"])
	}
	for name, l := range map[string]*Links{"nil": nil, "no base": {JiraProjects: []string{"WID"}}} {
		if body := health(l); body["links"] != nil {
			t.Fatalf("%s: health links %v, want the key omitted", name, body["links"])
		}
	}
}

// Starting a session answers like sending a message, so one code path in a
// client follows both.
func TestAPI_NewSessionAnswersLikeASend(t *testing.T) {
	srv, _ := newSessionAPI(t)
	resp, body := do(t, srv, "POST", "/v1/sessions", `{"cwd":"/w/app","text":"plan","key":"k2"}`)
	if resp.StatusCode != 202 {
		t.Fatal(resp.StatusCode)
	}
	for _, f := range []string{"id", "turn", "after", "events"} {
		if _, ok := body[f]; !ok {
			t.Fatalf("response %v lacks %q", body, f)
		}
	}
	if body["events"] != fmt.Sprintf("/v1/sessions/%s/events?after=%v", body["id"], body["after"]) {
		t.Fatalf("events link %v", body["events"])
	}
}

// fakeTerminal is a TerminalInput that records each call and answers err.
type fakeTerminal struct {
	mu    sync.Mutex
	err   error
	calls []string
}

func (f *fakeTerminal) Send(_ context.Context, id, text, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("send %s %q %s", id, text, key))
	return f.err
}

func (f *fakeTerminal) SendReplacing(_ context.Context, id, text, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("replace %s %q %s", id, text, key))
	return f.err
}

func (f *fakeTerminal) Press(_ context.Context, id, promptID string, option terminput.Key, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("press %s %s %s %s", id, promptID, option, key))
	return f.err
}

func newTerminalAPI(t *testing.T, term TerminalInput, enabled bool) *httptest.Server {
	t.Helper()
	srv, _ := newTerminalAPIWithLog(t, term, enabled)
	return srv
}

func newTerminalAPIWithLog(t *testing.T, term TerminalInput, enabled bool) (*httptest.Server, *syncBuffer) {
	t.Helper()
	log := &syncBuffer{}
	api := &API{Log: log,
		Broker:   NewBroker(BrokerDeps{}, BrokerOptions{}),
		Sessions: func(context.Context, int, bool) ([]Session, error) { return nil, nil },
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			if id == "nope" {
				return SessionDetail{}, acprun.ErrUnknownSession
			}
			return SessionDetail{Session: Session{ID: id, State: StateOpenInTerminal}, Turns: []Turn{},
				Terminal: &TerminalStatus{Status: "idle", CanType: true}}, nil
		},
		TerminalInput: term, TerminalEnabled: enabled,
	}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(srv.Close)
	return srv, log
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A driver failure answers a fixed message: osascript stderr and JXA
// exception text stay in the server's log, never in the response.
func TestAPI_TerminalDriverFailureHidesDetail(t *testing.T) {
	detail := "execution error: Error: TypeError: secret window 42 (-2700)"
	for _, path := range []string{"/v1/sessions/s9/terminal/messages", "/v1/sessions/s9/terminal/keys"} {
		srv, log := newTerminalAPIWithLog(t, &fakeTerminal{err: fmt.Errorf("iterm2: send: %s", detail)}, true)
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(`{"promptId":"p1","option":"1"}`))
		if strings.HasSuffix(path, "messages") {
			req, _ = http.NewRequest("POST", srv.URL+path, strings.NewReader(`{"text":"a"}`))
		}
		req.Header.Set("Tailscale-User-Login", testLogin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 500 || strings.Contains(string(raw), "secret") || !strings.Contains(string(raw), `"code":"internal"`) ||
			!strings.Contains(string(raw), "the terminal driver failed") {
			t.Fatalf("%s: %d %s, want 500 internal with a fixed message", path, resp.StatusCode, raw)
		}
		if !strings.Contains(log.String(), detail) {
			t.Fatalf("server log %q, want the driver's detail", log.String())
		}
	}
}

func TestAPI_TerminalBodyLimitsAndUnknownFields(t *testing.T) {
	term := &fakeTerminal{}
	srv := newTerminalAPI(t, term, true)
	big := strings.Repeat("a", 64<<10)
	for _, c := range []struct{ name, path, body string }{
		{"message over 64 KiB", "/v1/sessions/s9/terminal/messages", `{"text":"` + big + `"}`},
		{"unknown message field", "/v1/sessions/s9/terminal/messages", `{"text":"a","paste":true}`},
		{"keys over 64 KiB", "/v1/sessions/s9/terminal/keys", `{"promptId":"` + big + `","option":"1"}`},
		{"unknown keys field", "/v1/sessions/s9/terminal/keys", `{"promptId":"p1","option":"1","force":true}`},
	} {
		if resp, body := do(t, srv, "POST", c.path, c.body); resp.StatusCode != 400 || body["code"] != "bad_request" {
			t.Fatalf("%s: %d %v, want 400 bad_request", c.name, resp.StatusCode, body)
		}
	}
	if len(term.calls) != 0 {
		t.Fatalf("calls %v for refused bodies", term.calls)
	}
	// Just under the limit is typed.
	if resp, _ := do(t, srv, "POST", "/v1/sessions/s9/terminal/messages", `{"text":"`+strings.Repeat("a", 60<<10)+`"}`); resp.StatusCode != 202 {
		t.Fatalf("60 KiB message: %d, want 202", resp.StatusCode)
	}
}

// replace: true asks to clear the input box first; plain messages never do.
func TestAPI_TerminalMessageReplace(t *testing.T) {
	term := &fakeTerminal{}
	srv := newTerminalAPI(t, term, true)
	if resp, _ := do(t, srv, "POST", "/v1/sessions/s9/terminal/messages", `{"text":"run it","key":"k1","replace":true}`); resp.StatusCode != 202 {
		t.Fatalf("replace: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, "POST", "/v1/sessions/s9/terminal/messages", `{"text":"run it","key":"k2"}`); resp.StatusCode != 202 {
		t.Fatalf("plain: %d", resp.StatusCode)
	}
	if got := strings.Join(term.calls, "|"); got != `replace s9 "run it" k1|send s9 "run it" k2` {
		t.Fatalf("calls = %s", got)
	}
	term.err = terminput.ErrNotCleared
	if resp, body := do(t, srv, "POST", "/v1/sessions/s9/terminal/messages", `{"text":"run it","replace":true}`); resp.StatusCode != 409 || body["code"] != "box_not_cleared" {
		t.Fatalf("%d %v, want 409 box_not_cleared", resp.StatusCode, body)
	}
}

// Without a Session func, ErrNotInTerminal stays a 409 instead of panicking.
func TestAPI_TerminalErrorWithoutSessionFunc(t *testing.T) {
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), TerminalInput: &fakeTerminal{err: terminput.ErrNotInTerminal}, TerminalEnabled: true}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	if resp, body := do(t, srv, "POST", "/v1/sessions/x/terminal/messages", `{"text":"a"}`); resp.StatusCode != 409 || body["code"] != "not_in_terminal" {
		t.Fatalf("%d %v, want 409 not_in_terminal", resp.StatusCode, body)
	}
}

func TestAPI_TerminalMessages(t *testing.T) {
	tests := []struct {
		name      string
		session   string
		err       error
		body      string
		wantCode  int
		wantErr   string
		wantCalls int
	}{
		{"typed", "s9", nil, `{"text":"run the tests","key":"k1"}`, 202, "", 1},
		{"not json", "s9", nil, `hello`, 400, "bad_request", 0},
		{"empty text", "s9", nil, `{"text":"  "}`, 400, "bad_request", 0},
		{"control characters", "s9", terminput.ErrBadText, `{"text":"a"}`, 400, "bad_text", 1},
		{"unknown session", "nope", terminput.ErrNotInTerminal, `{"text":"a"}`, 404, "unknown_session", 1},
		{"not in a terminal", "s9", terminput.ErrNotInTerminal, `{"text":"a"}`, 409, "not_in_terminal", 1},
		{"not claude", "s9", terminput.ErrNotClaude, `{"text":"a"}`, 409, "not_claude", 1},
		{"mac in use", "s9", terminput.ErrMacInUse, `{"text":"a"}`, 409, "mac_in_use", 1},
		{"no driver", "s9", terminput.ErrNoDriver, `{"text":"a"}`, 409, "no_terminal_driver", 1},
		{"automation denied", "s9", terminput.ErrAutomationDenied, `{"text":"a"}`, 409, "automation_denied", 1},
		{"not at input", "s9", terminput.ErrNotAtInput, `{"text":"a"}`, 409, "not_at_input", 1},
		{"not submitted", "s9", terminput.ErrNotSubmitted, `{"text":"a"}`, 409, "not_submitted", 1},
		{"claude working", "s9", terminput.ErrBusy, `{"text":"a"}`, 409, "busy", 1},
		{"disabled by the service", "s9", terminput.ErrDisabled, `{"text":"a"}`, 403, "disabled", 1},
		{"driver failure", "s9", fmt.Errorf("iterm2: send: %w", errors.New("helper died")), `{"text":"a"}`, 500, "internal", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := &fakeTerminal{err: tt.err}
			srv := newTerminalAPI(t, term, true)
			resp, body := do(t, srv, "POST", "/v1/sessions/"+tt.session+"/terminal/messages", tt.body)
			if resp.StatusCode != tt.wantCode || (tt.wantErr != "" && body["code"] != tt.wantErr) {
				t.Fatalf("got %d %v, want %d %s", resp.StatusCode, body, tt.wantCode, tt.wantErr)
			}
			if tt.wantErr == "" && body["status"] != "typed" {
				t.Fatalf("body %v, want status typed", body)
			}
			if len(term.calls) != tt.wantCalls {
				t.Fatalf("calls %v, want %d (never retried)", term.calls, tt.wantCalls)
			}
			if tt.name == "typed" && term.calls[0] != `send s9 "run the tests" k1` {
				t.Fatalf("call %q", term.calls[0])
			}
		})
	}
}

// The guard's and the busy refusal's fixed messages, never err.Error().
func TestAPI_TerminalGuardMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{terminput.ErrMacInUse, "you're using this session on your Mac"},
		{terminput.ErrBusy, "Claude is working in this session; try again when it finishes"},
	} {
		srv := newTerminalAPI(t, &fakeTerminal{err: tc.err}, true)
		resp, body := do(t, srv, "POST", "/v1/sessions/s9/terminal/messages", `{"text":"a"}`)
		if resp.StatusCode != 409 || body["error"] != tc.want {
			t.Errorf("%s: got %d %v, want 409 %q", terminput.Code(tc.err), resp.StatusCode, body, tc.want)
		}
	}
}

func TestAPI_TerminalKeys(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		body      string
		wantCode  int
		wantErr   string
		wantCalls int
	}{
		{"typed", nil, `{"promptId":"p1","option":"1","key":"k2"}`, 202, "", 1},
		{"not json", nil, `{`, 400, "bad_request", 0},
		{"no prompt id", nil, `{"option":"1"}`, 400, "bad_request", 0},
		{"no option", nil, `{"promptId":"p1"}`, 400, "bad_request", 0},
		{"no prompt", terminput.ErrNoPrompt, `{"promptId":"p1","option":"1"}`, 409, "no_prompt", 1},
		{"prompt changed", terminput.ErrPromptChanged, `{"promptId":"p1","option":"1"}`, 409, "prompt_changed", 1},
		{"mac in use", terminput.ErrMacInUse, `{"promptId":"p1","option":"1"}`, 409, "mac_in_use", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := &fakeTerminal{err: tt.err}
			srv := newTerminalAPI(t, term, true)
			resp, body := do(t, srv, "POST", "/v1/sessions/s9/terminal/keys", tt.body)
			if resp.StatusCode != tt.wantCode || (tt.wantErr != "" && body["code"] != tt.wantErr) {
				t.Fatalf("got %d %v, want %d %s", resp.StatusCode, body, tt.wantCode, tt.wantErr)
			}
			if len(term.calls) != tt.wantCalls {
				t.Fatalf("calls %v, want %d", term.calls, tt.wantCalls)
			}
			if tt.name == "typed" && (body["status"] != "typed" || term.calls[0] != "press s9 p1 1 k2") {
				t.Fatalf("body %v call %v", body, term.calls)
			}
		})
	}
}

// Off in config (or not wired): both endpoints answer 403 disabled without
// reaching the terminal, and health leaves terminal_input out.
func TestAPI_TerminalDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		term *fakeTerminal
	}{{"off in config", &fakeTerminal{}}, {"not wired", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			var term TerminalInput
			if tc.term != nil {
				term = tc.term
			}
			srv := newTerminalAPI(t, term, false)
			for _, c := range []struct{ path, body string }{
				{"/v1/sessions/s9/terminal/messages", `{"text":"a"}`},
				{"/v1/sessions/s9/terminal/keys", `{"promptId":"p1","option":"1"}`},
			} {
				if resp, body := do(t, srv, "POST", c.path, c.body); resp.StatusCode != 403 || body["code"] != "disabled" {
					t.Fatalf("%s: %d %v, want 403 disabled", c.path, resp.StatusCode, body)
				}
			}
			if tc.term != nil && len(tc.term.calls) != 0 {
				t.Fatalf("calls %v while disabled", tc.term.calls)
			}
			_, body := do(t, srv, "GET", "/v1/health", "")
			if strings.Contains(fmt.Sprint(body["capabilities"]), CapTerminalInput) {
				t.Fatalf("capabilities %v while disabled", body["capabilities"])
			}
		})
	}
}

func TestAPI_TerminalCapabilityWhenEnabled(t *testing.T) {
	srv := newTerminalAPI(t, &fakeTerminal{}, true)
	_, body := do(t, srv, "GET", "/v1/health", "")
	if !strings.Contains(fmt.Sprint(body["capabilities"]), CapTerminalInput) {
		t.Fatalf("capabilities %v, want %s", body["capabilities"], CapTerminalInput)
	}
	resp, body := do(t, srv, "GET", "/v1/sessions/s9", "")
	term, _ := body["terminal"].(map[string]any)
	if resp.StatusCode != 200 || term["status"] != "idle" || term["canType"] != true {
		t.Fatalf("detail %d %v, want its terminal status", resp.StatusCode, body)
	}
}

// A coded error answers its fixed message, never the wrapped text.
func TestAPI_TerminalCodedErrorsUseFixedMessages(t *testing.T) {
	for _, sentinel := range []error{terminput.ErrNotAtInput, terminput.ErrAutomationDenied, terminput.ErrNoDriver,
		terminput.ErrNotClaude, terminput.ErrMacInUse, terminput.ErrPromptChanged, terminput.ErrNoPrompt,
		terminput.ErrNotInTerminal, terminput.ErrBadText, terminput.ErrDisabled, terminput.ErrNotSubmitted} {
		err := fmt.Errorf("iterm2: locate: secret tty /dev/ttys001: %w", sentinel)
		w := httptest.NewRecorder()
		writeTerminalFailure(w, err, io.Discard)
		var body ErrorBody
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if strings.Contains(w.Body.String(), "secret") || body.Code != terminput.Code(sentinel) || body.Error == "" {
			t.Errorf("%v -> %s, want code %s with a fixed message", sentinel, w.Body.String(), terminput.Code(sentinel))
		}
	}
	w := httptest.NewRecorder()
	writeTerminalFailure(w, terminput.ErrNotAtInput, io.Discard)
	if !strings.Contains(w.Body.String(), "Claude Code is not at an empty input box") {
		t.Fatalf("not_at_input -> %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	writeTerminalFailure(w, terminput.ErrBadText, io.Discard)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "starts with ! or /") {
		t.Fatalf("bad_text -> %d %s, want the leading ! and / rule named", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	writeTerminalFailure(w, terminput.ErrNotSubmitted, io.Discard)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "the message is in the input box but was not sent") {
		t.Fatalf("not_submitted -> %d %s", w.Code, w.Body.String())
	}
}

func TestAPI_TerminalDriverDetailIsQuotedInTheLog(t *testing.T) {
	log := &syncBuffer{}
	writeTerminalFailure(httptest.NewRecorder(), errors.New("line one\nline two"), log)
	if got := log.String(); got != "terminal input: \"line one\\nline two\"\n" {
		t.Fatalf("log %q, want the detail quoted on one line", got)
	}
}

// After a successful send or press the session's cached status is dropped;
// a failed attempt keeps it.
func TestAPI_TerminalSuccessInvalidatesStatus(t *testing.T) {
	var mu sync.Mutex
	var typed []string
	term := &fakeTerminal{}
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), TerminalInput: term, TerminalEnabled: true,
		TerminalTyped: func(id string) { mu.Lock(); typed = append(typed, id); mu.Unlock() }}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	do(t, srv, "POST", "/v1/sessions/s9/terminal/messages", `{"text":"a"}`)
	do(t, srv, "POST", "/v1/sessions/s8/terminal/keys", `{"promptId":"p1","option":"1"}`)
	term.mu.Lock()
	term.err = terminput.ErrNotAtInput
	term.mu.Unlock()
	do(t, srv, "POST", "/v1/sessions/s7/terminal/messages", `{"text":"a"}`)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(typed) != "[s9 s8]" {
		t.Fatalf("invalidated %v, want s9 and s8 only", typed)
	}
}
