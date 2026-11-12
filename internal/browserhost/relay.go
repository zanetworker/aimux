package browserhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/profilerun"
)

const (
	// apiBase is the v1 API on the wingman socket; serve requires Host aimux.local.
	apiBase = "http://aimux.local"
	// deltaChunk bounds delta text per event: JSON escaping can grow text
	// about 6x, so 64 KiB keeps each frame under Chrome's 1 MiB cap.
	deltaChunk = 64 * 1024
)

// profileOfLane is the profile each panel lane asks under.
var profileOfLane = map[string]string{"page": "page", "work": "work-readonly"}

// Relay serves one Wingman side panel over native messaging (the frames and
// Request/Event JSON the extension speaks) by turning its requests into
// calls on aimux's v1 API on the wingman socket.
type Relay struct {
	Socket string
	// Start starts the server behind Socket (aimux remote serve
	// --local-only); nil means never start one.
	Start func(ctx context.Context) error
	// Dial connects to Socket; nil dials it as a unix socket.
	Dial func(ctx context.Context) (net.Conn, error)
}

// Serve relays the panel's requests until in ends (the panel closed: nil)
// or ctx ends (ctx.Err()). Requests run concurrently; each ends with one
// done or error event.
func (r *Relay) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	s := &relaySession{relay: r, out: out, inflight: map[string]*inflight{}, bg: &wg,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return r.connect(ctx) },
		}}}
	defer func() {
		cancel()
		wg.Wait()
		s.http.CloseIdleConnections()
	}()

	reqs := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		for {
			msg, err := ReadMessage(in)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case reqs <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			if c, ok := in.(io.Closer); ok {
				_ = c.Close() // unblock the reader
			}
			return ctx.Err()
		case err := <-readErr:
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return fmt.Errorf("read from chrome: %w", err)
		case msg := <-reqs:
			s.dispatch(ctx, msg)
		}
	}
}

// relaySession is one panel connection's state.
type relaySession struct {
	relay *Relay
	http  *http.Client
	bg    *sync.WaitGroup // request and cancel goroutines; Serve waits for them

	wmu sync.Mutex
	out io.Writer

	mu       sync.Mutex
	inflight map[string]*inflight
}

// inflight is one running request. After a cancel has replied done, its
// later events are dropped so the panel sees one terminal event per id.
type inflight struct {
	cancel   context.CancelFunc
	mu       sync.Mutex
	canceled bool
	session  string // the v1 session, once known
}

func (s *relaySession) dispatch(ctx context.Context, msg []byte) {
	var req Request
	if err := json.Unmarshal(msg, &req); err != nil {
		var idOnly struct {
			ID string `json:"id"`
		}
		// The panel reads an id "" error as a disconnect: send a notice then.
		if json.Unmarshal(msg, &idOnly) == nil && idOnly.ID != "" {
			s.send(Event{ID: idOnly.ID, Type: "error", Error: "bad request: " + err.Error()})
		} else {
			s.send(Event{Type: "notice", Error: "bad request: " + err.Error()})
		}
		return
	}
	if req.Type == "cancel" {
		s.cancel(req.ID)
		return
	}
	rctx, rcancel := context.WithCancel(ctx)
	f := &inflight{cancel: rcancel}
	s.mu.Lock()
	if s.inflight[req.ID] != nil {
		s.mu.Unlock()
		rcancel()
		s.send(Event{ID: req.ID, Type: "error", Error: "request id already in flight: " + req.ID})
		return
	}
	s.inflight[req.ID] = f
	s.mu.Unlock()
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		defer rcancel()
		emit := func(e Event) {
			e.ID = req.ID
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.canceled {
				s.send(e)
			}
		}
		s.handle(rctx, req, f, emit)
		s.mu.Lock()
		delete(s.inflight, req.ID)
		s.mu.Unlock()
	}()
}

func (s *relaySession) handle(ctx context.Context, req Request, f *inflight, emit func(Event)) {
	fail := func(err error) { emit(Event{Type: "error", Error: panelMessage(err)}) }
	switch req.Type {
	case "ask":
		if err := s.ask(ctx, req, f, emit); err != nil {
			fail(err)
		}
	case "history.list":
		pages, err := s.listPages(ctx, req.Query)
		if err != nil {
			fail(err)
			return
		}
		emit(Event{Type: "history", Pages: pages})
	case "history.get":
		page, err := s.getPage(ctx, pageKey(req.URL))
		if err != nil {
			fail(err)
			return
		}
		emit(Event{Type: "history", Page: page})
		if page == nil {
			emit(Event{Type: "done"}) // page: null means not found
		}
	case "history.delete":
		if err := s.deletePage(ctx, pageKey(req.URL)); err != nil {
			fail(err)
			return
		}
		emit(Event{Type: "done"})
	default:
		fail(errors.New("unknown request type: " + req.Type))
	}
}

// ask starts (or continues) the page's session under the lane's profile
// and streams the turn. It always posts to /v1/sessions: the server reuses
// the page's session for the same client, url and profile (FindPage), so
// the relay keeps no page-to-session state of its own.
func (s *relaySession) ask(ctx context.Context, req Request, f *inflight, emit func(Event)) error {
	if req.Lane == "" {
		return errors.New("lane is required (page or work)")
	}
	profile, ok := profileOfLane[req.Lane]
	if !ok {
		return errors.New("unknown lane: " + req.Lane)
	}
	pageURL := pageKey(req.URL)
	if pageURL == "" {
		return errors.New("url is required")
	}
	var acc turnAccepted
	body := map[string]string{"profile": profile, "url": pageURL, "title": req.Title, "pageText": req.Page, "text": req.Question}
	if req.Model != "" {
		body["model"] = req.Model // the panel's Sonnet/Opus choice; the server checks it
	}
	if err := s.call(ctx, http.MethodPost, "/v1/sessions", body, &acc); err != nil {
		return err
	}
	f.mu.Lock()
	f.session = acc.ID
	canceled := f.canceled
	f.mu.Unlock()
	if canceled { // cancelled while the session was being started
		s.postCancel(acc.ID)
		return nil
	}
	return s.follow(ctx, acc, emit)
}

// cancel answers the panel's cancel of request id: done at once (and only
// once), then the v1 cancel of its session. Before the session is known the
// ask itself posts the cancel once its start returns, so a started session
// is never left running.
func (s *relaySession) cancel(id string) {
	s.mu.Lock()
	f := s.inflight[id]
	s.mu.Unlock()
	if f == nil {
		s.send(Event{ID: id, Type: "done"})
		return
	}
	f.mu.Lock()
	if !f.canceled {
		f.canceled = true
		s.send(Event{ID: id, Type: "done"})
	}
	session := f.session
	f.mu.Unlock()
	if session != "" {
		s.postCancel(session)
		f.cancel() // stop following the stream
	}
}

// cancelTimeout bounds the v1 cancel; it outlives the panel's request.
const cancelTimeout = 10 * time.Second

// postCancel asks the server to stop session's turn, in the background.
func (s *relaySession) postCancel(session string) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
		defer cancel()
		_ = s.call(ctx, http.MethodPost, sessionPath(session)+"/cancel", map[string]any{}, nil) // best effort: the panel already shows Stopped
	}()
}

// maxListed is how many sessions and pages history reads and lists.
const (
	maxListed = 500
	maxPages  = 100
)

// v1Session is a v1 session list entry (the fields the relay reads).
type v1Session struct {
	ID         string    `json:"id"`
	LastActive time.Time `json:"lastActive"`
	Profile    string    `json:"profile"`
	Page       *struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"page"`
}

// laneOfProfile is the panel lane a profile session belongs to.
func laneOfProfile(profile string) string {
	for lane, p := range profileOfLane {
		if p == profile {
			return lane
		}
	}
	return profile
}

// sessions lists Wingman's sessions, newest first; pageURL narrows them to
// one page when set.
func (s *relaySession) sessions(ctx context.Context, pageURL string) ([]v1Session, error) {
	q := url.Values{"client": {"wingman"}, "limit": {strconv.Itoa(maxListed)}}
	if pageURL != "" {
		q.Set("url", pageURL)
	}
	var list struct {
		Sessions []v1Session `json:"sessions"`
	}
	if err := s.call(ctx, http.MethodGet, "/v1/sessions?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	return list.Sessions, nil
}

// listPages groups Wingman's sessions into pages by URL, newest first, at
// most maxPages; query keeps the pages whose title or URL contains it
// (case-insensitively). A session's time is when it started (the v1 list
// gives profile sessions no later activity time).
func (s *relaySession) listPages(ctx context.Context, query string) ([]Page, error) {
	list, err := s.sessions(ctx, "")
	if err != nil {
		return nil, err
	}
	byURL := map[string]*Page{}
	var order []*Page
	for _, ss := range list {
		if ss.Page == nil || ss.Page.URL == "" {
			continue
		}
		p := byURL[ss.Page.URL]
		if p == nil {
			p = &Page{URL: ss.Page.URL, Title: ss.Page.Title, FirstAsked: ss.LastActive, LastAsked: ss.LastActive, Turns: []Turn{}}
			byURL[ss.Page.URL] = p
			order = append(order, p)
		}
		if ss.LastActive.After(p.LastAsked) {
			p.LastAsked = ss.LastActive
			if ss.Page.Title != "" {
				p.Title = ss.Page.Title
			}
		}
		if ss.LastActive.Before(p.FirstAsked) {
			p.FirstAsked = ss.LastActive
		}
		if p.Title == "" {
			p.Title = ss.Page.Title
		}
		setSessionID(p, ss)
	}
	q := strings.ToLower(query)
	pages := []Page{}
	for _, p := range order {
		if q == "" || strings.Contains(strings.ToLower(p.Title), q) || strings.Contains(strings.ToLower(p.URL), q) {
			pages = append(pages, *p)
		}
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if !pages[i].LastAsked.Equal(pages[j].LastAsked) {
			return pages[i].LastAsked.After(pages[j].LastAsked)
		}
		return pages[i].URL < pages[j].URL
	})
	return pages[:min(len(pages), maxPages)], nil
}

const (
	// history.get sends at most the last maxHistoryTurns turns, and drops
	// older ones until the page's JSON fits maxHistoryBytes: one event must
	// fit Chrome's 1 MiB message cap.
	maxHistoryTurns = 50
	maxHistoryBytes = 512 * 1024
)

// getPage is pageURL's conversation: the turns of every Wingman session on
// it, oldest first, capped for the panel; nil when it has none. A session
// whose detail is gone (404) is left out.
func (s *relaySession) getPage(ctx context.Context, pageURL string) (*Page, error) {
	list, err := s.sessions(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	var p *Page
	for _, ss := range list {
		if ss.Page == nil || ss.Page.URL != pageURL {
			continue
		}
		if p == nil {
			p = &Page{URL: pageURL, Title: ss.Page.Title, FirstAsked: ss.LastActive, LastAsked: ss.LastActive, Turns: []Turn{}}
		}
		if ss.LastActive.Before(p.FirstAsked) {
			p.FirstAsked = ss.LastActive
		}
		setSessionID(p, ss)
		var d struct {
			Turns []struct {
				Time  time.Time `json:"time"`
				User  string    `json:"user"`
				Reply string    `json:"reply"`
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"turns"`
		}
		err := s.call(ctx, http.MethodGet, sessionPath(ss.ID)+"?turns="+strconv.Itoa(maxHistoryTurns), nil, &d)
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, t := range d.Turns {
			tools := []string{}
			for _, tl := range t.Tools {
				tools = append(tools, tl.Name)
			}
			p.Turns = append(p.Turns, Turn{At: t.Time, Lane: laneOfProfile(ss.Profile), Question: profilerun.QuestionOf(t.User), Answer: t.Reply, ToolCalls: tools})
			if t.Time.After(p.LastAsked) {
				p.LastAsked = t.Time
			}
		}
	}
	if p == nil {
		return nil, nil
	}
	sort.SliceStable(p.Turns, func(i, j int) bool { return p.Turns[i].At.Before(p.Turns[j].At) })
	capped := capPage(*p)
	return &capped, nil
}

// deletePage hides every Wingman session on pageURL; v1 never deletes a
// session, and a hidden one leaves the lists.
func (s *relaySession) deletePage(ctx context.Context, pageURL string) error {
	list, err := s.sessions(ctx, pageURL)
	if err != nil {
		return err
	}
	for _, ss := range list {
		if ss.Page == nil || ss.Page.URL != pageURL {
			continue
		}
		if err := s.call(ctx, http.MethodPatch, sessionPath(ss.ID)+"/meta", map[string]bool{"hidden": true}, nil); err != nil {
			return err
		}
	}
	return nil
}

// capPage trims p for the panel to the last maxHistoryTurns turns that fit
// maxHistoryBytes, marking it Truncated when any turn is left out.
func capPage(p Page) Page {
	turns := p.Turns
	if len(turns) > maxHistoryTurns {
		turns = turns[len(turns)-maxHistoryTurns:]
		p.Truncated = true
	}
	p.Turns = []Turn{}
	base, _ := json.Marshal(p)
	size := len(base) + len(`,"truncated":true`)
	keep := len(turns)
	for keep > 0 {
		b, _ := json.Marshal(turns[keep-1])
		if size+len(b)+1 > maxHistoryBytes { // +1: the comma
			break
		}
		size += len(b) + 1
		keep--
	}
	if keep > 0 {
		p.Truncated = true
	}
	p.Turns = turns[keep:]
	return p
}

// setSessionID records ss as p's page or work session, keeping the first
// (newest) one of each lane.
func setSessionID(p *Page, ss v1Session) {
	switch laneOfProfile(ss.Profile) {
	case "page":
		if p.PageSessionID == "" {
			p.PageSessionID = ss.ID
		}
	case "work":
		if p.WorkSessionID == "" {
			p.WorkSessionID = ss.ID
		}
	}
}

// turnAccepted is the v1 TurnAccepted.
type turnAccepted struct {
	ID     string `json:"id"`
	Turn   string `json:"turn"`
	After  int64  `json:"after"`
	Events string `json:"events"`
}

// v1Event is one entry of a v1 session's event log.
type v1Event struct {
	Seq  int64           `json:"seq"`
	Turn string          `json:"turn"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// maxStalls is how many streams in a row may end without a new event
// before the relay gives up on a turn.
const maxStalls = 3

// resumeIdle is how long a reopened stream may carry nothing new.
var resumeIdle = 60 * time.Second

// errRestarted ends a turn the server no longer knows (it restarted).
var errRestarted = errors.New("the Wingman server restarted; ask again")

// follow streams acc's turn from its events URL, mapping it to panel events,
// until the turn ends. A stream that ends mid-turn (the server drops a
// subscriber that fell behind) is reopened after the last event seen.
func (s *relaySession) follow(ctx context.Context, acc turnAccepted, emit func(Event)) error {
	path, after, stalls, resumed := acc.Events, acc.After, 0, false
	for {
		progressed, end, err := s.stream(ctx, path, acc.Turn, &after, emit, resumed)
		if end || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if progressed {
			stalls = 0
		} else if stalls++; stalls >= maxStalls {
			return fmt.Errorf("the answer stream keeps ending early: %v", err)
		}
		path, resumed = fmt.Sprintf("%s/events?after=%d", sessionPath(acc.ID), after), true
	}
}

// stream reads one events stream, emitting turn's events and advancing
// after; end is true once the turn is over (err is then the turn's error).
// A resumed stream that the server answers 404 (a restarted server knows
// neither the session's events nor the turn) ends the turn with
// errRestarted; so does one that carries no frame at all, not even a
// keepalive, for resumeIdle. Any frame keeps a resumed stream alive.
func (s *relaySession) stream(ctx context.Context, path, turn string, after *int64, emit func(Event), resumed bool) (progressed, end bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var idled atomic.Bool
	reset := func() {}
	if resumed {
		timer := time.AfterFunc(resumeIdle, func() { idled.Store(true); cancel() })
		defer timer.Stop()
		reset = func() { timer.Reset(resumeIdle) }
	}
	resp, err := s.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		var ae *apiError
		switch {
		case idled.Load():
			return false, true, errRestarted
		case resumed && errors.As(err, &ae) && ae.Status == http.StatusNotFound:
			return false, true, errRestarted
		}
		return false, errors.As(err, &ae), err // an API refusal ends the turn; a dropped connection is retried
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	for {
		ev, err := readSSE(br, reset)
		if err != nil {
			if idled.Load() {
				return progressed, true, errRestarted
			}
			return progressed, false, err
		}
		if ev.Seq > *after {
			*after = ev.Seq
			progressed = true
		}
		if ev.Turn != turn {
			continue
		}
		if end, err := mapEvent(ev, emit); end {
			return true, true, err
		}
	}
}

// readSSE returns the next server-sent event's data as a v1Event, skipping
// comments (keepalives) and fields other than data; frame is called for
// every line read, keepalives included.
func readSSE(br *bufio.Reader, frame func()) (v1Event, error) {
	var data []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return v1Event{}, err
		}
		frame()
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data == nil {
				continue
			}
			var ev v1Event
			if err := json.Unmarshal(data, &ev); err != nil {
				return v1Event{}, fmt.Errorf("bad event: %w", err)
			}
			return ev, nil
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " ")...)
		}
	}
}

// mapEvent emits ev's panel events; end is true once the turn is over, with
// the turn's error, if any.
func mapEvent(ev v1Event, emit func(Event)) (end bool, err error) {
	switch ev.Kind {
	case "update":
		var u struct {
			Kind    string `json:"sessionUpdate"`
			Title   string `json:"title"`
			Content json.RawMessage
		}
		if json.Unmarshal(ev.Data, &u) != nil {
			return false, nil
		}
		switch u.Kind {
		case "agent_message_chunk":
			var c struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(u.Content, &c) == nil && c.Type == "text" && c.Text != "" {
				emit(Event{Type: "delta", Text: c.Text})
			}
		case "tool_call":
			emit(Event{Type: "tool_call", Tool: u.Title})
		}
		return false, nil
	case "turn_done":
		emit(Event{Type: "done"})
		return true, nil
	case "turn_error":
		var d struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(ev.Data, &d) // an unreadable error still ends the turn
		if d.Error == "" {
			d.Error = "the turn failed"
		}
		return true, errors.New(d.Error)
	}
	return false, nil
}

// apiError is a v1 error response.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("aimux API %d %s: %s", e.Status, e.Code, e.Message)
}

// panelMessage is the plain text the panel shows for err.
func panelMessage(err error) string {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	switch ae.Code {
	case "profile_not_allowed":
		return "Wingman is not allowed to use this profile"
	case "client_limit":
		return "Too many Wingman questions at once; try again in a moment"
	}
	if ae.Message != "" {
		return ae.Message
	}
	return http.StatusText(ae.Status)
}

// call sends a JSON request and decodes a 2xx JSON answer into out (when
// non-nil); an error status becomes an *apiError.
func (s *relaySession) call(ctx context.Context, method, path string, body, out any) error {
	resp, err := s.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("aimux API %s %s: bad answer: %w", method, path, err)
	}
	return nil
}

// do sends a request; a non-2xx status is returned as an *apiError.
func (s *relaySession) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	ae := &apiError{Status: resp.StatusCode}
	var eb struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&eb) == nil {
		ae.Code, ae.Message = eb.Code, eb.Error
	}
	return nil, ae
}

const (
	// startTimeout is how long a relay waits for a server it started.
	startTimeout = 15 * time.Second
	startPoll    = 20 * time.Millisecond
)

// connect dials the wingman socket. When nothing answers there it takes
// the lock next to the socket, dials again (another relay may have started
// the server meanwhile), else runs Start and waits for the socket, holding
// the lock so that relays starting together start one server.
func (r *Relay) connect(ctx context.Context) (net.Conn, error) {
	c, err := r.dial(ctx)
	if err == nil || !staleDialErr(err) {
		return c, err
	}
	if r.Start == nil {
		return nil, fmt.Errorf("aimux remote serve is not running (%s): %w", r.Socket, err)
	}
	if err := os.MkdirAll(filepath.Dir(r.Socket), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(r.Socket), err)
	}
	unlock, err := lockFile(r.Socket + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if c, err := r.dial(ctx); err == nil {
		return c, nil
	}
	if err := r.Start(ctx); err != nil {
		return nil, fmt.Errorf("start aimux remote serve: %w", err)
	}
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(startPoll)
	defer tick.Stop()
	for {
		if c, err := r.dial(ctx); err == nil {
			return c, nil
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return nil, fmt.Errorf("aimux remote serve did not start within %v (see ~/Library/Logs/aimux/wingman/host.log)", startTimeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (r *Relay) dial(ctx context.Context) (net.Conn, error) {
	if r.Dial != nil {
		return r.Dial(ctx)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", r.Socket)
}

// staleDialErr reports a socket nobody serves: missing, refused, or a
// non-socket file in its place.
func staleDialErr(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTSOCK)
}

// lockFile takes an exclusive flock on path (0600, never through a
// symlink) and returns its release.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: path is the lock file in the user's own aimux dir; O_NOFOLLOW refuses symlinks
	if err != nil {
		return nil, fmt.Errorf("open wingman lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close() // the lock error is the one to report
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // Close releases it anyway
		_ = f.Close()
	}, nil
}

// send writes e, splitting long delta text so each frame fits Chrome's cap.
func (s *relaySession) send(e Event) {
	if e.Type == "delta" && len(e.Text) > deltaChunk {
		for _, part := range ChunkText(e.Text, deltaChunk) {
			s.write(Event{ID: e.ID, Type: "delta", Text: part})
		}
		return
	}
	s.write(e)
}

func (s *relaySession) write(e Event) {
	msg, err := json.Marshal(e)
	if err != nil {
		msg, _ = json.Marshal(Event{ID: e.ID, Type: "error", Error: "encode event: " + err.Error()})
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := WriteMessage(s.out, msg); errors.Is(err, ErrTooLarge) {
		msg, _ = json.Marshal(Event{ID: e.ID, Type: "error", Error: fmt.Sprintf("%s event too large for Chrome (%d bytes)", e.Type, len(msg))})
		_ = WriteMessage(s.out, msg) // Chrome gone: Serve ends when in closes
	}
}

// pageKey is the URL without a plain #anchor, so a jump within a page stays
// on the same page, as in the panel (samePage in wingman's state.ts). A
// fragment with a "/" is an app route (Gmail's #inbox/<id>, hash routers'
// #/path) and names a different page, so it is kept.
func pageKey(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 && !strings.Contains(u[i+1:], "/") {
		return u[:i]
	}
	return u
}

// sessionPath is /v1/sessions/{id} with id escaped.
func sessionPath(id string) string { return "/v1/sessions/" + url.PathEscape(id) }
