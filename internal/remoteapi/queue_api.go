package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// Client-facing messages for the queue's errors; never the store's own text.
var (
	errQueueOff     = errors.New("the chat queue is not available")
	errQueueFull    = fmt.Errorf("the queue holds %d messages, the most it can", chatqueue.MaxItems)
	errItemSending  = errors.New("the message is being sent; it cannot change now")
	errQueueBadText = errors.New("the text is too long, holds control characters, or starts with ! or / (bash mode, slash commands)")
	errUnknownItem  = errors.New("no such message in this session's queue")
)

// writeQueueFailure maps the store's errors to statuses and stable codes.
func writeQueueFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chatqueue.ErrFull):
		writeError(w, http.StatusConflict, "queue_full", errQueueFull)
	case errors.Is(err, chatqueue.ErrItemSending):
		writeError(w, http.StatusConflict, "item_sending", errItemSending)
	case errors.Is(err, chatqueue.ErrBadText):
		writeError(w, http.StatusConflict, "bad_text", errQueueBadText)
	case errors.Is(err, chatqueue.ErrNotFound):
		writeError(w, http.StatusNotFound, "unknown_item", errUnknownItem)
	default:
		writeFailure(w, err)
	}
}

// queueStatus is session's queue in its wire shape.
func (a *API) queueStatus(session string) QueueStatus {
	q := a.Queue.Get(session)
	out := QueueStatus{State: q.State, Reason: q.Reason, Items: make([]QueueItem, 0, len(q.Items))}
	if q.State == chatqueue.QueueRunning && len(q.Items) > 0 && a.QueueNote != nil {
		out.Note = a.QueueNote(session)
	}
	for _, it := range q.Items {
		out.Items = append(out.Items, queueItemOf(it))
	}
	return out
}

func queueItemOf(it chatqueue.Item) QueueItem {
	return QueueItem{ID: it.ID, Text: it.Text, State: it.State, Created: it.Created}
}

// queueSession answers false (and the response) when the queue is off or
// the session is unknown.
func (a *API) queueSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.Queue == nil {
		writeError(w, http.StatusNotFound, "not_supported", errQueueOff)
		return "", false
	}
	id := r.PathValue("id")
	var err error
	switch {
	case a.SessionExists != nil:
		err = a.SessionExists(r.Context(), id)
	case a.Session != nil:
		_, err = a.Session(r.Context(), id, 1)
	}
	if err != nil {
		writeFailure(w, err)
		return "", false
	}
	return id, true
}

// changed tells the dispatcher to look now and answers the queue.
func (a *API) changed(w http.ResponseWriter, session string) {
	if a.QueueKick != nil {
		a.QueueKick()
	}
	writeJSON(w, http.StatusOK, a.queueStatus(session))
}

func (a *API) queueGet(w http.ResponseWriter, r *http.Request) {
	session, ok := a.queueSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.queueStatus(session))
}

// queueAdd appends a message, idempotent by key; the answer is the item
// (state done for a retry of one already sent or deleted).
func (a *API) queueAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Key  string `json:"key"`
	}
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with a text field, at most 64 KiB: %w", err))
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("text is empty"))
		return
	}
	session, ok := a.queueSession(w, r)
	if !ok {
		return
	}
	it, err := a.Queue.Add(session, req.Text, req.Key)
	if err != nil {
		writeQueueFailure(w, err)
		return
	}
	if a.QueueKick != nil {
		a.QueueKick()
	}
	writeJSON(w, http.StatusOK, queueItemOf(it))
}

// queuePatch edits a message's text or moves it (exactly one of the two);
// position is 0-based among the messages not being sent. Audited as
// queue_move when it moves, else queue_edit.
func (a *API) queuePatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text     *string `json:"text"`
		Position *int    `json:"position"`
	}
	err := decodeStrict(w, r, &req)
	action := devicelogin.ActionQueueEdit
	if req.Position != nil {
		action = devicelogin.ActionQueueMove
	}
	a.audited(action, true, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with text or position, at most 64 KiB: %w", err))
			return
		case (req.Text == nil) == (req.Position == nil):
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("give exactly one of text and position"))
			return
		case req.Text != nil && strings.TrimSpace(*req.Text) == "":
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("text is empty"))
			return
		case req.Position != nil && *req.Position < 0:
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("position must be 0 or more"))
			return
		}
		session, ok := a.queueSession(w, r)
		if !ok {
			return
		}
		item := r.PathValue("item")
		var err error
		if req.Text != nil {
			err = a.Queue.Edit(session, item, *req.Text)
		} else {
			err = a.Queue.Move(session, item, *req.Position)
		}
		if err != nil {
			writeQueueFailure(w, err)
			return
		}
		a.changed(w, session)
	})(w, r)
}

// queueDelete removes a message; the session need not exist any more, so
// the owner can clear what a gone session left behind.
func (a *API) queueDelete(w http.ResponseWriter, r *http.Request) {
	if a.Queue == nil {
		writeError(w, http.StatusNotFound, "not_supported", errQueueOff)
		return
	}
	session := r.PathValue("id")
	if err := a.Queue.Delete(session, r.PathValue("item")); err != nil {
		writeQueueFailure(w, err)
		return
	}
	a.changed(w, session)
}

// queueResume starts a paused queue; only this does.
func (a *API) queueResume(w http.ResponseWriter, r *http.Request) {
	session, ok := a.queueSession(w, r)
	if !ok {
		return
	}
	if err := a.Queue.Resume(session); err != nil {
		writeQueueFailure(w, err)
		return
	}
	a.changed(w, session)
}

// QueueEvents publishes a queue event on a session's log after each change
// to its queue, until ctx ends or the store closes. Run it once (the
// store's Changes has one reader).
func (a *API) QueueEvents(ctx context.Context) {
	if a.Queue == nil {
		return
	}
	changes := a.Queue.Changes()
	for {
		select {
		case <-ctx.Done():
			return
		case session, open := <-changes:
			if !open {
				return
			}
			a.PublishQueue(session)
		}
	}
}

// PublishQueue publishes session's queue as a queue event: after a change,
// and when the dispatcher's waiting line changes.
func (a *API) PublishQueue(session string) {
	if a.Queue != nil {
		a.Broker.Publish(session, KindQueue, a.queueStatus(session))
	}
}
