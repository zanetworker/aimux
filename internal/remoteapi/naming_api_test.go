package remoteapi

import (
	"bytes"
	"context"
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
	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/history"
)

// privateWords stand for session text: they must never reach a log, an
// audit line or an error body.
const privateWords = "the private plan for the merger"

// fakeNaming records what the API asks of the namer.
type fakeNaming struct {
	mu     sync.Mutex
	wants  [][]string
	asked  []string // UpdateSummary session ids
	result history.SummaryResult
	err    error
	status history.NamingStatus
}

func (f *fakeNaming) Want(ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wants = append(f.wants, append([]string(nil), ids...))
}

func (f *fakeNaming) UpdateSummary(_ context.Context, s history.Session) (history.SummaryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, s.ID)
	if f.err == nil && s.FilePath != "" {
		_ = history.UpdateMeta(s.FilePath, func(m *history.Meta) {
			if slices.Contains(f.result.Changed, "title") {
				m.Title, m.TitleBy = f.result.Title, "model"
			}
			if slices.Contains(f.result.Changed, "description") {
				m.Description, m.DescriptionBy = f.result.Description, "model"
				m.DescribedAt = time.Now().UTC().Format(time.RFC3339)
			}
		})
	}
	return f.result, f.err
}

func (f *fakeNaming) Status() history.NamingStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

// askedAbout reports whether UpdateSummary ran for id.
func (f *fakeNaming) askedAbout(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.asked, id)
}

// set changes the fake's answers between requests.
func (f *fakeNaming) set(fn func(*fakeNaming)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeNaming) allWants() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.wants...)
}

// metaRig is an API over sessions with real files, so PATCH writes real
// metadata.
type metaRig struct {
	t      *testing.T
	dir    string
	base   map[string]history.Session
	naming *fakeNaming
	api    *API
	srv    *httptest.Server
	log    *bytes.Buffer
}

func newMetaRig(t *testing.T) *metaRig { return newMetaRigWith(t, nil) }

// newMetaRigWith uses naming instead of the fake when it is not nil.
func newMetaRigWith(t *testing.T, naming Naming) *metaRig {
	t.Helper()
	r := &metaRig{t: t, dir: t.TempDir(), base: map[string]history.Session{}, naming: &fakeNaming{}, log: &bytes.Buffer{}}
	if naming == nil {
		naming = r.naming
	}
	now := time.Now()
	for _, s := range []history.Session{
		{ID: "s1", Provider: "claude", AITitle: "Claude's name", FirstPrompt: "c513a7b6", LastActive: now},
		{ID: "s2", Provider: "claude", FirstPrompt: "Fix the login", LastActive: now, AwaySummary: "Claude's recap", AwaySummaryAt: now.Add(-time.Hour)},
	} {
		s.FilePath = filepath.Join(r.dir, s.ID+".jsonl")
		if err := os.WriteFile(s.FilePath, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.base[s.ID] = s
	}
	r.api = &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Log: r.log,
		Sessions: func(context.Context, int, bool) ([]Session, error) { return nil, nil },
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			if _, ok := r.base[id]; !ok {
				return SessionDetail{}, acprun.ErrUnknownSession
			}
			return SessionDetail{Session: Session{ID: id, State: StateIdle}, Turns: []Turn{}}, nil
		},
		Record: r.record, Naming: naming,
	}
	r.srv = httptest.NewServer(r.api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(r.srv.Close)
	return r
}

// record is what Discover gives: the session with its metadata read fresh.
func (r *metaRig) record(_ context.Context, id string) (history.Session, error) {
	s, ok := r.base[id]
	if !ok {
		return history.Session{}, fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
	}
	m := history.LoadMeta(s.FilePath)
	s.Title, s.Description, s.DescriptionBy, s.DescribedAt, s.Starred = m.Title, m.Description, m.DescriptionBy, m.DescribedAt, m.Starred
	return s, nil
}

func (r *metaRig) meta(id string) history.Meta { return history.LoadMeta(r.base[id].FilePath) }

func (r *metaRig) patch(id, body string) (int, map[string]any) {
	r.t.Helper()
	resp, out := do(r.t, r.srv, "PATCH", "/v1/sessions/"+id+"/meta", body)
	return resp.StatusCode, out
}

func TestAPI_PatchMeta_WritesOwnerValuesAndPin(t *testing.T) {
	r := newMetaRig(t)
	st, body := r.patch("s1", `{"title":"Release checklist","description":"Prepares the 2.0 release.","pinned":true}`)
	if st != 200 || body["title"] != "Release checklist" || body["description"] != "Prepares the 2.0 release." || body["pinned"] != true {
		t.Fatalf("patch: %d %v", st, body)
	}
	m := r.meta("s1")
	if m.Title != "Release checklist" || m.TitleBy != "owner" || m.Description != "Prepares the 2.0 release." || m.DescriptionBy != "owner" || !m.Starred {
		t.Fatalf("meta %+v, want owner values and the star", m)
	}
	// pinned alone leaves the owner's text as it is
	st, body = r.patch("s1", `{"pinned":false}`)
	if st != 200 || body["pinned"] != false || body["title"] != "Release checklist" {
		t.Fatalf("unpin: %d %v", st, body)
	}
	if m := r.meta("s1"); m.Starred || m.TitleBy != "owner" || m.DescriptionBy != "owner" {
		t.Fatalf("meta after unpin %+v", m)
	}
}

func TestAPI_PatchMeta_EmptyClearsOnlyOwnerValues(t *testing.T) {
	r := newMetaRig(t)
	if st, body := r.patch("s2", `{"title":"Mine","description":"My words."}`); st != 200 {
		t.Fatalf("set: %d %v", st, body)
	}
	st, body := r.patch("s2", `{"title":"","description":""}`)
	if st != 200 || body["title"] != "Fix the login" || body["description"] != "Claude's recap" {
		t.Fatalf("clear: %d %v, want the first message and the away summary back", st, body)
	}
	if m := r.meta("s2"); m.Title != "" || m.TitleBy != "" || m.Description != "" || m.DescriptionBy != "" {
		t.Fatalf("meta after clear %+v", m)
	}
	// a model value is not the owner's: an empty string leaves it
	if err := history.UpdateMeta(r.base["s1"].FilePath, func(m *history.Meta) {
		m.Title, m.TitleBy, m.Description, m.DescriptionBy = "Model title", "model", "Model words.", "model"
	}); err != nil {
		t.Fatal(err)
	}
	st, body = r.patch("s1", `{"title":"","description":""}`)
	if st != 200 || body["title"] != "Model title" || body["description"] != "Model words." {
		t.Fatalf("clear over model values: %d %v", st, body)
	}
	if _, has := body["pinned"]; !has {
		t.Fatalf("answer %v lacks pinned", body)
	}
}

func TestAPI_PatchMeta_Validation(t *testing.T) {
	r := newMetaRig(t)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{}`, 400},
		{`not json`, 400},
		{`{"title":"a","color":"red"}`, 400},
		{`{"title":"` + strings.Repeat("é", 81) + `"}`, 400},
		{`{"title":"` + strings.Repeat("é", 80) + `"}`, 200},
		{`{"title":"a\nb"}`, 400},
		{`{"description":"` + strings.Repeat("ü", 301) + `"}`, 400},
		{`{"description":"` + strings.Repeat("ü", 300) + `"}`, 200},
		{`{"description":"tab\there"}`, 400},
		{`{"title":"   "}`, 200}, // spaces only: clears, like ""
	} {
		if st, body := r.patch("s1", tc.body); st != tc.want || (st == 400 && body["code"] != "bad_request") {
			t.Errorf("%.40s: %d %v, want %d", tc.body, st, body, tc.want)
		}
	}
	if st, body := r.patch("nope", `{"pinned":true}`); st != 404 || body["code"] != "unknown_session" {
		t.Errorf("unknown session: %d %v", st, body)
	}
}

func TestAPI_PatchMeta_NotSupportedWithoutRecords(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	if resp, body := do(t, srv, "PATCH", "/v1/sessions/s1/meta", `{"pinned":true}`); resp.StatusCode != 404 || body["code"] != "not_supported" {
		t.Fatalf("patch without records: %d %v", resp.StatusCode, body)
	}
	if resp, body := do(t, srv, "POST", "/v1/sessions/s1/describe", ""); resp.StatusCode != 404 || body["code"] != "not_supported" {
		t.Fatalf("describe without a namer: %d %v", resp.StatusCode, body)
	}
}

func TestAPI_Describe_AnswersWhatChanged(t *testing.T) {
	r := newMetaRig(t)
	r.naming.set(func(f *fakeNaming) {
		f.result = history.SummaryResult{Title: "Login fix", Description: "Fixes the login redirect.", Changed: []string{"title", "description"}}
	})
	resp, body := do(t, r.srv, "POST", "/v1/sessions/s2/describe", "")
	if resp.StatusCode != 200 || body["title"] != "Login fix" || body["description"] != "Fixes the login redirect." {
		t.Fatalf("describe: %d %v", resp.StatusCode, body)
	}
	if fmt.Sprint(body["changed"]) != "[title description]" || fmt.Sprint(body["kept"]) != "[]" {
		t.Fatalf("changed %v kept %v", body["changed"], body["kept"])
	}
	// an owner title is kept, and the answer shows it
	if st, _ := r.patch("s1", `{"title":"Mine"}`); st != 200 {
		t.Fatal(st)
	}
	r.naming.set(func(f *fakeNaming) {
		f.result = history.SummaryResult{Description: "New words.", Changed: []string{"description"}, Kept: []string{"title"}}
	})
	resp, body = do(t, r.srv, "POST", "/v1/sessions/s1/describe", "")
	if resp.StatusCode != 200 || body["title"] != "Mine" || body["description"] != "New words." || fmt.Sprint(body["kept"]) != "[title]" {
		t.Fatalf("describe with an owner title: %d %v", resp.StatusCode, body)
	}
	if resp, body := do(t, r.srv, "POST", "/v1/sessions/nope/describe", ""); resp.StatusCode != 404 || body["code"] != "unknown_session" {
		t.Fatalf("describe unknown: %d %v", resp.StatusCode, body)
	}
	if r.naming.askedAbout("nope") {
		t.Fatal("the namer was asked about an unknown session")
	}
}

func TestAPI_Describe_ErrorsAreFixedMessages(t *testing.T) {
	r := newMetaRig(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{history.ErrOwnerSet, 409, "owner_set"},
		{history.ErrBusy, 409, "describe_running"},
		{history.ErrNoContent, 409, "no_content"},
		{history.ErrTimeout, 504, "naming_timeout"},
		{history.ErrUnauthorized, 502, "naming_auth"},
		{history.ErrModelNotFound, 502, "naming_model"},
		{history.ErrBadReply, 502, "naming_unreadable"},
		{history.ErrModelCall, 502, "naming_failed"},
		{history.ErrNoBackend, 503, "naming_unavailable"},
		{errors.New("save naming for session s1: permission denied"), 500, "internal"},
	} {
		err := fmt.Errorf("update summary for session s1: %w", tc.err)
		if tc.code != "internal" { // a mapped error is never echoed, whatever its text
			err = fmt.Errorf("%w (%s)", err, privateWords)
		}
		r.naming.set(func(f *fakeNaming) { f.err = err })
		resp, body := do(t, r.srv, "POST", "/v1/sessions/s1/describe", "")
		if resp.StatusCode != tc.status || body["code"] != tc.code {
			t.Errorf("%v: %d %v, want %d %s", tc.err, resp.StatusCode, body, tc.status, tc.code)
		}
		if msg, _ := body["error"].(string); msg == "" || strings.Contains(msg, privateWords) || strings.Contains(msg, "s1") {
			t.Errorf("%v: message %q, want a fixed one", tc.err, msg)
		}
	}
	if log := r.log.String(); strings.Count(log, "\n") != 1 || !strings.Contains(log, "permission denied") {
		t.Errorf("log %q, want one line, for the save failure only", log)
	}
}

// One "Update summary" per session at a time, with the real namer: the
// second answers 409 describe_running while the first waits for the model.
func TestAPI_Describe_OneInFlightPerSession(t *testing.T) {
	backend := &blockingNamingBackend{entered: make(chan struct{}), release: make(chan struct{})}
	r := newMetaRigWith(t, history.NewNamer(history.NamerConfig{Lookup: func(string) (history.Session, bool) { return history.Session{}, false },
		Primary: backend, RecordUsage: func(cost.Usage) error { return nil }, Logf: func(string, ...any) {}}))
	transcript := `{"type":"user","message":{"role":"user","content":"Fix the login redirect"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}` + "\n"
	if err := os.WriteFile(r.base["s2"].FilePath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	first := make(chan int, 1)
	go func() {
		resp, _ := do(t, r.srv, "POST", "/v1/sessions/s2/describe", "")
		first <- resp.StatusCode
	}()
	<-backend.entered
	resp, body := do(t, r.srv, "POST", "/v1/sessions/s2/describe", "")
	if resp.StatusCode != 409 || body["code"] != "describe_running" {
		t.Fatalf("second describe: %d %v", resp.StatusCode, body)
	}
	close(backend.release)
	if st := <-first; st != 200 {
		t.Fatalf("first describe: %d", st)
	}
	if m := r.meta("s2"); m.Title != "Login redirect fix" || m.TitleBy != "model" {
		t.Fatalf("meta %+v, want the model's title", m)
	}
}

type blockingNamingBackend struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *blockingNamingBackend) Complete(ctx context.Context, _ history.NamingRequest) (history.NamingReply, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return history.NamingReply{Text: `{"sessions":[{"id":"1","title":"Login redirect fix","description":"Fixes the login redirect."}]}`}, nil
}

func TestAPI_Health_DescribeAndNaming(t *testing.T) {
	r := newMetaRig(t)
	r.naming.set(func(f *fakeNaming) {
		f.status = history.NamingStatus{Today: 4, Limit: 20, OnRequest: 2, Failing: true, Reason: "timed out", Paused: true}
	})
	_, body := do(t, r.srv, "GET", "/v1/health", "")
	if caps, _ := body["capabilities"].([]any); !slices.Contains(caps, any(CapDescribe)) {
		t.Fatalf("capabilities %v lack describe", body["capabilities"])
	}
	n, _ := body["naming"].(map[string]any)
	if n["today"] != 4.0 || n["limit"] != 20.0 || n["failing"] != true || n["reason"] != "timed out" || n["paused"] != true {
		t.Fatalf("naming %v", body["naming"])
	}
	r.naming.set(func(f *fakeNaming) { f.status = history.NamingStatus{Limit: 20} })
	_, body = do(t, r.srv, "GET", "/v1/health", "")
	if n, _ := body["naming"].(map[string]any); n["failing"] != false || n["reason"] != nil || n["paused"] != nil {
		t.Fatalf("healthy naming %v, want failing false and no reason or paused", body["naming"])
	}
	srv, _ := newTestAPI(t, &fakeBackend{})
	_, body = do(t, srv, "GET", "/v1/health", "")
	if caps, _ := body["capabilities"].([]any); slices.Contains(caps, any(CapDescribe)) || body["naming"] != nil {
		t.Fatalf("without a namer: %v", body)
	}
}

// wantRig lists sessions to a client with automatic naming on.
func wantRig(t *testing.T, list []Session, last func() (string, time.Time, bool)) (*httptest.Server, *fakeNaming, *API) {
	return wantRigAuto(t, list, last, true)
}

func wantRigAuto(t *testing.T, list []Session, last func() (string, time.Time, bool), auto bool) (*httptest.Server, *fakeNaming, *API) {
	t.Helper()
	naming := &fakeNaming{}
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Naming: naming, AutoName: auto, LastOnMac: last,
		Sessions: func(context.Context, int, bool) ([]Session, error) { return list, nil },
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			for _, s := range list {
				if s.ID == id {
					return SessionDetail{Session: s, Turns: []Turn{}}, nil
				}
			}
			return SessionDetail{}, acprun.ErrUnknownSession
		},
	}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(srv.Close)
	return srv, naming, api
}

func TestAPI_ListWantsTheSessionsClientsShow(t *testing.T) {
	now := time.Now()
	old := now.Add(-72 * time.Hour)
	list := []Session{
		{ID: "today-1", LastActive: now, State: StateIdle},
		{ID: "today-2", LastActive: now, State: StateIdle},
		{ID: "old-pinned", LastActive: old, Pinned: true, State: StateIdle},
		{ID: "old", LastActive: old, State: StateIdle},
		{ID: "old-mac", LastActive: old, State: StateIdle},
	}
	srv, naming, _ := wantRig(t, list, func() (string, time.Time, bool) { return "old-mac", now, true })
	if resp, _ := do(t, srv, "GET", "/v1/sessions", ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	got := waitWants(t, naming, 1)
	if len(got) != 1 || fmt.Sprint(got[0]) != "[old-mac old-pinned today-1 today-2]" {
		t.Fatalf("wants %v, want Last on your Mac, pinned, then the recent ones", got)
	}
	// an opened chat
	do(t, srv, "GET", "/v1/sessions/old", "")
	if got := waitWants(t, naming, 2); len(got) != 2 || fmt.Sprint(got[1]) != "[old]" {
		t.Fatalf("wants after opening old: %v", got)
	}
	do(t, srv, "GET", "/v1/sessions/nope", "")
	do(t, srv, "GET", "/v1/sessions/old", "") // a marker: once it arrives, nope would have too
	if got := waitWants(t, naming, 3); len(got) != 3 || fmt.Sprint(got[2]) != "[old]" {
		t.Fatalf("an unknown session was wanted: %v", got)
	}
}

func TestAPI_ListWantsAtMost30(t *testing.T) {
	now := time.Now()
	var list []Session
	for i := range 40 {
		list = append(list, Session{ID: fmt.Sprintf("t%02d", i), LastActive: now, State: StateIdle})
	}
	srv, naming, _ := wantRig(t, list, nil)
	do(t, srv, "GET", "/v1/sessions", "")
	got := waitWants(t, naming, 1)
	if len(got) != 1 || len(got[0]) != 30 || got[0][0] != "t00" || got[0][29] != "t29" {
		t.Fatalf("wants %v, want the first 30", got)
	}
}

func TestAPI_NoWantsWithAutomaticNamingOff(t *testing.T) {
	now := time.Now()
	srv, naming, _ := wantRigAuto(t, []Session{{ID: "a", LastActive: now, State: StateIdle}}, nil, false)
	do(t, srv, "GET", "/v1/sessions", "")
	do(t, srv, "GET", "/v1/sessions/a", "")
	time.Sleep(50 * time.Millisecond) // a Want would run off the request
	if got := naming.allWants(); len(got) != 0 {
		t.Fatalf("wants %v with sessions.auto_title off", got)
	}
}

func TestAPI_ListCarriesLastOnMac(t *testing.T) {
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	list := []Session{{ID: "a", LastActive: at, State: StateIdle}}
	var mu sync.Mutex
	id, ok := "a", true
	srv, _, _ := wantRig(t, list, func() (string, time.Time, bool) { mu.Lock(); defer mu.Unlock(); return id, at, ok })
	_, body := do(t, srv, "GET", "/v1/sessions", "")
	lm, _ := body["lastOnMac"].(map[string]any)
	if lm["id"] != "a" || lm["at"] != "2026-10-30T08:00:00Z" {
		t.Fatalf("lastOnMac %v", body["lastOnMac"])
	}
	// a session that is gone is left out
	mu.Lock()
	id = "gone"
	mu.Unlock()
	if _, body := do(t, srv, "GET", "/v1/sessions", ""); body["lastOnMac"] != nil {
		t.Fatalf("lastOnMac for a gone session: %v", body["lastOnMac"])
	}
	// unknown
	mu.Lock()
	ok = false
	mu.Unlock()
	if _, body := do(t, srv, "GET", "/v1/sessions", ""); body["lastOnMac"] != nil {
		t.Fatalf("lastOnMac when unknown: %v", body["lastOnMac"])
	}
}

// ctxNamingBackend waits for release, or gives up when its context ends.
type ctxNamingBackend struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *ctxNamingBackend) Complete(ctx context.Context, _ history.NamingRequest) (history.NamingReply, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return history.NamingReply{Text: `{"sessions":[{"id":"1","title":"Login redirect fix","description":"Fixes the login redirect."}]}`}, nil
	case <-ctx.Done():
		return history.NamingReply{}, ctx.Err()
	}
}

// A phone that leaves while "Update summary" runs: the summary is still
// written (it is paid for), and nothing counts as a naming failure.
func TestAPI_Describe_FinishesWhenTheClientLeaves(t *testing.T) {
	backend := &ctxNamingBackend{entered: make(chan struct{}), release: make(chan struct{})}
	namer := history.NewNamer(history.NamerConfig{Lookup: func(string) (history.Session, bool) { return history.Session{}, false },
		Primary: backend, RecordUsage: func(cost.Usage) error { return nil }, Logf: func(string, ...any) {}})
	r := newMetaRigWith(t, namer)
	transcript := `{"type":"user","message":{"role":"user","content":"Fix the login redirect"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}` + "\n"
	if err := os.WriteFile(r.base["s2"].FilePath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", r.srv.URL+"/v1/sessions/s2/describe", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-backend.entered
	cancel()
	<-gone
	time.Sleep(50 * time.Millisecond) // let the server see the closed connection
	close(backend.release)
	deadline := time.Now().Add(3 * time.Second)
	for r.meta("s2").Title != "Login redirect fix" {
		if time.Now().After(deadline) {
			t.Fatalf("meta %+v, want the summary written after the client left", r.meta("s2"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := namer.Status(); st.Failing || st.Paused {
		t.Fatalf("status %+v, want no failure", st)
	}
}

// blockingWant is a namer whose Want takes as long as the test likes.
type blockingWant struct {
	fakeNaming
	release chan struct{}
}

func (b *blockingWant) Want(ids []string) {
	<-b.release
	b.fakeNaming.Want(ids)
}

// The list answers without waiting for Want, which reads metadata files.
func TestAPI_ListDoesNotWaitForWant(t *testing.T) {
	naming := &blockingWant{release: make(chan struct{})}
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Naming: naming, AutoName: true,
		Sessions: func(context.Context, int, bool) ([]Session, error) {
			return []Session{{ID: "a", LastActive: time.Now(), State: StateIdle}}, nil
		},
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			return SessionDetail{Session: Session{ID: id, State: StateIdle}, Turns: []Turn{}}, nil
		},
	}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	defer close(naming.release) // before srv.Close, which waits for handlers
	done := make(chan int, 2)
	go func() {
		resp, _ := do(t, srv, "GET", "/v1/sessions", "")
		done <- resp.StatusCode
		resp, _ = do(t, srv, "GET", "/v1/sessions/a", "")
		done <- resp.StatusCode
	}()
	for range 2 {
		select {
		case st := <-done:
			if st != 200 {
				t.Fatalf("status %d", st)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the handler waited for Want")
		}
	}
}

// waitWants waits until the namer was asked n times.
func waitWants(t *testing.T, f *fakeNaming, n int) [][]string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := f.allWants()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestShownIDs_OnlySessionsNowCanShow(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.Local)
	list := []Session{
		{ID: "morning", LastActive: now.Add(-5 * time.Hour), State: StateIdle},
		{ID: "edge-out", LastActive: now.Add(-901 * time.Second), State: StateIdle},
		{ID: "recent", LastActive: now.Add(-900 * time.Second), State: StateIdle},
		{ID: "busy-old", LastActive: now.Add(-48 * time.Hour), State: StateBusy},
		{ID: "term-old", LastActive: now.Add(-48 * time.Hour), State: StateOpenInTerminal},
		{ID: "pinned-old", LastActive: now.Add(-48 * time.Hour), State: StateIdle, Pinned: true},
		{ID: "mac", LastActive: now.Add(-48 * time.Hour), State: StateIdle},
	}
	got := shownIDs(list, &LastOnMac{ID: "mac"}, now)
	if fmt.Sprint(got) != "[mac pinned-old recent busy-old term-old]" {
		t.Fatalf("shownIDs %v, want Last on your Mac, pinned, then busy, in a terminal or finished within 900 s", got)
	}
}
