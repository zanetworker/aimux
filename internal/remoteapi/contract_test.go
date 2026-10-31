package remoteapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
)

// contractTerminal types anything but "prompt showing" and presses only on
// prompt p1.
type contractTerminal struct{}

func (contractTerminal) Send(_ context.Context, id, text, _ string) error {
	switch {
	case id == "nope":
		return terminput.ErrNotInTerminal
	case text == "prompt showing":
		return terminput.ErrNotAtInput
	case text == "while Claude works":
		return terminput.ErrBusy
	}
	return nil
}

func (c contractTerminal) SendReplacing(ctx context.Context, id, text, key string) error {
	return c.Send(ctx, id, text, key)
}

func (contractTerminal) Press(_ context.Context, _, promptID string, _ terminput.Key, _ string) error {
	if promptID != "p1" {
		return terminput.ErrPromptChanged
	}
	return nil
}

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
	KindQueue:            "QueueStatus",
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
	return c.streamQuery(session, fmt.Sprintf("after=%d", after), until)
}

// streamQuery is stream with the query string given.
func (c *contract) streamQuery(session, query, until string) []map[string]any {
	c.t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/v1/sessions/%s/events?%s", c.srv.URL, session, query), nil)
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
			if id == "s9" {
				return SessionDetail{Session: sessions[1], Turns: []Turn{}, Terminal: &TerminalStatus{Status: "idle", CanType: false,
					Reason: "not_at_input", Prompt: &TerminalPrompt{PromptID: "p1", Question: "Do you want to create a.go?", Detail: "Create file\na.go\n  1 package a",
						Options: []TerminalOption{{Key: "1", Label: "Yes"}, {Key: "2", Label: "Yes, and allow all edits"}, {Key: "3", Label: "No"}}}}}, nil
			}
			if id != "s1" {
				return SessionDetail{}, acprun.ErrUnknownSession
			}
			return SessionDetail{Session: sessions[0], Turns: []Turn{
				{Number: 1, Time: now, User: "make the TTL configurable", Reply: "Done.", Tools: []TurnTool{{Name: "Edit", Snippet: "cache.go", OK: true, Detail: &ToolDetail{Input: "cache.go", Old: "ttl := 5\n", New: "ttl := cfg.TTL\n"}}, {Name: "Bash", Snippet: "$ go test", OK: false, Detail: &ToolDetail{Error: "FAIL"}}}},
			}, Stats: &SessionStats{CostUSD: 1.25, TokensIn: 1000, TokensOut: 200, CacheReadTokens: 5000, CacheWriteTokens: 300,
				Turns: 12, StartTime: &now, DurationMin: 42.5, Model: "claude-opus-x", GitBranch: "main"},
				Repo: &Repo{Web: "https://github.com/acme/widget", Branch: "main", Root: "/w/app"}}, nil
		},
		Search: func(context.Context, string, int) ([]SearchHit, error) {
			return []SearchHit{{Session: sessions[0], Snippet: "the [cache] TTL"}}, nil
		},
		Projects: func(context.Context) ([]Project, error) {
			return []Project{{Path: "/w/app", Name: "app", LastActive: now}}, nil
		},
		Create: func(context.Context, string, string) (string, error) { return "new-1", nil },
	}
	api.TerminalInput, api.TerminalEnabled = &contractTerminal{}, true
	queue, err := chatqueue.Open(filepath.Join(t.TempDir(), "remote-queue.json"), time.Now, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	api.Queue = queue
	queueCtx, stopQueue := context.WithCancel(context.Background())
	defer stopQueue()
	go api.QueueEvents(queueCtx)
	api.Links = &Links{JiraBase: "https://jira.example.com", JiraProjects: []string{"WID"}}
	devices := &push.DeviceStore{Path: filepath.Join(t.TempDir(), "devices.json")}
	api.RegisterDevice, api.UnregisterDevice = devices.Add, devices.Remove
	// Titles, descriptions, pins and Last on your Mac.
	metaFile := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(metaFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api.Record = func(_ context.Context, id string) (history.Session, error) {
		if id != "s1" {
			return history.Session{}, acprun.ErrUnknownSession
		}
		m := history.LoadMeta(metaFile)
		return history.Session{ID: "s1", FilePath: metaFile, FirstPrompt: "make the TTL configurable", Title: m.Title,
			Description: m.Description, DescriptionBy: m.DescriptionBy, DescribedAt: m.DescribedAt, Starred: m.Starred}, nil
	}
	naming := &fakeNaming{status: history.NamingStatus{Today: 3, Limit: 20, Failing: true, Reason: "timed out", Paused: true},
		result: history.SummaryResult{Title: "TTL setting", Description: "Makes the cache TTL configurable.", Changed: []string{"title", "description"}}}
	api.Naming, api.AutoName = naming, true
	api.LastOnMac = func() (string, time.Time, bool) { return "s1", now, true }
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
	if links, _ := body["links"].(map[string]any); links["jiraBase"] != "https://jira.example.com" {
		t.Fatalf("health links %v, want the configured Jira base", body["links"])
	}
	st, body = c.call("GET", "/v1/sessions?limit=10", "", testLogin)
	expect(200, st, body, "list")
	st, body = c.call("GET", "/v1/sessions?limit=0", "", testLogin)
	expect(400, st, body, "list with a bad limit")
	st, body = c.call("GET", "/v1/sessions/s1?turns=5", "", testLogin)
	expect(200, st, body, "detail")
	if stats, _ := body["stats"].(map[string]any); stats["costUsd"] != 1.25 || stats["turns"] != float64(12) {
		t.Fatalf("detail stats %v, want the session's stats", body["stats"])
	}
	if repo, _ := body["repo"].(map[string]any); repo["web"] != "https://github.com/acme/widget" || repo["branch"] != "main" || repo["root"] != "/w/app" {
		t.Fatalf("detail repo %v, want the session's repository", body["repo"])
	}
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

	// Terminal input: a detail with its terminal status, typing, pressing.
	st, body = c.call("GET", "/v1/sessions/s9", "", testLogin)
	expect(200, st, body, "detail of a terminal session")
	if term, _ := body["terminal"].(map[string]any); term["reason"] != "not_at_input" || term["prompt"] == nil {
		t.Fatalf("terminal %v, want the status with its prompt", body["terminal"])
	}
	st, body = c.call("POST", "/v1/sessions/s9/terminal/messages", `{"text":"run the tests","key":"t1"}`, testLogin)
	expect(202, st, body, "type a message")
	st, body = c.call("POST", "/v1/sessions/s9/terminal/messages", `{"text":"prompt showing"}`, testLogin)
	expect(409, st, body, "type while a prompt shows")
	st, body = c.call("POST", "/v1/sessions/s9/terminal/messages", `{"text":""}`, testLogin)
	expect(400, st, body, "type nothing")
	// A client without the queue sees a working session refuse a message.
	st, body = c.call("POST", "/v1/sessions/s9/terminal/messages", `{"text":"while Claude works"}`, testLogin)
	expect(409, st, body, "type while Claude works")
	if body["code"] != "busy" || body["error"] != terminalMessages["busy"].msg {
		t.Fatalf("busy terminal: %v, want busy with its fixed message", body)
	}
	st, body = c.call("POST", "/v1/sessions/nope/terminal/messages", `{"text":"a"}`, testLogin)
	expect(404, st, body, "type into an unknown session")
	st, body = c.call("POST", "/v1/sessions/s9/terminal/keys", `{"promptId":"p1","option":"1","key":"t2"}`, testLogin)
	expect(202, st, body, "press an option")
	st, body = c.call("POST", "/v1/sessions/s9/terminal/keys", `{"promptId":"old","option":"1"}`, testLogin)
	expect(409, st, body, "press on a prompt that changed")
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if !strings.Contains(fmt.Sprint(body["capabilities"]), CapTerminalInput) {
		t.Fatalf("capabilities %v, want %s", body["capabilities"], CapTerminalInput)
	}
	api.TerminalEnabled = false
	st, body = c.call("POST", "/v1/sessions/s9/terminal/keys", `{"promptId":"p1","option":"1"}`, testLogin)
	expect(403, st, body, "press while terminal input is off")

	// The chat queue: add, read, edit, move, delete, resume, its errors, the
	// detail's queue and queue events.
	st, body = c.call("GET", "/v1/sessions/s1/queue", "", testLogin)
	expect(200, st, body, "empty queue")
	st, body = c.call("POST", "/v1/sessions/s1/queue", `{"text":"run the tests","key":"q1"}`, testLogin)
	expect(200, st, body, "queue a message")
	first, _ := body["id"].(string)
	st, body = c.call("POST", "/v1/sessions/s1/queue", `{"text":"then commit"}`, testLogin)
	expect(200, st, body, "queue another")
	second, _ := body["id"].(string)
	st, body = c.call("POST", "/v1/sessions/s1/queue", `{"text":""}`, testLogin)
	expect(400, st, body, "queue nothing")
	st, body = c.call("POST", "/v1/sessions/s1/queue", `{"text":"!ls"}`, testLogin)
	expect(409, st, body, "queue bash mode")
	st, body = c.call("POST", "/v1/sessions/nope/queue", `{"text":"a"}`, testLogin)
	expect(404, st, body, "queue into an unknown session")
	st, body = c.call("PATCH", "/v1/sessions/s1/queue/"+first, `{"text":"run all the tests"}`, testLogin)
	expect(200, st, body, "edit a queued message")
	st, body = c.call("PATCH", "/v1/sessions/s1/queue/"+second, `{"position":0}`, testLogin)
	expect(200, st, body, "move a queued message")
	st, body = c.call("PATCH", "/v1/sessions/s1/queue/"+second, `{"position":0,"text":"x"}`, testLogin)
	expect(400, st, body, "edit and move at once")
	st, body = c.call("PATCH", "/v1/sessions/s1/queue/nope", `{"position":0}`, testLogin)
	expect(404, st, body, "move an unknown message")
	if _, err := queue.MarkSending("s1", second); err != nil {
		t.Fatal(err)
	}
	st, body = c.call("DELETE", "/v1/sessions/s1/queue/"+second, "", testLogin)
	expect(409, st, body, "delete the message being sent")
	if err := queue.Pause("s1", chatqueue.ReasonNoAnswer); err != nil {
		t.Fatal(err)
	}
	st, body = c.call("GET", "/v1/sessions/s1", "", testLogin)
	expect(200, st, body, "detail with a queue")
	if q, _ := body["queue"].(map[string]any); q["state"] != chatqueue.QueuePaused || q["reason"] != chatqueue.ReasonNoAnswer {
		t.Fatalf("detail queue %v, want it paused with its reason", body["queue"])
	}
	st, body = c.call("POST", "/v1/sessions/s1/queue/resume", "", testLogin)
	expect(200, st, body, "resume")
	st, body = c.call("DELETE", "/v1/sessions/s1/queue/"+first, "", testLogin)
	expect(200, st, body, "delete a queued message")
	c.streamQuery("s1", "queue=1&after=0", KindQueue)
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if !strings.Contains(fmt.Sprint(body["capabilities"]), CapQueue) {
		t.Fatalf("capabilities %v, want %s", body["capabilities"], CapQueue)
	}

	// Titles, descriptions and pins.
	st, body = c.call("GET", "/v1/sessions", "", testLogin)
	expect(200, st, body, "list with Last on your Mac")
	if lm, _ := body["lastOnMac"].(map[string]any); lm["id"] != "s1" {
		t.Fatalf("lastOnMac %v", body["lastOnMac"])
	}
	st, body = c.call("PATCH", "/v1/sessions/s1/meta", `{"title":"Cache TTL","description":"Owner words.","pinned":true}`, testLogin)
	expect(200, st, body, "edit title, description and pin")
	st, body = c.call("PATCH", "/v1/sessions/s1/meta", `{}`, testLogin)
	expect(400, st, body, "edit nothing")
	st, body = c.call("PATCH", "/v1/sessions/nope/meta", `{"pinned":true}`, testLogin)
	expect(404, st, body, "edit an unknown session")
	st, body = c.call("PATCH", "/v1/sessions/s1/meta", `{"title":"","description":""}`, testLogin)
	expect(200, st, body, "clear the owner's values")
	st, body = c.call("POST", "/v1/sessions/s1/describe", "", testLogin)
	expect(200, st, body, "update summary")
	naming.set(func(f *fakeNaming) { f.err = history.ErrOwnerSet })
	st, body = c.call("POST", "/v1/sessions/s1/describe", "", testLogin)
	expect(409, st, body, "update summary over owner values")
	naming.set(func(f *fakeNaming) { f.err = history.ErrTimeout })
	st, body = c.call("POST", "/v1/sessions/s1/describe", "", testLogin)
	expect(504, st, body, "update summary timing out")
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if n, _ := body["naming"].(map[string]any); !strings.Contains(fmt.Sprint(body["capabilities"]), CapDescribe) || n["limit"] != 20.0 {
		t.Fatalf("health %v, want describe and naming", body)
	}

	// Every operation and event kind in the contract was exercised; the
	// /v1/auth operations by TestContract_DeviceLogin.
	var missing []string
	for path, item := range c.doc.Paths.Map() {
		if strings.HasPrefix(path, "/v1/auth/") {
			continue
		}
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
	for _, err := range []error{
		terminput.ErrDisabled, terminput.ErrNotInTerminal, terminput.ErrNotClaude, terminput.ErrMacInUse,
		terminput.ErrNoDriver, terminput.ErrAutomationDenied, terminput.ErrNotAtInput, terminput.ErrNoPrompt,
		terminput.ErrPromptChanged, terminput.ErrBadText, terminput.ErrNotSubmitted, errors.New("anything else"),
	} {
		w := httptest.NewRecorder()
		writeTerminalFailure(w, err, io.Discard)
		var body any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if err2 := schema.VisitJSON(body); err2 != nil {
			t.Errorf("%v -> %s: not in the contract: %v", err, w.Body.String(), err2)
		}
	}
	for _, f := range namingFailures {
		w := httptest.NewRecorder()
		(&API{}).writeNamingFailure(w, "s1", f.err)
		var body any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if err2 := schema.VisitJSON(body); err2 != nil {
			t.Errorf("%v -> %s: not in the contract: %v", f.err, w.Body.String(), err2)
		}
	}
	for _, err := range []error{
		chatqueue.ErrFull, chatqueue.ErrItemSending, chatqueue.ErrBadText, chatqueue.ErrNotFound, errors.New("anything else"),
	} {
		w := httptest.NewRecorder()
		writeQueueFailure(w, err)
		var body any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if err2 := schema.VisitJSON(body); err2 != nil {
			t.Errorf("%v -> %s: not in the contract: %v", err, w.Body.String(), err2)
		}
	}
}

// A PATCH of a queued message carries exactly one of text and position.
func TestContract_QueuePatchHoldsOneField(t *testing.T) {
	schema := loadContract(t).Components.Schemas["QueuePatchRequest"].Value
	for body, ok := range map[string]bool{
		`{"text":"a"}`: true, `{"position":0}`: true, `{}`: false, `{"text":"a","position":0}`: false,
	} {
		var v any
		_ = json.Unmarshal([]byte(body), &v)
		if err := schema.VisitJSON(v); (err == nil) != ok {
			t.Errorf("%s: valid %v, want %v (%v)", body, err == nil, ok, err)
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

// callAs is call with a session token or cookie and extra headers.
func (c *contract) callAs(method, path, body, bearer, cookie string, headers ...string) (int, map[string]any, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Tailscale-User-Login", testLogin)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		req.Header.Set("Cookie", "__Host-aimux_session="+cookie)
	}
	for _, h := range headers {
		k, v, _ := strings.Cut(h, ": ")
		req.Header.Set(k, v)
	}
	route, params, err := c.router.FindRoute(req)
	if err != nil {
		c.t.Fatalf("%s %s is not in the contract: %v", method, path, err)
	}
	c.mu.Lock()
	c.ops[route.Operation.OperationID] = true
	c.mu.Unlock()
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	check, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	check.Header = req.Header.Clone()
	in := &openapi3filter.RequestValidationInput{Request: check, PathParams: params, Route: route, Options: opts}
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
	if reqErr != nil && resp.StatusCode < 400 {
		c.t.Fatalf("%s %s: the server accepted a request the contract rejects: %v", method, path, reqErr)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, resp.Header
}

// The /v1/auth operations and login_required, against the contract.
func TestContract_DeviceLogin(t *testing.T) {
	dir := t.TempDir()
	dl, err := devicelogin.NewServer(devicelogin.Options{Store: devicelogin.Open(dir),
		Login: func(r *http.Request) string { return LoginFrom(r.Context()) }})
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Version: "test", Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), DeviceLogin: dl,
		Sessions: func(context.Context, int, bool) ([]Session, error) { return []Session{}, nil }}
	c := newContract(t, api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	expect := func(want, got int, body map[string]any, what string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: %d %v, want %d", what, got, body, want)
		}
	}
	store := devicelogin.Open(dir)
	b64 := base64.StdEncoding

	st, body, _ := c.callAs("GET", "/v1/health", "", "", "")
	expect(200, st, body, "health without a session")
	if body["session"] != "none" {
		t.Fatalf("health session %v, want none", body["session"])
	}
	if !strings.Contains(fmt.Sprint(body["capabilities"]), CapDeviceLogin) {
		t.Fatalf("capabilities %v, want %s", body["capabilities"], CapDeviceLogin)
	}
	st, body, _ = c.callAs("GET", "/v1/sessions", "", "", "")
	expect(401, st, body, "sessions without a session")
	if body["code"] != "login_required" {
		t.Fatalf("code %v, want login_required", body["code"])
	}

	// Pairing an iPhone: refused proof, then a good one.
	code, hash := devicelogin.NewCode()
	if err := store.SetPending("iphone", hash, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	signB64 := func(msg []byte) string {
		h := sha256.Sum256(msg)
		sig, _ := ecdsa.SignASN1(rand.Reader, k, h[:])
		return b64.EncodeToString(sig)
	}
	canon, _ := devicelogin.CanonicalCode(code)
	pairBody := func(sig string) string {
		j, _ := json.Marshal(map[string]string{"code": code, "kind": "secure-enclave", "publicKey": b64.EncodeToString(der), "signature": sig})
		return string(j)
	}
	st, body, _ = c.callAs("POST", "/v1/auth/pair", pairBody(signB64([]byte("other"))), "", "")
	expect(401, st, body, "pair with a bad proof")
	st, body, _ = c.callAs("POST", "/v1/auth/pair", `{"code":"ABCD-1234","kind":"secure-enclave","publicKey":"AAAA"}`, "", "")
	expect(400, st, body, "pair with a bad key")
	st, body, _ = c.callAs("POST", "/v1/auth/pair", pairBody(signB64(devicelogin.PairMessage("127.0.0.1", canon))), "", "")
	expect(200, st, body, "pair")
	token, _ := body["token"].(string)
	deviceID, _ := body["deviceId"].(string)

	// Pairing a browser: the session comes as a cookie.
	code2, hash2 := devicelogin.NewCode()
	if err := store.SetPending("safari", hash2, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	canon2, _ := devicelogin.CanonicalCode(code2)
	ch := sha256.Sum256([]byte(canon2))
	rp := sha256.Sum256([]byte("127.0.0.1"))
	cd, _ := json.Marshal(map[string]string{"type": "webauthn.create", "challenge": base64.RawURLEncoding.EncodeToString(ch[:]), "origin": "https://" + c.srv.URL[len("http://"):]})
	ad := append(append(rp[:], 0x45, 0, 0, 0, 0), make([]byte, 60)...) // UP, UV, AT
	userHandle := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	web, _ := json.Marshal(map[string]string{"code": code2, "kind": "webauthn", "publicKey": b64.EncodeToString(der),
		"credentialId": "Y3JlZA", "userHandle": userHandle, "clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad)})
	st, body, hdr := c.callAs("POST", "/v1/auth/pair", string(web), "", "")
	expect(200, st, body, "pair a passkey")
	cookie, _ := strings.CutPrefix(strings.Split(hdr.Get("Set-Cookie"), ";")[0], "__Host-aimux_session=")
	if _, has := body["token"]; has || cookie == "" {
		t.Fatalf("web pairing: body %v, Set-Cookie %q", body, hdr.Get("Set-Cookie"))
	}
	st, body, _ = c.callAs("POST", "/v1/auth/pair", string(web), "", "")
	expect(401, st, body, "pair with a used code")

	// Login.
	st, body, _ = c.callAs("POST", "/v1/auth/challenge", `{"deviceId":"nope"}`, "", "")
	expect(404, st, body, "challenge for an unknown device")
	st, body, _ = c.callAs("POST", "/v1/auth/challenge", `{"deviceId":"`+deviceID+`"}`, "", "")
	expect(200, st, body, "challenge")
	challenge, _ := body["challenge"].(string)
	login := func(sig string) string {
		j, _ := json.Marshal(map[string]string{"deviceId": deviceID, "challenge": challenge, "signature": sig})
		return string(j)
	}
	st, body, _ = c.callAs("POST", "/v1/auth/login", login(signB64([]byte("other"))), "", "")
	expect(401, st, body, "login with a bad signature")
	st, body, _ = c.callAs("POST", "/v1/auth/challenge", `{"deviceId":"`+deviceID+`"}`, "", "")
	expect(200, st, body, "second challenge")
	challenge, _ = body["challenge"].(string)
	st, body, _ = c.callAs("POST", "/v1/auth/login", login(signB64(devicelogin.LoginMessage("127.0.0.1", challenge))), "", "")
	expect(200, st, body, "login")
	st, body, _ = c.callAs("POST", "/v1/auth/login", `{"deviceId":"nope","challenge":"x","signature":"AA=="}`, "", "")
	expect(404, st, body, "login of an unknown device")

	// Sessions in use.
	st, body, _ = c.callAs("GET", "/v1/sessions", "", token, "", "Aimux-Interaction: 1")
	expect(200, st, body, "sessions with a Bearer token")
	st, body, _ = c.callAs("GET", "/v1/sessions", "", "", cookie)
	expect(200, st, body, "sessions with the cookie")
	st, body, _ = c.callAs("GET", "/v1/health", "", "", cookie)
	expect(200, st, body, "health with a session")
	if body["session"] != "live" {
		t.Fatalf("health session %v, want live", body["session"])
	}
	st, body, _ = c.callAs("POST", "/v1/auth/logout", "", token, "")
	expect(200, st, body, "logout")
	st, body, _ = c.callAs("POST", "/v1/auth/logout", "", token, "")
	expect(401, st, body, "logout twice")

	var missing []string
	for path, item := range c.doc.Paths.Map() {
		for method, op := range item.Operations() {
			if strings.HasPrefix(path, "/v1/auth/") && !c.ops[op.OperationID] {
				missing = append(missing, method+" "+path)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("never exercised: %v", missing)
	}
}

// The GET routes the clients mark with Aimux-Interaction when the person
// acts (devicelogin's TestInteraction_ClientActionsContract checks that each
// counts): the contract lists the header on each of them.
func TestContract_InteractionHeaderListed(t *testing.T) {
	doc := loadContract(t)
	for _, p := range []string{"/v1/sessions", "/v1/sessions/{id}", "/v1/search", "/v1/projects", "/v1/sessions/{id}/events"} {
		item := doc.Paths.Find(p)
		if item == nil || item.Get == nil {
			t.Fatalf("no GET %s in the contract", p)
		}
		if item.Get.Parameters.GetByInAndName("header", devicelogin.InteractionHeader) == nil {
			t.Errorf("GET %s does not list %s", p, devicelogin.InteractionHeader)
		}
	}
}
