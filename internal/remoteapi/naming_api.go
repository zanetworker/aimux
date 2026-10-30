package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// Naming makes session titles and descriptions (history.Namer).
type Naming interface {
	// Want queues sessions a client shows; those that need a name get one.
	Want(ids []string)
	// UpdateSummary writes a new title and description now ("Update
	// summary"); owner values stay.
	UpdateSummary(ctx context.Context, s history.Session) (history.SummaryResult, error)
	Status() history.NamingStatus
}

// Limits on the owner's own values, in characters.
const (
	maxTitleChars       = 80
	maxDescriptionChars = 300
	// maxWanted caps the sessions one list request queues for naming.
	maxWanted = 30
)

// finishedWindow is how long Now shows a session as just finished (the
// clients' RECENT_MS).
const finishedWindow = 900 * time.Second

func (a *API) describing() bool { return a.Naming != nil && a.Record != nil }

// want queues the sessions a client was shown for automatic naming, only
// with sessions.auto_title on. It runs off the request: Want reads each
// session's metadata file.
func (a *API) want(ids []string) {
	if a.AutoName && a.Naming != nil && len(ids) > 0 {
		go a.Naming.Want(ids)
	}
}

// shownIDs are the list's sessions a client shows on Now: Last on your Mac,
// the pinned ones, then those working (busy or open in a terminal) or
// finished within finishedWindow; at most maxWanted. The opened chat is
// queued by getSession.
func shownIDs(list []Session, last *LastOnMac, now time.Time) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] && len(ids) < maxWanted {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if last != nil {
		add(last.ID)
	}
	for _, s := range list {
		if s.Pinned {
			add(s.ID)
		}
	}
	for _, s := range list {
		if s.State == StateBusy || s.State == StateOpenInTerminal || now.Sub(s.LastActive) <= finishedWindow {
			add(s.ID)
		}
	}
	return ids
}

// lastOnMac is the session last in front on the Mac, nil when unknown or
// when the session is gone.
func (a *API) lastOnMac(ctx context.Context) *LastOnMac {
	if a.LastOnMac == nil {
		return nil
	}
	id, at, ok := a.LastOnMac()
	if !ok {
		return nil
	}
	var err error
	switch {
	case a.SessionExists != nil:
		err = a.SessionExists(ctx, id)
	case a.Session != nil:
		_, err = a.Session(ctx, id, 1)
	}
	if err != nil {
		return nil
	}
	return &LastOnMac{ID: id, At: at}
}

// metaRequest is PATCH /v1/sessions/{id}/meta: each field is optional, an
// empty title or description clears the owner's value.
type metaRequest struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Pinned      *bool   `json:"pinned"`
}

// actions are the audit actions a request stands for.
func (q metaRequest) actions() []string {
	var out []string
	if q.Title != nil || q.Description != nil {
		out = append(out, devicelogin.ActionMetaEdit)
	}
	if q.Pinned != nil {
		action := devicelogin.ActionUnpin
		if *q.Pinned {
			action = devicelogin.ActionPin
		}
		out = append(out, action)
	}
	if len(out) == 0 {
		out = append(out, devicelogin.ActionMetaEdit)
	}
	return out
}

// ownerText trims an owner's title or description and checks it: at most
// limit characters, no control characters (one line). Errors never quote it.
func ownerText(field string, v *string, limit int) (string, error) {
	if v == nil {
		return "", nil
	}
	t := strings.TrimSpace(*v)
	if utf8.RuneCountInString(t) > limit {
		return "", fmt.Errorf("%s is longer than %d characters", field, limit)
	}
	if strings.IndexFunc(t, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%s holds control characters (it is one line)", field)
	}
	return t, nil
}

// patchMeta sets the owner's title and description and the pin, then
// audits one line per action (meta_edit, pin, unpin), never the text.
func (a *API) patchMeta(w http.ResponseWriter, r *http.Request) {
	var req metaRequest
	decodeErr := decodeStrict(w, r, &req)
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	a.editMeta(rec, r, req, decodeErr)
	if a.DeviceLogin == nil {
		return
	}
	result := "ok"
	if rec.status >= 400 {
		result = "http_" + strconv.Itoa(rec.status)
	}
	for _, action := range req.actions() {
		a.DeviceLogin.Record(r, action, r.PathValue("id"), result)
	}
}

func (a *API) editMeta(w http.ResponseWriter, r *http.Request, req metaRequest, decodeErr error) {
	if decodeErr != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with title, description or pinned, at most 64 KiB: %w", decodeErr))
		return
	}
	if req.Title == nil && req.Description == nil && req.Pinned == nil {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("give at least one of title, description and pinned"))
		return
	}
	title, err := ownerText("title", req.Title, maxTitleChars)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	desc, err := ownerText("description", req.Description, maxDescriptionChars)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if a.Record == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("editing sessions is not available"))
		return
	}
	s, err := a.Record(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	var saved history.Meta
	err = history.UpdateMeta(s.FilePath, func(m *history.Meta) {
		if req.Title != nil {
			setOwner(&m.Title, &m.TitleBy, title)
		}
		if req.Description != nil {
			setOwner(&m.Description, &m.DescriptionBy, desc)
		}
		if req.Pinned != nil {
			m.Starred = *req.Pinned
		}
		saved = *m
	})
	if err != nil {
		a.logf("meta edit: session %s: %v", s.ID, err)
		writeError(w, http.StatusInternalServerError, "internal", errors.New("the session's metadata could not be saved"))
		return
	}
	s.Title, s.Description, s.DescriptionBy, s.DescribedAt, s.Starred = saved.Title, saved.Description, saved.DescriptionBy, saved.DescribedAt, saved.Starred
	shown, _ := history.ShownDescription(s)
	writeJSON(w, http.StatusOK, MetaResult{Title: shownTitle(s), Description: shown, Pinned: s.Starred})
}

// setOwner stores an owner value; an empty one clears the field only when
// it is the owner's, so the shown value falls back to the model's or a
// free one.
func setOwner(value, by *string, v string) {
	switch {
	case v != "":
		*value, *by = v, "owner"
	case *by == "owner":
		*value, *by = "", ""
	}
}

// describe runs "Update summary" for a session now.
func (a *API) describe(w http.ResponseWriter, r *http.Request) {
	if !a.describing() {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("updating summaries is not available"))
		return
	}
	id := r.PathValue("id")
	s, err := a.Record(r.Context(), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	// A paid summary is written even when the client leaves meanwhile; the
	// backends bound the call (60 s each).
	res, err := a.Naming.UpdateSummary(context.WithoutCancel(r.Context()), s)
	if err != nil {
		a.writeNamingFailure(w, id, err)
		return
	}
	if fresh, err := a.Record(r.Context(), id); err == nil {
		s = fresh
	}
	shown, _ := history.ShownDescription(s)
	writeJSON(w, http.StatusOK, DescribeResult{Title: shownTitle(s), Description: shown,
		Changed: nonNil(res.Changed), Kept: nonNil(res.Kept)})
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// namingFailures are the client-facing answers per namer error.
var namingFailures = []struct {
	err    error
	status int
	code   string
	msg    string
}{
	{history.ErrOwnerSet, http.StatusConflict, "owner_set", "you wrote both the title and the description; edit them instead"},
	{history.ErrBusy, http.StatusConflict, "describe_running", "a summary for this session is already being made"},
	{history.ErrNoContent, http.StatusConflict, "no_content", "the session has no messages to summarise"},
	{history.ErrTimeout, http.StatusGatewayTimeout, "naming_timeout", "the model took too long; try again"},
	{history.ErrUnauthorized, http.StatusBadGateway, "naming_auth", "the model API rejected the API key on the Mac"},
	{history.ErrModelNotFound, http.StatusBadGateway, "naming_model", "the naming model was not found; check sessions.title_model on the Mac"},
	{history.ErrBadReply, http.StatusBadGateway, "naming_unreadable", "the model's reply could not be read; try again"},
	{history.ErrNoBackend, http.StatusServiceUnavailable, "naming_unavailable", "the Mac has no API key and no claude CLI to make summaries"},
	{history.ErrModelCall, http.StatusBadGateway, "naming_failed", "the model call failed; try again later"},
}

// writeNamingFailure answers a namer error with its fixed message; anything
// else (the metadata could not be saved) is a 500, its detail logged. The
// namer's errors carry no session text.
func (a *API) writeNamingFailure(w http.ResponseWriter, id string, err error) {
	for _, f := range namingFailures {
		if errors.Is(err, f.err) {
			writeJSON(w, f.status, ErrorBody{Error: f.msg, Code: f.code})
			return
		}
	}
	a.logf("describe: session %s: %v", id, err)
	writeError(w, http.StatusInternalServerError, "internal", errors.New("the summary could not be saved"))
}

func (a *API) logf(format string, args ...any) {
	if a.Log != nil {
		_, _ = fmt.Fprintf(a.Log, format+"\n", args...)
	}
}
