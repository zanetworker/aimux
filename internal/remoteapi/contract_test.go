package remoteapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	aimuxapi "github.com/zanetworker/aimux/api"
	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/push"
)

// The /v1 contract test: the server is driven through every operation in
// api/remote-v1.openapi.yaml, including error responses and every event
// kind, and each response and event is validated against the spec. It also
// fails when the spec lists an operation or event kind the test never saw,
// so the spec and the server cannot drift apart unnoticed.

// eventDataSchema names the schema of each event kind's data.
var eventDataSchema = map[string]string{
	KindTurnStarted:      "TurnStartedData",
	acprun.KindUpdate:    "UpdateData",
	KindApproval:         "ApprovalData",
	KindApprovalResolved: "ApprovalResolvedData",
	KindTurnDone:         "TurnDoneData",
	KindTurnError:        "TurnErrorData",
}

type contract struct {
	t      *testing.T
	doc    *openapi3.T
	srv    *httptest.Server
	router routers.Router

	mu    sync.Mutex
	ops   map[string]bool // operationIds exercised
	kinds map[string]bool // event kinds seen
}

func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(aimuxapi.RemoteV1)
	if err != nil {
		t.Fatalf("load the remote API contract: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("the remote API contract is not a valid OpenAPI document: %v", err)
	}
	return doc
}

func newContract(t *testing.T, handler http.Handler) *contract {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	doc := loadContract(t)
	doc.Servers = openapi3.Servers{{URL: srv.URL}} // route this test server's URLs
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &contract{t: t, doc: doc, srv: srv, router: router, ops: map[string]bool{}, kinds: map[string]bool{}}
}

// call sends a request, validates the request and the response against the
// spec, and returns the status and decoded JSON body.
func (c *contract) call(method, path, body string, login string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if login != "" {
		req.Header.Set("Tailscale-User-Login", login)
	}
	route, params, err := c.router.FindRoute(req)
	if err != nil {
		c.t.Fatalf("%s %s is not in the contract: %v", method, path, err)
	}
	c.mu.Lock()
	c.ops[route.Operation.OperationID] = true
	c.mu.Unlock()
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: opts}
	// The request is validated on a copy of its body; the server gets the
	// original.
	check, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	check.Header = req.Header.Clone()
	in.Request = check
	reqErr := openapi3filter.ValidateRequest(context.Background(), in)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	in.Request = req
	if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header,
		Body: io.NopCloser(bytes.NewReader(raw)), Options: opts,
	}); err != nil {
		c.t.Fatalf("%s %s -> %d %s: response breaks the contract: %v", method, path, resp.StatusCode, raw, err)
	}
	// A request the spec rejects must be one the server rejects too.
	if reqErr != nil && resp.StatusCode < 400 {
		c.t.Fatalf("%s %s: the server accepted a request the contract rejects: %v", method, path, reqErr)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// checkEvent validates one event and its kind's data.
func (c *contract) checkEvent(raw []byte) map[string]any {
	c.t.Helper()
	var e map[string]any
	if err := json.Unmarshal(raw, &e); err != nil {
		c.t.Fatalf("event %s is not JSON: %v", raw, err)
	}
	if err := c.doc.Components.Schemas["Event"].Value.VisitJSON(e); err != nil {
		c.t.Fatalf("event %s breaks the contract: %v", raw, err)
	}
	kind, _ := e["kind"].(string)
	name := eventDataSchema[kind]
	if err := c.doc.Components.Schemas[name].Value.VisitJSON(e["data"]); err != nil {
		c.t.Fatalf("%s data %v breaks %s: %v", kind, e["data"], name, err)
	}
	c.mu.Lock()
	c.kinds[kind] = true
	c.mu.Unlock()
	return e
}

// stream reads the SSE stream from after until an event of kind until,
// checking every frame.
func (c *contract) stream(session string, after int64, until string) []map[string]any {
	c.t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/v1/sessions/%s/events?after=%d", c.srv.URL, session, after), nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	route, _, err := c.router.FindRoute(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.ops[route.Operation.OperationID] = true
	c.mu.Unlock()
	if _, ok := route.Operation.Responses.Status(200).Value.Content["text/event-stream"]; !ok {
		c.t.Fatal("the contract does not declare the event stream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || !strings.HasPrefix(ct, "text/event-stream") {
		c.t.Fatalf("stream: %d %q", resp.StatusCode, ct)
	}
	var out []map[string]any
	var id, event string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			e := c.checkEvent([]byte(strings.TrimPrefix(line, "data: ")))
			if event != e["kind"] || !strings.HasSuffix(id, fmt.Sprintf(".%v", e["seq"])) {
				c.t.Fatalf("frame id %q event %q for %v: want id <log>.<seq> and event = kind", id, event, e)
			}
			out = append(out, e)
			if e["kind"] == until {
				return out
			}
		}
	}
	c.t.Fatalf("stream ended before %s; got %d events", until, len(out))
	return nil
}

func TestContract(t *testing.T) {
	var b *Broker
	release := make(chan struct{})
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		switch text {
		case "ask":
			req := permissionRequest()
			req.SessionId = acp.SessionId(s)
			_, _ = b.Approver()(ctx, req)
		case "fail":
			return "", errors.New("the agent could not start")
		case "wait":
			<-release
			return "end_turn", nil
		}
		on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`)})
		return "end_turn", nil
	}}
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: time.Minute})
	now := time.Now().UTC().Truncate(time.Second)
	sessions := []Session{
		{ID: "s1", Provider: "claude", Title: "Fix the cache", Project: "/w/app", LastActive: now, State: StateIdle, LastPrompt: "make the TTL configurable"},
		{ID: "s9", Provider: "claude", Title: "Read only", Project: "/w/app", LastActive: now, State: StateOpenInTerminal},
	}
	api := &API{Version: "test", Broker: b,
		Sessions: func(context.Context, int, bool) ([]Session, error) { return sessions, nil },
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			if id != "s1" {
				return SessionDetail{}, acprun.ErrUnknownSession
			}
			return SessionDetail{Session: sessions[0], Turns: []Turn{
				{Number: 1, Time: now, User: "make the TTL configurable", Reply: "Done.", Tools: []TurnTool{{Name: "Edit", Snippet: "cache.go", OK: true}}},
			}}, nil
		},
		Search: func(context.Context, string, int) ([]SearchHit, error) {
			return []SearchHit{{Session: sessions[0], Snippet: "the [cache] TTL"}}, nil
		},
		Projects: func(context.Context) ([]Project, error) {
			return []Project{{Path: "/w/app", Name: "app", LastActive: now}}, nil
		},
		Create: func(context.Context, string, string) (string, error) { return "new-1", nil },
	}
	devices := &push.DeviceStore{Path: filepath.Join(t.TempDir(), "devices.json")}
	api.RegisterDevice, api.UnregisterDevice = devices.Add, devices.Remove
	c := newContract(t, api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	expect := func(want int, got int, body map[string]any, what string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: %d %v, want %d", what, got, body, want)
		}
	}

	// Reading.
	st, body := c.call("GET", "/v1/health", "", testLogin)
	expect(200, st, body, "health")
	st, body = c.call("GET", "/v1/sessions?limit=10", "", testLogin)
	expect(200, st, body, "list")
	st, body = c.call("GET", "/v1/sessions?limit=0", "", testLogin)
	expect(400, st, body, "list with a bad limit")
	st, body = c.call("GET", "/v1/sessions/s1?turns=5", "", testLogin)
	expect(200, st, body, "detail")
	st, body = c.call("GET", "/v1/sessions/nope", "", testLogin)
	expect(404, st, body, "unknown session")
	st, body = c.call("GET", "/v1/search?q=cache", "", testLogin)
	expect(200, st, body, "search")
	st, body = c.call("GET", "/v1/search?q=", "", testLogin)
	expect(400, st, body, "empty search")
	st, body = c.call("GET", "/v1/projects", "", testLogin)
	expect(200, st, body, "projects")
	st, body = c.call("GET", "/v1/openapi.yaml", "", testLogin)
	expect(200, st, body, "contract")

	// Identity.
	st, body = c.call("GET", "/v1/health", "", "")
	expect(401, st, body, "no identity")
	st, body = c.call("GET", "/v1/health", "", "someone@else.example")
	expect(403, st, body, "not allowed")

	// A turn, followed on the stream: turn_started, update, turn_done.
	st, body = c.call("POST", "/v1/sessions/s1/messages", `{"text":"hello","key":"k1"}`, testLogin)
	expect(202, st, body, "send")
	c.stream("s1", int64(body["after"].(float64)), KindTurnDone)
	st, body = c.call("GET", "/v1/sessions/s1/events?wait=0", "", testLogin)
	expect(200, st, body, "event page")
	st, body = c.call("GET", "/v1/sessions/s1/events?after=x&wait=0", "", testLogin)
	expect(400, st, body, "bad cursor")
	st, body = c.call("POST", "/v1/sessions/s1/messages", `{"text":""}`, testLogin)
	expect(400, st, body, "empty message")

	// Approvals: approval, a refused option, the answer, approval_resolved.
	st, body = c.call("POST", "/v1/sessions/s2/messages", `{"text":"ask"}`, testLogin)
	expect(202, st, body, "send that asks")
	evs := waitFor(t, b, "s2", KindApproval)
	var approvalID string
	for _, e := range evs {
		if e.Kind == KindApproval {
			var d ApprovalData
			_ = json.Unmarshal(e.Data, &d)
			approvalID = d.ApprovalID
		}
	}
	st, body = c.call("POST", "/v1/approvals/"+approvalID, `{"option":"sudo"}`, testLogin)
	expect(400, st, body, "invalid option")
	st, body = c.call("POST", "/v1/approvals/"+approvalID, `{"option":"allow"}`, testLogin)
	expect(200, st, body, "answer")
	c.stream("s2", 0, KindTurnDone)
	st, body = c.call("POST", "/v1/approvals/"+approvalID, `{"option":"allow"}`, testLogin)
	expect(404, st, body, "answered twice")

	// A failing turn: turn_error.
	st, body = c.call("POST", "/v1/sessions/s3/messages", `{"text":"fail"}`, testLogin)
	expect(202, st, body, "send that fails")
	c.stream("s3", 0, KindTurnError)

	// Busy and cancel.
	st, body = c.call("POST", "/v1/sessions/s4/messages", `{"text":"wait"}`, testLogin)
	expect(202, st, body, "long turn")
	st, body = c.call("POST", "/v1/sessions/s4/messages", `{"text":"again"}`, testLogin)
	expect(409, st, body, "busy")
	st, body = c.call("POST", "/v1/sessions/s4/cancel", "", testLogin)
	expect(202, st, body, "cancel")
	close(release)
	waitFor(t, b, "s4", KindTurnDone)
	st, body = c.call("POST", "/v1/sessions/s4/cancel", "", testLogin)
	expect(409, st, body, "cancel with nothing running")

	// New sessions.
	st, body = c.call("POST", "/v1/sessions", `{"cwd":"/w/app","text":"plan the release","key":"n1"}`, testLogin)
	expect(202, st, body, "new session")
	st, body = c.call("POST", "/v1/sessions", `{"cwd":"/etc","text":"x"}`, testLogin)
	expect(400, st, body, "new session outside the projects")

	// Push devices.
	token := strings.Repeat("0a", 32)
	st, body = c.call("POST", "/v1/devices", `{"token":"`+token+`","environment":"sandbox"}`, testLogin)
	expect(200, st, body, "register a device")
	if ds, _ := devices.List(); len(ds) != 1 || ds[0].Token != token || ds[0].Login != testLogin {
		t.Fatalf("registered devices %+v", ds)
	}
	st, body = c.call("POST", "/v1/devices", `{"token":"xyz","environment":"sandbox"}`, testLogin)
	expect(400, st, body, "register a bad token")
	st, body = c.call("DELETE", "/v1/devices/"+token, "", testLogin)
	expect(200, st, body, "unregister a device")
	if ds, _ := devices.List(); len(ds) != 0 {
		t.Fatalf("devices after unregister %+v", ds)
	}
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if !strings.Contains(fmt.Sprint(body["capabilities"]), CapPush) {
		t.Fatalf("capabilities %v, want %s", body["capabilities"], CapPush)
	}

	// Every operation and event kind in the contract was exercised.
	var missing []string
	for path, item := range c.doc.Paths.Map() {
		for method, op := range item.Operations() {
			if !c.ops[op.OperationID] {
				missing = append(missing, method+" "+path)
			}
		}
	}
	for kind := range eventDataSchema {
		if !c.kinds[kind] {
			missing = append(missing, "event "+kind)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the contract test never exercised: %v", missing)
	}
}

// A server without search or new sessions says so: 404 not_supported, and
// the capabilities leave them out.
func TestContract_UnsupportedEndpoints(t *testing.T) {
	api := &API{Version: "test", Broker: NewBroker(BrokerDeps{}, BrokerOptions{}),
		Sessions: func(context.Context, int, bool) ([]Session, error) { return nil, nil }}
	c := newContract(t, api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	st, body := c.call("GET", "/v1/search?q=x", "", testLogin)
	if st != 404 || body["code"] != "not_supported" {
		t.Fatalf("search: %d %v", st, body)
	}
	st, body = c.call("POST", "/v1/sessions", `{"cwd":"/w","text":"x"}`, testLogin)
	if st != 404 || body["code"] != "not_supported" {
		t.Fatalf("new session: %d %v", st, body)
	}
	st, body = c.call("POST", "/v1/devices", `{"token":"`+strings.Repeat("0a", 32)+`","environment":"sandbox"}`, testLogin)
	if st != 404 || body["code"] != "not_supported" {
		t.Fatalf("register a device: %d %v", st, body)
	}
	st, body = c.call("DELETE", "/v1/devices/"+strings.Repeat("0a", 32), "", testLogin)
	if st != 404 || body["code"] != "not_supported" {
		t.Fatalf("unregister a device: %d %v", st, body)
	}
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if fmt.Sprint(body["capabilities"]) != "[sessions]" {
		t.Fatalf("capabilities %v, want only sessions", body["capabilities"])
	}
}

// Every error the server can map has a code the contract lists.
func TestContract_ErrorCodes(t *testing.T) {
	doc := loadContract(t)
	schema := doc.Components.Schemas["ErrorBody"].Value
	for _, err := range []error{
		acprun.ErrUnknownSession, acprun.ErrOpenInTerminal, acprun.ErrBusy, acprun.ErrInUseElsewhere,
		acprun.ErrOwnershipUnknown, acprun.ErrNotRunning, acprun.ErrTooManyRuns,
		ErrNoSuchApproval, ErrInvalidOption, errNotAProject, errors.New("anything else"),
	} {
		w := httptest.NewRecorder()
		writeFailure(w, err)
		var body any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if err2 := schema.VisitJSON(body); err2 != nil {
			t.Errorf("%v -> %s: not in the contract: %v", err, w.Body.String(), err2)
		}
	}
}

// recordedExchange is one request and its response, as recorded from a real
// client (see TestContract_RecordedTraffic).
type recordedExchange struct {
	Method       string `json:"method"`
	Path         string `json:"path"` // with the query string
	RequestBody  string `json:"requestBody"`
	Status       int    `json:"status"`
	ContentType  string `json:"contentType"`
	ResponseBody string `json:"responseBody"`
}

// TestContract_RecordedTraffic checks a client against the contract: set
// AIMUX_CONTRACT_TRAFFIC to a JSON Lines file of recordedExchange (one per
// request, recorded from the web client in a browser or from any other
// client) and every request and response must match the spec. Event
// streams are left out; their events are checked by TestContract.
func TestContract_RecordedTraffic(t *testing.T) {
	file := os.Getenv("AIMUX_CONTRACT_TRAFFIC")
	if file == "" {
		t.Skip("set AIMUX_CONTRACT_TRAFFIC to a recorded traffic file to check a client")
	}
	data, err := os.ReadFile(file) // #nosec G304,G703 -- a test input the developer names
	if err != nil {
		t.Fatal(err)
	}
	doc := loadContract(t)
	doc.Servers = openapi3.Servers{{URL: "http://client.test"}}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	n := 0
	for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var x recordedExchange
		if err := json.Unmarshal(line, &x); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		req, _ := http.NewRequest(x.Method, "http://client.test"+x.Path, strings.NewReader(x.RequestBody))
		if x.RequestBody != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		route, params, err := router.FindRoute(req)
		if err != nil {
			t.Errorf("%s %s: the client calls something the contract does not have: %v", x.Method, x.Path, err)
			continue
		}
		in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: opts}
		if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
			t.Errorf("%s %s: request breaks the contract: %v", x.Method, x.Path, err)
		}
		if strings.HasPrefix(x.ContentType, "text/event-stream") {
			continue
		}
		h := http.Header{}
		h.Set("Content-Type", x.ContentType)
		if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
			RequestValidationInput: in, Status: x.Status, Header: h,
			Body: io.NopCloser(strings.NewReader(x.ResponseBody)), Options: opts,
		}); err != nil {
			t.Errorf("%s %s -> %d: response breaks the contract: %v", x.Method, x.Path, x.Status, err)
		}
		n++
	}
	t.Logf("checked %d exchanges", n)
}
