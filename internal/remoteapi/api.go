package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	aimuxapi "github.com/zanetworker/aimux/api"
	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
)

// Session states as clients see them.
const (
	StateIdle           = "idle"             // can be continued from here
	StateOpenInTerminal = "open_in_terminal" // follow only
	StateBusy           = "busy"             // aimux is running a turn
	// StateUnavailable: created here, but its first prompt failed before the
	// agent wrote the session, so there is nothing to continue.
	StateUnavailable = "unavailable"
)

// Session is one entry in the session list. Automated sessions (started by
// a program, e.g. `claude -p`) are left out unless include_automated=1.
type Session struct {
	ID         string    `json:"id"`
	Provider   string    `json:"provider"`
	Title      string    `json:"title"`
	Project    string    `json:"project"`
	LastActive time.Time `json:"lastActive"`
	State      string    `json:"state"`
	LastPrompt string    `json:"lastPrompt,omitempty"` // the latest message the user sent
	// Description is the shown description (history.ShownDescription): the
	// owner's, else Claude Code's away summary, else aimux's.
	Description string `json:"description,omitempty"`
	Pinned      bool   `json:"pinned"` // the session's star
}

// SessionDetail is a session plus its most recent turns.
type SessionDetail struct {
	Session
	Turns []Turn `json:"turns"`
	// Stats is absent while the session is not in the index yet.
	Stats *SessionStats `json:"stats,omitempty"`
	// Terminal is set only for sessions open in a terminal on the Mac.
	Terminal *TerminalStatus `json:"terminal,omitempty"`
	// Repo is set only when the session's folder is a git repository on a
	// known host (GitHub or GitLab).
	Repo *Repo `json:"repo,omitempty"`
	// Queue is set only when the session's chat queue holds items.
	Queue *QueueStatus `json:"queue,omitempty"`
}

// TerminalInput types into sessions open in a terminal on the Mac, after its
// own safety checks (terminput.Service).
type TerminalInput interface {
	Send(ctx context.Context, sessionID, text, idemKey string) error
	Press(ctx context.Context, sessionID, promptID string, option terminput.Key, idemKey string) error
}

// SessionStats is what a session has cost and used so far, from the index.
type SessionStats struct {
	CostUSD          float64    `json:"costUsd"`
	TokensIn         int64      `json:"tokensIn"`
	TokensOut        int64      `json:"tokensOut"`
	CacheReadTokens  int64      `json:"cacheReadTokens"`
	CacheWriteTokens int64      `json:"cacheWriteTokens"`
	Turns            int        `json:"turns"`
	StartTime        *time.Time `json:"startTime,omitempty"` // nil when the index has none
	DurationMin      float64    `json:"durationMin"`
	Model            string     `json:"model,omitempty"`
	GitBranch        string     `json:"gitBranch,omitempty"`
}

// API serves /v1. Sessions and Session read aimux's session index.
type API struct {
	Version  string // reported by /v1/health
	Broker   *Broker
	Sessions func(ctx context.Context, limit int, includeAutomated bool) ([]Session, error)
	Session  func(ctx context.Context, id string, turns int) (SessionDetail, error)
	// SessionExists, if set, is the queue routes' check that a session is
	// known, without reading its detail; nil falls back to Session.
	SessionExists func(ctx context.Context, id string) error
	Search        func(ctx context.Context, query string, limit int) ([]SearchHit, error)
	// Projects lists the folders a new session may start in; Create starts
	// one there and returns its id.
	Projects func(ctx context.Context) ([]Project, error)
	Create   func(ctx context.Context, cwd, text string) (string, error)
	// Client, if set, is a browser client served at /app/ behind the same
	// identity check as /v1. It is any handler; the server knows nothing
	// else about it.
	Client http.Handler
	// RegisterDevice and UnregisterDevice, if set, let a native app sign a
	// phone up for approval notifications (APNs) and off again.
	RegisterDevice   func(push.Device) error
	UnregisterDevice func(token string) error
	// TerminalInput, with TerminalEnabled (remote_access.terminal_input.enabled),
	// serves /terminal/messages and /terminal/keys; otherwise both answer
	// 403 disabled.
	TerminalInput   TerminalInput
	TerminalEnabled bool
	// Links, if it names a Jira base, is reported in /v1/health.
	Links *Links
	// TerminalTyped, if set, is told after a successful send or key press,
	// so a cached terminal status of that session is dropped.
	TerminalTyped func(sessionID string)
	// Log, if set, receives details the server keeps from clients, such as
	// a terminal driver's error text.
	Log io.Writer
	// DeviceLogin, if set (remote_access.device_login), serves /v1/auth,
	// requires a paired device's session on every other route but the open
	// ones, ends event streams with their session and audits each action.
	// Nil keeps the tailnet check alone.
	DeviceLogin *devicelogin.Server
	// Queue, if set, serves /v1/sessions/{id}/queue. QueueKick (the
	// dispatcher's Kick) is called after each change; QueueNote gives a
	// session's waiting line. Either may be nil.
	Queue     *chatqueue.Store
	QueueKick func()
	QueueNote func(session string) string
	// Record, if set, gives a session's history record (its file and
	// metadata) and serves PATCH /v1/sessions/{id}/meta; with Naming it also
	// serves POST /v1/sessions/{id}/describe.
	Record func(ctx context.Context, id string) (history.Session, error)
	// Naming, if set, makes titles and descriptions ("Update summary") and
	// reports its state in health. With AutoName (sessions.auto_title) it is
	// also told which sessions clients show, to name them automatically.
	Naming   Naming
	AutoName bool
	// LastOnMac, if set, is the session last in front on the Mac
	// (terminput.LastFocus.Last).
	LastOnMac func() (id string, at time.Time, ok bool)

	createMu sync.Mutex        // one creation at a time, so a retried key cannot create twice
	created  map[string]string // client key -> new session id
}

// Handler returns the /v1 routes, admitting only allowed Tailscale logins
// arriving through tailscale serve for one of trustedHosts.
func (a *API) Handler(allowedLogins, trustedHosts []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.health)
	mux.HandleFunc("GET /v1/openapi.yaml", serveContract)
	mux.HandleFunc("GET /v1/sessions", a.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", a.getSession)
	mux.HandleFunc("POST /v1/sessions", a.audited(devicelogin.ActionNewSession, false, a.newSession))
	mux.HandleFunc("GET /v1/search", a.search)
	mux.HandleFunc("GET /v1/projects", a.projects)
	mux.HandleFunc("PATCH /v1/sessions/{id}/meta", a.patchMeta)
	mux.HandleFunc("POST /v1/sessions/{id}/describe", a.audited(devicelogin.ActionDescribe, true, a.describe))
	mux.HandleFunc("POST /v1/sessions/{id}/messages", a.audited(devicelogin.ActionSend, true, a.sendMessage))
	mux.HandleFunc("POST /v1/sessions/{id}/terminal/messages", a.audited(devicelogin.ActionTerminalMessage, true, a.terminalMessage))
	mux.HandleFunc("POST /v1/sessions/{id}/terminal/keys", a.audited(devicelogin.ActionTerminalKey, true, a.terminalKey))
	mux.HandleFunc("GET /v1/sessions/{id}/events", a.events)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", a.audited(devicelogin.ActionCancel, true, a.cancel))
	mux.HandleFunc("GET /v1/sessions/{id}/queue", a.queueGet)
	mux.HandleFunc("POST /v1/sessions/{id}/queue", a.audited(devicelogin.ActionQueueAdd, true, a.queueAdd))
	mux.HandleFunc("PATCH /v1/sessions/{id}/queue/{item}", a.queuePatch)
	mux.HandleFunc("DELETE /v1/sessions/{id}/queue/{item}", a.audited(devicelogin.ActionQueueDelete, true, a.queueDelete))
	mux.HandleFunc("POST /v1/sessions/{id}/queue/resume", a.audited(devicelogin.ActionQueueResume, true, a.queueResume))
	mux.HandleFunc("POST /v1/approvals/{id}", a.audited(devicelogin.ActionApprove, false, a.answer))
	mux.HandleFunc("POST /v1/devices", a.registerDevice)
	mux.HandleFunc("DELETE /v1/devices/{token}", a.unregisterDevice)
	if a.Client != nil {
		mux.Handle("GET /app/", a.Client)
		mux.Handle("GET /app", http.RedirectHandler("/app/", http.StatusFound))
	}
	var h http.Handler = mux
	if a.DeviceLogin != nil {
		a.DeviceLogin.Register(mux)
		h = a.DeviceLogin.Middleware(mux)
	}
	return RequireTailnetUser(allowedLogins, trustedHosts, h)
}

// audited writes an audit line for each request to h once it is answered:
// the action, the session in the path when withSession (never the body),
// and "ok" or http_<status>. Without device login it is h.
func (a *API) audited(action string, withSession bool, h http.HandlerFunc) http.HandlerFunc {
	if a.DeviceLogin == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		result := "ok"
		if rec.status >= 400 {
			result = "http_" + strconv.Itoa(rec.status)
		}
		session := ""
		if withSession {
			session = r.PathValue("id")
		}
		a.DeviceLogin.Record(r, action, session, result)
	}
}

// statusRecorder remembers the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, ErrorBody{Error: err.Error(), Code: code})
}

// writeFailure maps aimux errors to statuses and stable codes for clients.
func writeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, acprun.ErrUnknownSession):
		writeError(w, http.StatusNotFound, "unknown_session", err)
	case errors.Is(err, acprun.ErrOpenInTerminal):
		writeError(w, http.StatusConflict, "open_in_terminal", err)
	case errors.Is(err, acprun.ErrBusy):
		writeError(w, http.StatusConflict, "busy", err)
	case errors.Is(err, acprun.ErrInUseElsewhere):
		writeError(w, http.StatusConflict, "in_use", err)
	case errors.Is(err, acprun.ErrOwnershipUnknown):
		writeError(w, http.StatusServiceUnavailable, "ownership_unknown", err)
	case errors.Is(err, acprun.ErrNotRunning):
		writeError(w, http.StatusConflict, "not_running", err)
	case errors.Is(err, acprun.ErrTooManyRuns):
		writeError(w, http.StatusServiceUnavailable, "too_many_runs", err)
	case errors.Is(err, ErrNoSuchApproval):
		writeError(w, http.StatusNotFound, "no_such_approval", err)
	case errors.Is(err, ErrInvalidOption):
		writeError(w, http.StatusBadRequest, "invalid_option", err)
	case errors.Is(err, push.ErrBadDevice):
		writeError(w, http.StatusBadRequest, "bad_request", err)
	case errors.Is(err, errNotAProject):
		writeError(w, http.StatusBadRequest, "bad_request", err)
	default:
		writeError(w, http.StatusInternalServerError, "internal", err)
	}
}

func intParam(r *http.Request, name string, def, max int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return min(n, max), nil
}

// serveContract hands out the API's own OpenAPI document.
func serveContract(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(aimuxapi.RemoteV1)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	caps := []string{CapSessions}
	if a.Search != nil {
		caps = append(caps, CapSearch)
	}
	if a.Projects != nil && a.Create != nil {
		caps = append(caps, CapNewSessions)
	}
	if a.RegisterDevice != nil && a.UnregisterDevice != nil {
		caps = append(caps, CapPush)
	}
	if a.terminalOn() {
		caps = append(caps, CapTerminalInput)
	}
	if a.DeviceLogin != nil {
		caps = append(caps, CapDeviceLogin)
	}
	if a.Queue != nil {
		caps = append(caps, CapQueue)
	}
	if a.describing() {
		caps = append(caps, CapDescribe)
	}
	h := Health{Status: "ok", Login: LoginFrom(r.Context()), Version: a.Version,
		ACPProtocolVersion: int(acp.ProtocolVersionNumber), Capabilities: caps}
	if a.DeviceLogin != nil {
		h.Session = a.DeviceLogin.SessionState(r)
	}
	if a.Links != nil && a.Links.JiraBase != "" {
		h.Links = &Links{JiraBase: a.Links.JiraBase, JiraProjects: append([]string{}, a.Links.JiraProjects...)}
	}
	if a.Naming != nil {
		st := a.Naming.Status()
		h.Naming = &NamingHealth{Today: st.Today, Limit: st.Limit, Failing: st.Failing, Reason: st.Reason, Paused: st.Paused}
	}
	writeJSON(w, http.StatusOK, h)
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 50, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	list, err := a.Sessions(r.Context(), limit, r.URL.Query().Get("include_automated") == "1")
	if err != nil {
		writeFailure(w, err)
		return
	}
	if list == nil {
		list = []Session{}
	}
	out := SessionList{Sessions: list, LastOnMac: a.lastOnMac(r.Context())}
	writeJSON(w, http.StatusOK, out)
	a.want(shownIDs(list, out.LastOnMac, time.Now()))
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	turns, err := intParam(r, "turns", 20, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	d, err := a.Session(r.Context(), r.PathValue("id"), turns)
	if err != nil {
		writeFailure(w, err)
		return
	}
	if a.Queue != nil {
		if q := a.queueStatus(d.ID); len(q.Items) > 0 {
			d.Queue = &q
		}
	}
	writeJSON(w, http.StatusOK, d)
	a.want([]string{d.ID})
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("q is empty"))
		return
	}
	limit, err := intParam(r, "limit", 20, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if a.Search == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("search is not available"))
		return
	}
	hits, err := a.Search(r.Context(), q, limit)
	if err != nil {
		writeFailure(w, err)
		return
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	writeJSON(w, http.StatusOK, SearchResults{Results: hits})
}

func (a *API) projects(w http.ResponseWriter, r *http.Request) {
	if a.Projects == nil {
		writeJSON(w, http.StatusOK, ProjectList{Projects: []Project{}})
		return
	}
	list, err := a.Projects(r.Context())
	if err != nil {
		writeFailure(w, err)
		return
	}
	if list == nil {
		list = []Project{}
	}
	writeJSON(w, http.StatusOK, ProjectList{Projects: list})
}

// newSession starts a session in one of the recent project folders (never
// an arbitrary path) and sends its first message.
func (a *API) newSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd  string `json:"cwd"`
		Text string `json:"text"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with cwd and text: %w", err))
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("text is empty"))
		return
	}
	if a.Create == nil || a.Projects == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("starting sessions is not available"))
		return
	}

	a.createMu.Lock()
	id, known := a.created[req.Key]
	var err error
	if !known || req.Key == "" {
		// Only a new creation is checked against the current projects; a
		// retry of one already made is answered from its key.
		id, err = a.createIn(r.Context(), req.Cwd, req.Text)
		if err == nil && req.Key != "" {
			if a.created == nil {
				a.created = map[string]string{}
			}
			a.created[req.Key] = id
		}
	}
	a.createMu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	turn, err := a.Broker.Start(id, req.Text, req.Key)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.accepted(id, turn))
}

// accepted describes a started turn and where to follow it: the events URL
// starts just before the turn, so a client sees all of it.
func (a *API) accepted(session, turn string) TurnAccepted {
	after := int64(0)
	for _, e := range a.Broker.Events(session, 0) {
		if e.Turn == turn && e.Kind == KindTurnStarted {
			after = e.Seq - 1
			break
		}
	}
	return TurnAccepted{ID: session, Turn: turn, After: after,
		Events: fmt.Sprintf("/v1/sessions/%s/events?after=%d", session, after)}
}

// errNotAProject refuses a new session outside the recent project folders.
var errNotAProject = errors.New("cwd must be one of the recent projects from /v1/projects")

// createIn starts a session in cwd if it is one of the recent projects.
func (a *API) createIn(ctx context.Context, cwd, text string) (string, error) {
	projects, err := a.Projects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Path == cwd {
			return a.Create(ctx, cwd, text)
		}
	}
	return "", errNotAProject
}

func (a *API) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with a text field: %w", err))
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("text is empty"))
		return
	}
	session := r.PathValue("id")
	if a.queueActive(session) {
		writeError(w, http.StatusConflict, "busy", errQueueActive)
		return
	}
	turn, err := a.Broker.Start(session, req.Text, req.Key)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.accepted(session, turn))
}

// events streams the session's events as server-sent events, starting after
// the Last-Event-ID header if present, else ?after=. ?wait=0 returns one JSON
// page instead.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	session := r.PathValue("id")
	after := int64(0)
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("after must be a non-negative integer"))
			return
		}
		after = n
	}
	// A cursor taken from another log (the server restarted since) means
	// nothing here: replay this log from the start.
	paramLog := r.URL.Query().Get("log")
	if paramLog != "" && paramLog != a.Broker.LogID() {
		after = 0
	}
	// On reconnect, EventSource repeats the original URL and adds the last id
	// it received, "<log>.<seq>"; that newer cursor wins. One from an earlier
	// log (the server restarted) restarts the replay from the beginning. A
	// bare "<seq>" belongs to the log named by ?log=, if any.
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		log, seq, scoped := strings.Cut(v, ".")
		if !scoped {
			seq, log = log, paramLog
		}
		if n, err := strconv.ParseInt(seq, 10, 64); err == nil && n >= 0 {
			after = n
			if log != "" && log != a.Broker.LogID() {
				after = 0
			}
		}
	}
	// Queue events are for clients that ask: one before the queue would
	// take an event of no turn for a turn still running.
	withQueue := r.URL.Query().Get("queue") == "1"
	if r.URL.Query().Get("wait") == "0" {
		evs := a.Broker.Events(session, after)
		if !withQueue {
			evs = slices.DeleteFunc(evs, func(e Event) bool { return e.Kind == KindQueue })
		}
		writeJSON(w, http.StatusOK, EventPage{Events: evs, Log: a.Broker.LogID()})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// A stream ends with its login (logout, revoke, idle or 12 hours); it
	// never counts as use. Without device login, ended is nil and never fires.
	var ended <-chan struct{}
	if s, ok := devicelogin.SessionFrom(r.Context()); ok {
		ended = s.Done()
	}
	ch, stop := a.Broker.Subscribe(session, after)
	defer stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ended:
			return
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				return // fell behind; the client reconnects with Last-Event-ID
			}
			if e.Kind == KindQueue && !withQueue {
				continue
			}
			data, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "id: %s.%d\nevent: %s\ndata: %s\n\n", a.Broker.LogID(), e.Seq, e.Kind, data)
			flusher.Flush()
		}
	}
}

func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	if err := a.Broker.Cancel(r.PathValue("id")); err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, Status{Status: "cancelling"})
}

func (a *API) answer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Option string `json:"option"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with an option field: %w", err))
		return
	}
	if err := a.Broker.Answer(r.PathValue("id"), req.Option); err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Status{Status: "answered"})
}

func (a *API) registerDevice(w http.ResponseWriter, r *http.Request) {
	if a.RegisterDevice == nil || a.UnregisterDevice == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("push notifications are not configured on this server"))
		return
	}
	var req struct {
		Token       string `json:"token"`
		Environment string `json:"environment"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with token and environment: %w", err))
		return
	}
	if err := a.RegisterDevice(push.Device{Token: req.Token, Environment: req.Environment, Login: LoginFrom(r.Context())}); err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Status{Status: "registered"})
}

func (a *API) unregisterDevice(w http.ResponseWriter, r *http.Request) {
	if a.RegisterDevice == nil || a.UnregisterDevice == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("push notifications are not configured on this server"))
		return
	}
	if err := a.UnregisterDevice(r.PathValue("token")); err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Status{Status: "unregistered"})
}

func (a *API) typed(sessionID string) {
	if a.TerminalTyped != nil {
		a.TerminalTyped(sessionID)
	}
}

func (a *API) terminalOn() bool { return a.TerminalEnabled && a.TerminalInput != nil }

// errTerminalDriver is what a client sees of a driver failure: its text
// (osascript stderr, JXA exceptions) goes to the server's log only.
var errTerminalDriver = errors.New("the terminal driver failed")

// maxTerminalBody caps a terminal message or key request.
const maxTerminalBody = 64 << 10

// terminalMessages are the client-facing messages per terminput code.
// Clients never see err.Error(): terminput wraps driver text into some
// coded errors.
var terminalMessages = map[string]struct {
	status int
	msg    string
}{
	"disabled":           {http.StatusForbidden, "terminal input is off on the Mac"},
	"bad_text":           {http.StatusBadRequest, "the text holds control characters, or starts with ! or / (bash mode, slash commands)"},
	"not_in_terminal":    {http.StatusConflict, "the session is not open in a terminal"},
	"not_claude":         {http.StatusConflict, "the terminal is not running this session's Claude Code"},
	"mac_in_use":         {http.StatusConflict, "you're using this session on your Mac"},
	"no_terminal_driver": {http.StatusConflict, "no terminal driver reaches this session (iTerm2 only)"},
	"automation_denied":  {http.StatusConflict, "macOS denied aimux Automation access to the terminal"},
	"not_at_input":       {http.StatusConflict, "Claude Code is not at an empty input box"},
	"not_submitted":      {http.StatusConflict, "the message is in the input box but was not sent; the screen changed, a prompt may be showing"},
	"busy":               {http.StatusConflict, "Claude is working in this session; try again when it finishes"},
	"no_prompt":          {http.StatusConflict, "no permission prompt is showing"},
	"prompt_changed":     {http.StatusConflict, "a different permission prompt is showing"},
}

// writeTerminalFailure answers a terminput error with its code's fixed
// message; anything else (a driver failure) is a 500 with a fixed message,
// its detail written to log.
func writeTerminalFailure(w http.ResponseWriter, err error, log io.Writer) {
	code := terminput.Code(err)
	if m, ok := terminalMessages[code]; ok {
		writeJSON(w, m.status, ErrorBody{Error: m.msg, Code: code})
		return
	}
	if log != nil {
		_, _ = fmt.Fprintf(log, "terminal input: %q\n", err.Error())
	}
	writeError(w, http.StatusInternalServerError, "internal", errTerminalDriver)
}

// terminalError answers a failed terminal attempt: a session the server
// does not know is a 404, not "not in a terminal". The attempt is never
// retried here: a retry could type the text twice.
func (a *API) terminalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, terminput.ErrNotInTerminal) && a.Session != nil {
		if _, derr := a.Session(r.Context(), r.PathValue("id"), 1); errors.Is(derr, acprun.ErrUnknownSession) {
			writeFailure(w, derr)
			return
		}
	}
	writeTerminalFailure(w, err, a.Log)
}

// errQueueActive answers a direct message while the session's queue is
// sending: it would join the queue's turn, or jump ahead of its items.
var errQueueActive = errors.New("messages are queued for this session; add this one to the queue")

// queueActive reports whether session's chat queue has an item in flight,
// or is running with items waiting: the dispatcher may type at any moment.
func (a *API) queueActive(session string) bool {
	if a.Queue == nil {
		return false
	}
	q := a.Queue.Get(session)
	for _, it := range q.Items {
		if it.State != chatqueue.StateWaiting || q.State == chatqueue.QueueRunning {
			return true
		}
	}
	return false
}

// terminalMessage types text into the session's terminal pane.
func (a *API) terminalMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Key  string `json:"key"`
	}
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with a text field, at most 64 KiB: %w", err))
		return
	}
	if !a.terminalOn() {
		writeTerminalFailure(w, terminput.ErrDisabled, a.Log)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("text is empty"))
		return
	}
	if a.queueActive(r.PathValue("id")) {
		writeError(w, http.StatusConflict, "busy", errQueueActive)
		return
	}
	if err := a.TerminalInput.Send(r.Context(), r.PathValue("id"), req.Text, req.Key); err != nil {
		a.terminalError(w, r, err)
		return
	}
	a.typed(r.PathValue("id"))
	writeJSON(w, http.StatusAccepted, Status{Status: "typed"})
}

// terminalKey presses one option of the permission prompt promptId.
func (a *API) terminalKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PromptID string `json:"promptId"`
		Option   string `json:"option"`
		Key      string `json:"key"`
	}
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with promptId and option: %w", err))
		return
	}
	if !a.terminalOn() {
		writeTerminalFailure(w, terminput.ErrDisabled, a.Log)
		return
	}
	if req.PromptID == "" || req.Option == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("promptId and option are required"))
		return
	}
	if err := a.TerminalInput.Press(r.Context(), r.PathValue("id"), req.PromptID, terminput.Key(req.Option), req.Key); err != nil {
		a.terminalError(w, r, err)
		return
	}
	a.typed(r.PathValue("id"))
	writeJSON(w, http.StatusAccepted, Status{Status: "typed"})
}

// decodeStrict reads a terminal request body: at most maxTerminalBody, and
// only the fields the contract lists.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTerminalBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
