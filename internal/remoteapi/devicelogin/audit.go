package devicelogin

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Audited actions: one line each for every action that changes something.
const (
	ActionPair            = "pair"
	ActionPairFailed      = "pair_failed"
	ActionLogin           = "login"
	ActionLoginFailed     = "login_failed"
	ActionLogout          = "logout"
	ActionRevoke          = "revoke"
	ActionSend            = "send"
	ActionNewSession      = "new_session"
	ActionCancel          = "cancel"
	ActionApprove         = "approve"
	ActionTerminalMessage = "terminal_message"
	ActionTerminalKey     = "terminal_key"
	// The chat queue's dispatcher: an item sent, a queue paused. Result is
	// a code, never the text or the reason line.
	ActionQueueSend   = "queue_send"
	ActionQueuePaused = "queue_paused"
	// The owner's changes to a queue through the API.
	ActionQueueAdd    = "queue_add"
	ActionQueueEdit   = "queue_edit"
	ActionQueueMove   = "queue_move"
	ActionQueueDelete = "queue_delete"
	ActionQueueResume = "queue_resume"
	// Session titles, descriptions and pins.
	ActionDescribe = "describe"
	ActionMetaEdit = "meta_edit"
	ActionPin      = "pin"
	ActionUnpin    = "unpin"
)

// AuditEntry is one audit line. Device is the device's name; Session is the
// aimux session acted on, never a login token. No field ever holds message
// text, a code, a challenge, a token or a key.
type AuditEntry struct {
	Time    time.Time `json:"time"`
	Login   string    `json:"login"`
	Device  string    `json:"device"`
	Action  string    `json:"action"`
	Session string    `json:"session,omitempty"`
	Result  string    `json:"result"`
	// Count, on a failure line, is how many identical failures it stands
	// for: those left out in the minute since the last such line, plus this.
	Count int `json:"count,omitempty"`
}

// collapseEvery: identical failures (same login, device, action and result) are
// written at most once in this long.
const collapseEvery = time.Minute

type failureKey struct{ login, device, action, result string }

type failureRun struct {
	written time.Time
	skipped int
}

// Audit writes AuditEntry values as JSON lines. A nil *Audit writes nothing.
type Audit struct {
	w    io.Writer
	now  func() time.Time
	errs io.Writer // told when a line cannot be written; may be nil
	mu   sync.Mutex
	runs map[failureKey]*failureRun // bounded: allowed logins x paired device names x fixed results
}

// NewAudit writes to w (the 0600 ~/.aimux/logs/remote-audit.log), stamping
// lines with now, and reports write failures to errs.
func NewAudit(w io.Writer, now func() time.Time, errs io.Writer) *Audit {
	return &Audit{w: w, now: now, errs: errs, runs: map[failureKey]*failureRun{}}
}

// Write appends e, setting its time. A failure identical to one written
// less than a minute ago is counted instead, and the count goes on the next
// such line.
func (a *Audit) Write(e AuditEntry) {
	if a == nil {
		return
	}
	e.Time = a.now().UTC()
	a.mu.Lock()
	if strings.HasSuffix(e.Action, "_failed") {
		k := failureKey{e.Login, e.Device, e.Action, e.Result}
		run := a.runs[k]
		if run != nil && e.Time.Sub(run.written) < collapseEvery {
			run.skipped++
			a.mu.Unlock()
			return
		}
		if run != nil && run.skipped > 0 {
			e.Count = run.skipped + 1
		}
		a.runs[k] = &failureRun{written: e.Time}
	}
	b, err := json.Marshal(e)
	if err == nil {
		_, err = a.w.Write(append(b, '\n')) // one write per line, so lines never interleave
	}
	a.mu.Unlock()
	if err != nil && a.errs != nil {
		_, _ = fmt.Fprintf(a.errs, "audit log: cannot write a %s line: %v\n", e.Action, err)
	}
}
