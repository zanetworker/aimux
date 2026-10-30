package remoteapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
)

type queueRig struct {
	t     *testing.T
	srv   *httptest.Server
	api   *API
	store *chatqueue.Store
	b     *Broker
	f     *fakeBackend // the broker's ACP backend
	kicks atomic.Int32
	// details counts reads of the full session detail (terminal status,
	// repository); the queue routes must use SessionExists instead.
	details *atomic.Int32
}

// newQueueAPI serves a queue for the known session s1 (s2 is unknown).
func newQueueAPI(t *testing.T) *queueRig {
	t.Helper()
	store, err := chatqueue.Open(filepath.Join(t.TempDir(), "remote-queue.json"), time.Now, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	r := &queueRig{t: t, store: store, details: &atomic.Int32{}}
	r.f = &fakeBackend{}
	r.b = NewBroker(r.f.deps(&r.b), BrokerOptions{})
	r.api = &API{Broker: r.b, Queue: store,
		QueueKick: func() { r.kicks.Add(1) },
		QueueNote: func(session string) string {
			if session == "s1" {
				return chatqueue.NoteMacInUse
			}
			return ""
		},
		Sessions: func(context.Context, int, bool) ([]Session, error) { return nil, nil },
		SessionExists: func(_ context.Context, id string) error {
			if id != "s1" {
				return acprun.ErrUnknownSession
			}
			return nil
		},
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			r.details.Add(1)
			if id != "s1" {
				return SessionDetail{}, acprun.ErrUnknownSession
			}
			return SessionDetail{Session: Session{ID: "s1", State: StateIdle}, Turns: []Turn{}}, nil
		},
	}
	r.srv = httptest.NewServer(r.api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *queueRig) do(method, path, body string) (int, map[string]any) {
	r.t.Helper()
	resp, out := do(r.t, r.srv, method, path, body)
	return resp.StatusCode, out
}

func (r *queueRig) add(text, key string) string {
	r.t.Helper()
	st, body := r.do("POST", "/v1/sessions/s1/queue", fmt.Sprintf(`{"text":%q,"key":%q}`, text, key))
	if st != 200 {
		r.t.Fatalf("add %q: %d %v", text, st, body)
	}
	return body["id"].(string)
}

func queueTexts(body map[string]any) string {
	items, _ := body["items"].([]any)
	var out []string
	for _, it := range items {
		out = append(out, it.(map[string]any)["text"].(string))
	}
	return strings.Join(out, ",")
}

func wantCode(t *testing.T, st int, body map[string]any, status int, code, what string) {
	t.Helper()
	if st != status || body["code"] != code {
		t.Fatalf("%s: %d %v, want %d %s", what, st, body, status, code)
	}
}

func TestQueueAPI_AddAndGet(t *testing.T) {
	r := newQueueAPI(t)
	st, body := r.do("GET", "/v1/sessions/s1/queue", "")
	if st != 200 || body["state"] != chatqueue.QueueRunning || queueTexts(body) != "" {
		t.Fatalf("empty queue: %d %v", st, body)
	}
	id := r.add("run the tests", "k1")
	st, body = r.do("POST", "/v1/sessions/s1/queue", `{"text":"run the tests","key":"k1"}`)
	if st != 200 || body["id"] != id || body["state"] != chatqueue.StateWaiting {
		t.Fatalf("retry with the same key: %d %v, want the same item", st, body)
	}
	if _, hasKey := body["key"]; hasKey {
		t.Fatalf("item %v shows its key", body)
	}
	r.add("then commit", "")
	st, body = r.do("GET", "/v1/sessions/s1/queue", "")
	if st != 200 || queueTexts(body) != "run the tests,then commit" {
		t.Fatalf("queue: %d %v", st, body)
	}
	first := body["items"].([]any)[0].(map[string]any)
	if first["state"] != chatqueue.StateWaiting || first["created"] == nil {
		t.Fatalf("item %v", first)
	}
	if body["note"] != chatqueue.NoteMacInUse {
		t.Fatalf("note %v, want the dispatcher's waiting line", body["note"])
	}
	if r.kicks.Load() < 2 {
		t.Fatalf("kicks %d after two adds", r.kicks.Load())
	}
}

func TestQueueAPI_AddErrors(t *testing.T) {
	r := newQueueAPI(t)
	st, body := r.do("POST", "/v1/sessions/s1/queue", `{"text":"  "}`)
	wantCode(t, st, body, 400, "bad_request", "empty text")
	st, body = r.do("POST", "/v1/sessions/s1/queue", `{"text":"a","extra":1}`)
	wantCode(t, st, body, 400, "bad_request", "unknown field")
	st, body = r.do("POST", "/v1/sessions/s1/queue", `{"text":"!rm -rf /"}`)
	wantCode(t, st, body, 409, "bad_text", "bash mode")
	st, body = r.do("POST", "/v1/sessions/s1/queue", `{"text":"/clear"}`)
	wantCode(t, st, body, 409, "bad_text", "slash command")
	st, body = r.do("POST", "/v1/sessions/s2/queue", `{"text":"hi"}`)
	wantCode(t, st, body, 404, "unknown_session", "unknown session")
	st, body = r.do("GET", "/v1/sessions/s2/queue", "")
	wantCode(t, st, body, 404, "unknown_session", "get of an unknown session")
	for i := range chatqueue.MaxItems {
		r.add(fmt.Sprintf("item %d", i), "")
	}
	st, body = r.do("POST", "/v1/sessions/s1/queue", `{"text":"one more"}`)
	wantCode(t, st, body, 409, "queue_full", "21st item")
	if strings.Contains(fmt.Sprint(body), "chatqueue") {
		t.Fatalf("error %v shows the package's error text", body)
	}
}

func TestQueueAPI_RetryOfAnItemAlreadySentAnswersDone(t *testing.T) {
	r := newQueueAPI(t)
	id := r.add("hello", "k1")
	if st, body := r.do("DELETE", "/v1/sessions/s1/queue/"+id, ""); st != 200 {
		t.Fatalf("delete: %d %v", st, body)
	}
	st, body := r.do("POST", "/v1/sessions/s1/queue", `{"text":"hello","key":"k1"}`)
	if st != 200 || body["id"] != id || body["state"] != chatqueue.StateDone {
		t.Fatalf("retry after delete: %d %v, want the item as done", st, body)
	}
	if _, body := r.do("GET", "/v1/sessions/s1/queue", ""); queueTexts(body) != "" {
		t.Fatalf("queue %v, want it still empty", body)
	}
}

func TestQueueAPI_EditAndMove(t *testing.T) {
	r := newQueueAPI(t)
	a := r.add("a", "")
	r.add("b", "")
	c := r.add("c", "")
	st, body := r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{"text":"a2"}`)
	if st != 200 || queueTexts(body) != "a2,b,c" {
		t.Fatalf("edit: %d %v", st, body)
	}
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+c, `{"position":0}`)
	if st != 200 || queueTexts(body) != "c,a2,b" {
		t.Fatalf("move to 0: %d %v", st, body)
	}
	// With the first item in flight, positions count the waiting items.
	if _, err := r.store.MarkSending("s1", c); err != nil {
		t.Fatal(err)
	}
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{"position":1}`)
	if st != 200 || queueTexts(body) != "c,b,a2" {
		t.Fatalf("move among waiting items: %d %v", st, body)
	}
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+c, `{"text":"c2"}`)
	wantCode(t, st, body, 409, "item_sending", "edit the sending item")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+c, `{"position":2}`)
	wantCode(t, st, body, 409, "item_sending", "move the sending item")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{"text":"!ls"}`)
	wantCode(t, st, body, 409, "bad_text", "edit to bash mode")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{"text":" "}`)
	wantCode(t, st, body, 400, "bad_request", "edit to empty")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{"text":"x","position":0}`)
	wantCode(t, st, body, 400, "bad_request", "both text and position")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{}`)
	wantCode(t, st, body, 400, "bad_request", "neither")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/"+a, `{"position":-1}`)
	wantCode(t, st, body, 400, "bad_request", "negative position")
	st, body = r.do("PATCH", "/v1/sessions/s1/queue/nope", `{"position":0}`)
	wantCode(t, st, body, 404, "unknown_item", "unknown item")
	st, body = r.do("PATCH", "/v1/sessions/s2/queue/"+a, `{"position":0}`)
	wantCode(t, st, body, 404, "unknown_session", "unknown session")
}

func TestQueueAPI_DeleteAndResume(t *testing.T) {
	r := newQueueAPI(t)
	a := r.add("a", "")
	b := r.add("b", "")
	st, body := r.do("DELETE", "/v1/sessions/s1/queue/"+b, "")
	if st != 200 || queueTexts(body) != "a" {
		t.Fatalf("delete: %d %v", st, body)
	}
	st, body = r.do("DELETE", "/v1/sessions/s1/queue/"+b, "")
	wantCode(t, st, body, 404, "unknown_item", "delete twice")
	if _, err := r.store.MarkSending("s1", a); err != nil {
		t.Fatal(err)
	}
	st, body = r.do("DELETE", "/v1/sessions/s1/queue/"+a, "")
	wantCode(t, st, body, 409, "item_sending", "delete the sending item of a running queue")

	if err := r.store.Pause("s1", chatqueue.ReasonNoAnswer); err != nil {
		t.Fatal(err)
	}
	r.add("c", "")
	_, body = r.do("GET", "/v1/sessions/s1/queue", "")
	if body["state"] != chatqueue.QueuePaused || body["reason"] != chatqueue.ReasonNoAnswer {
		t.Fatalf("paused queue %v", body)
	}
	if _, ok := body["note"]; ok {
		t.Fatalf("a paused queue shows the waiting line: %v", body)
	}
	before := r.kicks.Load()
	st, body = r.do("POST", "/v1/sessions/s1/queue/resume", "")
	if st != 200 || body["state"] != chatqueue.QueueRunning || body["reason"] != nil {
		t.Fatalf("resume: %d %v", st, body)
	}
	if r.kicks.Load() != before+1 {
		t.Fatalf("resume kicked %d times, want once", r.kicks.Load()-before)
	}
	st, body = r.do("POST", "/v1/sessions/s2/queue/resume", "")
	wantCode(t, st, body, 404, "unknown_session", "resume an unknown session")
}

func TestQueueAPI_SessionDetailAndCapability(t *testing.T) {
	r := newQueueAPI(t)
	_, body := r.do("GET", "/v1/sessions/s1", "")
	if _, ok := body["queue"]; ok {
		t.Fatalf("detail %v has a queue with no items", body)
	}
	r.add("next", "")
	_, body = r.do("GET", "/v1/sessions/s1", "")
	q, _ := body["queue"].(map[string]any)
	if q["state"] != chatqueue.QueueRunning || queueTexts(q) != "next" {
		t.Fatalf("detail queue %v", body["queue"])
	}
	_, body = r.do("GET", "/v1/health", "")
	if caps, _ := body["capabilities"].([]any); !slices.Contains(caps, any(CapQueue)) {
		t.Fatalf("capabilities %v lack %s", caps, CapQueue)
	}
}

func TestQueueAPI_NotSupportedWithoutAStore(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := do(t, srv, "GET", "/v1/sessions/s1/queue", "")
	if resp.StatusCode != 404 || body["code"] != "not_supported" {
		t.Fatalf("queue without a store: %d %v", resp.StatusCode, body)
	}
	_, body = do(t, srv, "GET", "/v1/health", "")
	if caps, _ := body["capabilities"].([]any); slices.Contains(caps, any(CapQueue)) {
		t.Fatalf("capabilities %v offer %s without a store", caps, CapQueue)
	}
	_, body = do(t, srv, "GET", "/v1/sessions/s1", "")
	if _, ok := body["queue"]; ok {
		t.Fatalf("detail %v has a queue", body)
	}
}

// Each change becomes a queue event on the session's log, for clients that
// ask with queue=1. Clients that do not (every client before the queue)
// never see one: they would take an event of no turn for a running turn.
func TestQueueAPI_QueueEvents(t *testing.T) {
	r := newQueueAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.api.QueueEvents(ctx)

	id := r.add("hello", "")
	evs := waitFor(t, r.b, "s1", KindQueue)
	var q QueueStatus
	if err := json.Unmarshal(evs[len(evs)-1].Data, &q); err != nil {
		t.Fatal(err)
	}
	if q.State != chatqueue.QueueRunning || len(q.Items) != 1 || q.Items[0].ID != id || q.Items[0].Text != "hello" || evs[len(evs)-1].Turn != "" {
		t.Fatalf("queue event %+v turn %q", q, evs[len(evs)-1].Turn)
	}

	page := eventsPage(t, r.srv, "/v1/sessions/s1/events?wait=0&queue=1", "")
	if len(page) == 0 || page[len(page)-1].Kind != KindQueue {
		t.Fatalf("page with queue=1: %+v", page)
	}
	if page := eventsPage(t, r.srv, "/v1/sessions/s1/events?wait=0", ""); len(page) != 0 {
		t.Fatalf("page without queue=1: %+v, want no queue events", page)
	}

	// Streams: with queue=1 the next change comes through; without, the
	// first event a client sees is the next turn's.
	after := page[len(page)-1].Seq
	withQueue := r.stream(fmt.Sprintf("/v1/sessions/s1/events?queue=1&after=%d", after))
	without := r.stream(fmt.Sprintf("/v1/sessions/s1/events?after=%d", after))
	if st, body := r.do("DELETE", "/v1/sessions/s1/queue/"+id, ""); st != 200 {
		t.Fatalf("delete: %d %v", st, body)
	}
	got := readSSE(t, withQueue, KindQueue)
	if last := got[len(got)-1]; !strings.Contains(string(last.Data), `"items":[]`) {
		t.Fatalf("streamed %+v, want the emptied queue", got)
	}
	if st, body := r.do("POST", "/v1/sessions/s1/messages", `{"text":"hi"}`); st != 202 {
		t.Fatalf("send: %d %v", st, body)
	}
	if got := readSSE(t, without, KindTurnStarted); len(got) != 1 {
		t.Fatalf("stream without queue=1 carried %v", kindsOf(got))
	}
}

// stream opens an event stream; it closes with the test.
func (r *queueRig) stream(path string) io.Reader {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.srv.URL+path, nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp.Body
}

// The queue routes check a session exists without reading its detail: no
// terminal status read, no repository lookup.
func TestQueueAPI_RoutesNeverReadTheDetail(t *testing.T) {
	r := newQueueAPI(t)
	id := r.add("a", "")
	r.do("GET", "/v1/sessions/s1/queue", "")
	r.do("PATCH", "/v1/sessions/s1/queue/"+id, `{"position":0}`)
	r.do("POST", "/v1/sessions/s1/queue/resume", "")
	r.do("DELETE", "/v1/sessions/s1/queue/"+id, "")
	r.do("POST", "/v1/sessions/s2/queue", `{"text":"a"}`)
	if n := r.details.Load(); n != 0 {
		t.Fatalf("queue routes read the session detail %d times", n)
	}
}

// The owner can clear a queue whose session is gone: the store still holds
// its items.
func TestQueueAPI_DeleteWorksForAGoneSession(t *testing.T) {
	r := newQueueAPI(t)
	it, err := r.store.Add("gone", "left behind", "")
	if err != nil {
		t.Fatal(err)
	}
	st, body := r.do("DELETE", "/v1/sessions/gone/queue/"+it.ID, "")
	if st != 200 || queueTexts(body) != "" {
		t.Fatalf("delete in a gone session: %d %v", st, body)
	}
	st, body = r.do("DELETE", "/v1/sessions/gone/queue/"+it.ID, "")
	wantCode(t, st, body, 404, "unknown_item", "delete it again")
}

// A note change reaches streaming clients as a queue event.
func TestQueueAPI_PublishQueue(t *testing.T) {
	r := newQueueAPI(t)
	r.add("a", "")
	r.api.PublishQueue("s1")
	evs := waitFor(t, r.b, "s1", KindQueue)
	var q QueueStatus
	if err := json.Unmarshal(evs[len(evs)-1].Data, &q); err != nil {
		t.Fatal(err)
	}
	if q.Note != chatqueue.NoteMacInUse || len(q.Items) != 1 {
		t.Fatalf("published %+v, want the queue with its waiting line", q)
	}
}

// A direct send to a session whose queue is active (an item in flight, or
// running with items waiting) answers 409 busy on both message routes, so
// it never joins the dispatcher's turn and a waiting item keeps its place.
// A paused queue with only waiting items lets it through.
func TestQueueAPI_DirectSendBusyWhileQueueActive(t *testing.T) {
	r := newQueueAPI(t)
	term := &fakeTerminal{}
	r.api.TerminalInput, r.api.TerminalEnabled = term, true
	acpStarts := &r.f.sends
	send := func(route string) (int, string) {
		st, body := r.do("POST", "/v1/sessions/s1/"+route, `{"text":"hello"}`)
		code, _ := body["code"].(string)
		return st, code
	}
	routes := []string{"terminal/messages", "messages"}

	first := r.add("first", "k1")
	for _, route := range routes {
		if st, code := send(route); st != http.StatusConflict || code != "busy" {
			t.Fatalf("running with a waiting item, %s: %d %q, want 409 busy", route, st, code)
		}
	}
	if len(term.calls) != 0 || acpStarts.Load() != 0 {
		t.Fatalf("sent while the queue was active: terminal %v, acp %d", term.calls, acpStarts.Load())
	}

	if err := r.store.Pause("s1", "test"); err != nil {
		t.Fatal(err)
	}
	if st, code := send("terminal/messages"); st != http.StatusAccepted {
		t.Fatalf("paused, waiting only: %d %q, want 202", st, code)
	}

	if err := r.store.Resume("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.MarkSending("s1", first); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Pause("s1", "test"); err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if st, code := send(route); st != http.StatusConflict || code != "busy" {
			t.Fatalf("paused with an item in flight, %s: %d %q, want 409 busy", route, st, code)
		}
	}
}
