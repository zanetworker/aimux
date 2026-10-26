package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	aimuxapi "github.com/zanetworker/aimux/api"
	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/push"
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
}

// SessionDetail is a session plus its most recent turns.
type SessionDetail struct {
	Session
	Turns []Turn `json:"turns"`
}

// API serves /v1. Sessions and Session read aimux's session index.
type API struct {
	Version  string // reported by /v1/health
	Broker   *Broker
	Sessions func(ctx context.Context, limit int, includeAutomated bool) ([]Session, error)
	Session  func(ctx context.Context, id string, turns int) (SessionDetail, error)
	Search   func(ctx context.Context, query string, limit int) ([]SearchHit, error)
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
	mux.HandleFunc("POST /v1/sessions", a.newSession)
	mux.HandleFunc("GET /v1/search", a.search)
	mux.HandleFunc("GET /v1/projects", a.projects)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", a.sendMessage)
	mux.HandleFunc("GET /v1/sessions/{id}/events", a.events)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", a.cancel)
	mux.HandleFunc("POST /v1/approvals/{id}", a.answer)
	mux.HandleFunc("POST /v1/devices", a.registerDevice)
	mux.HandleFunc("DELETE /v1/devices/{token}", a.unregisterDevice)
	if a.Client != nil {
		mux.Handle("GET /app/", a.Client)
		mux.Handle("GET /app", http.RedirectHandler("/app/", http.StatusFound))
	}
	return RequireTailnetUser(allowedLogins, trustedHosts, mux)
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
	writeJSON(w, http.StatusOK, Health{Status: "ok", Login: LoginFrom(r.Context()), Version: a.Version,
		ACPProtocolVersion: int(acp.ProtocolVersionNumber), Capabilities: caps})
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
	writeJSON(w, http.StatusOK, SessionList{Sessions: list})
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
	writeJSON(w, http.StatusOK, d)
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
	if r.URL.Query().Get("wait") == "0" {
		writeJSON(w, http.StatusOK, EventPage{Events: a.Broker.Events(session, after), Log: a.Broker.LogID()})
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

	ch, stop := a.Broker.Subscribe(session, after)
	defer stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				return // fell behind; the client reconnects with Last-Event-ID
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
