package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// handoffRig is a Handoff over fakes that record every call in order.
type handoffRig struct {
	mu       sync.Mutex
	calls    []string
	state    string // what State answers before Open ran
	turn     bool   // State answers open_in_terminal once Open ran
	opened   bool
	held     bool   // Hold succeeded
	heldAs   string // if set, what State answers once Hold succeeded (before Open)
	targetE  error
	holdE    error
	dropE    error
	openE    error
	released int
	openCtx  context.Context
	open     func() // runs inside Open, before it returns
}

func (r *handoffRig) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *handoffRig) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// set changes the fakes' answers between requests.
func (r *handoffRig) set(fn func(*handoffRig)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r)
}

// err reads one of the fakes' errors under the lock.
func (r *handoffRig) err(e *error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *e
}

func (r *handoffRig) releases() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.released
}

func (r *handoffRig) handoff() *Handoff {
	return &Handoff{
		State: func(context.Context, string) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.opened && r.turn {
				return StateOpenInTerminal, nil
			}
			if r.held && !r.opened && r.heldAs != "" {
				return r.heldAs, nil
			}
			return r.state, nil
		},
		Target: func(context.Context, string) (string, []string, string, error) {
			r.record("target")
			return "Fix the cache", []string{"/bin/claude", "--resume", "s1"}, "/w/app", r.err(&r.targetE)
		},
		Hold: func(string) (func(), error) {
			r.record("hold")
			if err := r.err(&r.holdE); err != nil {
				return nil, err
			}
			r.mu.Lock()
			r.held = true
			r.mu.Unlock()
			return func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.released++
			}, nil
		},
		Drop: func(string) error {
			r.record("drop")
			return r.err(&r.dropE)
		},
		Open: func(ctx context.Context, _ string, _ []string, _ string) error {
			r.record("open")
			r.mu.Lock()
			r.openCtx = ctx
			r.mu.Unlock()
			if r.open != nil {
				r.open()
			}
			r.mu.Lock()
			r.opened = true
			r.mu.Unlock()
			return r.err(&r.openE)
		},
		Wait: time.Second,
		Poll: 5 * time.Millisecond,
	}
}

func TestHandoff_OrderAndSuccess(t *testing.T) {
	r := &handoffRig{state: StateIdle, turn: true}
	if err := r.handoff().Run(context.Background(), "s1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"target", "hold", "drop", "open"}; !slices.Equal(r.got(), want) {
		t.Fatalf("calls %v, want %v", r.got(), want)
	}
	if r.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.releases())
	}
	if _, ok := r.openCtx.Deadline(); !ok {
		t.Fatal("Open got a context without a deadline")
	}
}

func TestHandoff_AlreadyInTerminal(t *testing.T) {
	r := &handoffRig{state: StateOpenInTerminal}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, acprun.ErrOpenInTerminal) {
		t.Fatalf("err %v, want ErrOpenInTerminal", err)
	}
	if len(r.got()) != 0 {
		t.Fatalf("calls %v, want none", r.got())
	}
}

func TestHandoff_BusyRefused(t *testing.T) {
	r := &handoffRig{state: StateIdle, holdE: acprun.ErrBusy}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, acprun.ErrBusy) {
		t.Fatalf("err %v, want ErrBusy", err)
	}
	if want := []string{"target", "hold"}; !slices.Equal(r.got(), want) {
		t.Fatalf("calls %v, want %v", r.got(), want)
	}
}

func TestHandoff_DropBusyReleasesHold(t *testing.T) {
	r := &handoffRig{state: StateIdle, dropE: acprun.ErrBusy}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, acprun.ErrBusy) {
		t.Fatalf("err %v, want ErrBusy", err)
	}
	if want := []string{"target", "hold", "drop"}; !slices.Equal(r.got(), want) || r.releases() != 1 {
		t.Fatalf("calls %v released %d, want %v and 1", r.got(), r.releases(), want)
	}
}

func TestHandoff_NotStarted(t *testing.T) {
	r := &handoffRig{state: StateIdle, targetE: ErrNotStarted}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, ErrNotStarted) {
		t.Fatalf("err %v, want ErrNotStarted", err)
	}
	if want := []string{"target"}; !slices.Equal(r.got(), want) {
		t.Fatalf("calls %v, want %v", r.got(), want)
	}
}

func TestHandoff_UnknownSession(t *testing.T) {
	r := &handoffRig{}
	h := r.handoff()
	h.State = func(context.Context, string) (string, error) { return "", acprun.ErrUnknownSession }
	if err := h.Run(context.Background(), "nope"); !errors.Is(err, acprun.ErrUnknownSession) {
		t.Fatalf("err %v, want ErrUnknownSession", err)
	}
	if len(r.got()) != 0 {
		t.Fatalf("calls %v, want none", r.got())
	}
}

func TestHandoff_TimeoutReleasesHold(t *testing.T) {
	r := &handoffRig{state: StateIdle} // never turns open_in_terminal
	h := r.handoff()
	h.Wait = 50 * time.Millisecond
	start := time.Now()
	err := h.Run(context.Background(), "s1")
	if !errors.Is(err, ErrHandoffTimeout) {
		t.Fatalf("err %v, want ErrHandoffTimeout", err)
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("Run took %v, want about the 50ms wait", d)
	}
	if r.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.releases())
	}
	// The run stays dropped: nothing after Open undoes it.
	if want := []string{"target", "hold", "drop", "open"}; !slices.Equal(r.got(), want) {
		t.Fatalf("calls %v, want %v", r.got(), want)
	}
}

func TestHandoff_NotAuthorizedReleasesHold(t *testing.T) {
	r := &handoffRig{state: StateIdle, turn: true, openE: ErrNotAuthorized}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("err %v, want ErrNotAuthorized", err)
	}
	if r.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.releases())
	}
}

func TestHandoff_OpenErrorIsNotWaitedOn(t *testing.T) {
	r := &handoffRig{state: StateIdle, openE: errors.New("osascript failed")}
	h := r.handoff()
	h.Wait = time.Minute
	err := h.Run(context.Background(), "s1")
	if err == nil || errors.Is(err, ErrHandoffTimeout) || !strings.Contains(err.Error(), "osascript failed") {
		t.Fatalf("err %v, want the open error", err)
	}
	if r.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.releases())
	}
}

// A client that goes away once the session is held does not stop the
// handoff: the tab still opens and the hold lasts until the terminal holds
// the session.
func TestHandoff_ClientGoneDuringOpenFinishes(t *testing.T) {
	r := &handoffRig{state: StateIdle, turn: true}
	h := r.handoff()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var openErr error
	var releasedInOpen int
	r.open = func() {
		cancel()
		r.mu.Lock()
		openErr = r.openCtx.Err()
		r.mu.Unlock()
		releasedInOpen = r.releases()
	}
	if err := h.Run(ctx, "s1"); err != nil {
		t.Fatalf("Run after the client left: %v, want nil", err)
	}
	if openErr != nil {
		t.Fatalf("Open's context ended with the client: %v", openErr)
	}
	if releasedInOpen != 0 || r.releases() != 1 {
		t.Fatalf("released %d times during Open and %d after, want 0 and 1", releasedInOpen, r.releases())
	}
}

// After the client left, the wait is still bounded by Wait.
func TestHandoff_ClientGoneStillTimesOut(t *testing.T) {
	r := &handoffRig{state: StateIdle}
	h := r.handoff()
	h.Wait = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.open = cancel
	if err := h.Run(ctx, "s1"); !errors.Is(err, ErrHandoffTimeout) {
		t.Fatalf("err %v, want ErrHandoffTimeout", err)
	}
	if r.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.releases())
	}
}

// A terminal that took the session between the first look and the hold
// wins: nothing is dropped or opened.
func TestHandoff_InTerminalAfterHold(t *testing.T) {
	r := &handoffRig{state: StateIdle, heldAs: StateOpenInTerminal}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, acprun.ErrOpenInTerminal) {
		t.Fatalf("err %v, want ErrOpenInTerminal", err)
	}
	if want := []string{"target", "hold"}; !slices.Equal(r.got(), want) || r.releases() != 1 {
		t.Fatalf("calls %v released %d, want %v and 1", r.got(), r.releases(), want)
	}
}

// An open that runs out of time is a timeout clients have words for.
func TestHandoff_OpenDeadlineIsTimeout(t *testing.T) {
	r := &handoffRig{state: StateIdle, openE: fmt.Errorf("osascript: %w", context.DeadlineExceeded)}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, ErrHandoffTimeout) {
		t.Fatalf("err %v, want ErrHandoffTimeout", err)
	}
	if r.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.releases())
	}
}

// A session running a turn is refused before anything else is asked.
func TestHandoff_BusyStateRefusedFirst(t *testing.T) {
	r := &handoffRig{state: StateBusy}
	err := r.handoff().Run(context.Background(), "s1")
	if !errors.Is(err, acprun.ErrBusy) {
		t.Fatalf("err %v, want ErrBusy", err)
	}
	if len(r.got()) != 0 {
		t.Fatalf("calls %v, want none", r.got())
	}
}

func TestHandoff_Defaults(t *testing.T) {
	r := &handoffRig{state: StateIdle, turn: true}
	h := r.handoff()
	h.Wait, h.Poll = 0, 0
	if err := h.Run(context.Background(), "s1"); err != nil {
		t.Fatalf("Run with default Wait and Poll: %v", err)
	}
}

func TestHandoff_ConcurrentSecondIsBusy(t *testing.T) {
	b := NewBroker(BrokerDeps{}, BrokerOptions{})
	r := &handoffRig{state: StateIdle, turn: true}
	inOpen, secondDone := make(chan struct{}), make(chan struct{})
	r.open = func() {
		close(inOpen)
		<-secondDone
	}
	h := r.handoff()
	h.Hold = b.Hold
	first := make(chan error, 1)
	go func() { first <- h.Run(context.Background(), "s1") }()
	<-inOpen
	second := h.Run(context.Background(), "s1")
	close(secondDone)
	if !errors.Is(second, acprun.ErrBusy) {
		t.Fatalf("second Run: %v, want ErrBusy", second)
	}
	if err := <-first; err != nil {
		t.Fatalf("first Run: %v", err)
	}
	opens := 0
	for _, c := range r.got() {
		if c == "open" {
			opens++
		}
	}
	if opens != 1 {
		t.Fatalf("Open called %d times, want 1", opens)
	}
	if b.Busy("s1") {
		t.Fatal("the session is still held after the handoff")
	}
}

func TestHandoff_QueuedItemWaits(t *testing.T) {
	b := NewBroker(BrokerDeps{}, BrokerOptions{})
	q := QueueACP{Broker: b}
	r := &handoffRig{state: StateIdle, turn: true}
	var busyDuringOpen bool
	r.open = func() { busyDuringOpen = q.Busy("s1") }
	h := r.handoff()
	h.Hold = b.Hold
	if err := h.Run(context.Background(), "s1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !busyDuringOpen {
		t.Fatal("the queue saw the session free while the tab opened")
	}
	if q.Busy("s1") {
		t.Fatal("the queue still sees the session busy after the handoff")
	}
}

// openOnMacFixture is api/testdata/clients/terminal/open-on-mac.json, the
// copy and codes both clients read.
type openOnMacFixture struct {
	Copy    map[string]string `json:"copy"`
	Success struct {
		Status int    `json:"status"`
		State  string `json:"state"`
	} `json:"success"`
	Codes map[string]int `json:"codes"`
}

func loadOpenOnMacFixture(t *testing.T) openOnMacFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "testdata", "clients", "terminal", "open-on-mac.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f openOnMacFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("open-on-mac.json: %v", err)
	}
	return f
}

// openOnMacServer serves the API with h as its Handoff (nil: none).
func openOnMacServer(t *testing.T, h *Handoff) *httptest.Server {
	t.Helper()
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Handoff: h}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(srv.Close)
	return srv
}

// Every code in the shared fixture is what the route answers, with its
// status, and every refusal but unknown_session has its copy.
func TestOpenOnMac_CodesMatchFixture(t *testing.T) {
	f := loadOpenOnMacFixture(t)
	cases := map[string]func(*handoffRig){
		"busy":             func(r *handoffRig) { r.holdE = acprun.ErrBusy },
		"open_in_terminal": func(r *handoffRig) { r.state = StateOpenInTerminal },
		"in_use":           func(r *handoffRig) { r.dropE = acprun.ErrInUseElsewhere },
		"not_started":      func(r *handoffRig) { r.targetE = ErrNotStarted },
		"no_terminal":      func(r *handoffRig) { r.openE = ErrNoTerminal },
		"not_authorized":   func(r *handoffRig) { r.openE = ErrNotAuthorized },
		"timeout":          func(r *handoffRig) { r.turn = false },
		"unknown_session":  func(r *handoffRig) { r.targetE = acprun.ErrUnknownSession },
	}
	if len(cases) != len(f.Codes) {
		t.Fatalf("fixture codes %v, test cases %d", f.Codes, len(cases))
	}
	for code, status := range f.Codes {
		set, ok := cases[code]
		if !ok {
			t.Errorf("fixture code %q has no case", code)
			continue
		}
		r := &handoffRig{state: StateIdle, turn: true}
		set(r)
		h := r.handoff()
		h.Wait = 30 * time.Millisecond
		resp, body := do(t, openOnMacServer(t, h), "POST", "/v1/sessions/s1/open-on-mac", "")
		if resp.StatusCode != status || body["code"] != code {
			t.Errorf("%s: %d %v, want %d", code, resp.StatusCode, body, status)
		}
		if code != "unknown_session" && f.Copy[code] == "" {
			t.Errorf("%s has no copy", code)
		}
	}
	r := &handoffRig{state: StateIdle, turn: true}
	resp, body := do(t, openOnMacServer(t, r.handoff()), "POST", "/v1/sessions/s1/open-on-mac", "")
	if resp.StatusCode != f.Success.Status || body["state"] != f.Success.State {
		t.Fatalf("success: %d %v, want %d %s", resp.StatusCode, body, f.Success.Status, f.Success.State)
	}
	for _, key := range []string{"action", "progress", "done"} {
		if f.Copy[key] == "" {
			t.Errorf("copy %q missing", key)
		}
	}
}

func TestOpenOnMac_NoHandoffIsNoTerminal(t *testing.T) {
	resp, body := do(t, openOnMacServer(t, nil), "POST", "/v1/sessions/s1/open-on-mac", "")
	if resp.StatusCode != 503 || body["code"] != "no_terminal" {
		t.Fatalf("%d %v, want 503 no_terminal", resp.StatusCode, body)
	}
}

func TestOpenOnMac_Health(t *testing.T) {
	_, body := do(t, openOnMacServer(t, nil), "GET", "/v1/health", "")
	if v, ok := body["openOnMac"].(bool); !ok || v {
		t.Fatalf("openOnMac %v without a Handoff, want false", body["openOnMac"])
	}
	_, body = do(t, openOnMacServer(t, (&handoffRig{}).handoff()), "GET", "/v1/health", "")
	if body["openOnMac"] != true {
		t.Fatalf("openOnMac %v with a Handoff, want true", body["openOnMac"])
	}
}

func TestOpenOnMac_ContractDeclaresRoute(t *testing.T) {
	doc := loadContract(t)
	item := doc.Paths.Find("/v1/sessions/{id}/open-on-mac")
	if item == nil || item.Post == nil {
		t.Fatal("the contract has no POST /v1/sessions/{id}/open-on-mac")
	}
	for _, status := range []int{200, 403, 404, 409, 503, 504} {
		if item.Post.Responses.Status(status) == nil {
			t.Errorf("the contract lists no %d for open-on-mac", status)
		}
	}
	if doc.Components.Schemas["Health"].Value.Properties["openOnMac"] == nil {
		t.Error("the contract's Health has no openOnMac")
	}
}

// A phone that drops the request while the tab opens still gets the real
// outcome in the audit log, not a failure.
func TestOpenOnMac_ClientGoneAuditsOutcome(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	tok := r.pair("iphone")
	inOpen, proceed := make(chan struct{}), make(chan struct{})
	r.handoff.set(func(h *handoffRig) {
		h.turn = false // the terminal takes the session a moment after the tab opens
		h.open = func() {
			close(inOpen)
			<-proceed
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(r.req("POST", "/v1/sessions/s1/open-on-mac", "", tok).WithContext(ctx))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-inOpen
	cancel()
	<-done
	close(proceed)
	time.Sleep(50 * time.Millisecond)
	r.handoff.set(func(h *handoffRig) { h.turn = true })
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(r.audit.String(), `"open_on_mac"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no open_on_mac audit line:\n%s", r.audit.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, line := range strings.Split(strings.TrimSpace(r.audit.String()), "\n") {
		var e devicelogin.AuditEntry
		_ = json.Unmarshal([]byte(line), &e)
		if e.Action == devicelogin.ActionOpenOnMac && e.Result != "ok" {
			t.Fatalf("audit result %q after the client left, want ok", e.Result)
		}
	}
	if r.handoff.releases() != 1 {
		t.Fatalf("released %d times, want 1", r.handoff.releases())
	}
}
