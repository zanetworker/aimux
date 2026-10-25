package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
)

// Session states as clients see them.
const (
	StateIdle           = "idle"             // can be continued from here
	StateOpenInTerminal = "open_in_terminal" // follow only
	StateBusy           = "busy"             // aimux is running a turn
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
}

// SessionDetail is a session plus its most recent turns.
type SessionDetail struct {
	Session
	Turns []json.RawMessage `json:"turns"`
}

// API serves /v1. Sessions and Session read aimux's session index.
type API struct {
	Broker   *Broker
	Sessions func(ctx context.Context, limit int, includeAutomated bool) ([]Session, error)
	Session  func(ctx context.Context, id string, turns int) (SessionDetail, error)
}

// Handler returns the /v1 routes, admitting only allowed Tailscale logins
// arriving through tailscale serve for one of trustedHosts.
func (a *API) Handler(allowedLogins, trustedHosts []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.health)
	mux.HandleFunc("GET /v1/sessions", a.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", a.getSession)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", a.sendMessage)
	mux.HandleFunc("GET /v1/sessions/{id}/events", a.events)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", a.cancel)
	mux.HandleFunc("POST /v1/approvals/{id}", a.answer)
	mux.Handle("GET /app/", webApp())
	mux.Handle("GET /app", http.RedirectHandler("/app/", http.StatusFound))
	return RequireTailnetUser(allowedLogins, trustedHosts, mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
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

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "login": LoginFrom(r.Context())})
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
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
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
	after := int64(0)
	for _, e := range a.Broker.Events(session, 0) {
		if e.Turn == turn && e.Kind == KindTurnStarted {
			after = e.Seq - 1
			break
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"turn": turn, "after": after,
		"events": fmt.Sprintf("/v1/sessions/%s/events?after=%d", session, after),
	})
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
	if log := r.URL.Query().Get("log"); log != "" && log != a.Broker.LogID() {
		after = 0
	}
	// On reconnect, EventSource repeats the original URL and adds the last id
	// it received, "<log>.<seq>"; that newer cursor wins. One from an earlier
	// log (the server restarted) restarts the replay from the beginning.
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		log, seq, scoped := strings.Cut(v, ".")
		if !scoped {
			seq = log
		}
		if n, err := strconv.ParseInt(seq, 10, 64); err == nil && n >= 0 {
			after = n
			if scoped && log != a.Broker.LogID() {
				after = 0
			}
		}
	}
	if r.URL.Query().Get("wait") == "0" {
		writeJSON(w, http.StatusOK, map[string]any{"events": a.Broker.Events(session, after), "log": a.Broker.LogID()})
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
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "answered"})
}
