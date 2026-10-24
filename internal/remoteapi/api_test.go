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
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
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
			return SessionDetail{Session: Session{ID: "s1", State: StateIdle}, Turns: []json.RawMessage{json.RawMessage(`{"user":"hi"}`)}}, nil
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
