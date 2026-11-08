package remoteapi

import (
	"net/http"
	"slices"
	"testing"

	"github.com/zanetworker/aimux/internal/terminput"
)

func (r *queueRig) withTerminal() *fakeTerminal {
	term := &fakeTerminal{}
	r.api.TerminalInput, r.api.TerminalEnabled = term, true
	return term
}

// Send now takes the item out of the queue and types it with now; the
// other items keep their order.
func TestQueueAPI_SendNow(t *testing.T) {
	r := newQueueAPI(t)
	term := r.withTerminal()
	r.add("a", "")
	b := r.add("b", "")
	r.add("c", "")
	kicks := r.kicks.Load()
	st, body := r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", `{"key":"n1"}`)
	if st != 200 || queueTexts(body) != "a,c" {
		t.Fatalf("send now: %d %v", st, body)
	}
	if !slices.Equal(term.calls, []string{`now s1 "b" queue-` + b}) {
		t.Fatalf("calls %v", term.calls)
	}
	if r.kicks.Load() == kicks {
		t.Fatal("the dispatcher was not told")
	}
	// A retry with the same key answers the queue, typing nothing.
	st, body = r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", `{"key":"n1"}`)
	if st != 200 || queueTexts(body) != "a,c" || len(term.calls) != 1 {
		t.Fatalf("retry: %d %v, calls %v", st, body, term.calls)
	}
	// Without a key the body may be empty.
	c := r.store.Get("s1").Items[1].ID
	if st, body := r.do("POST", "/v1/sessions/s1/queue/"+c+"/send-now", ""); st != 200 || queueTexts(body) != "a" {
		t.Fatalf("no body: %d %v", st, body)
	}
}

// A refused Send now keeps the item at its place, free to edit, and answers
// the terminal's code; a session no terminal holds (started from the phone)
// is not_supported.
func TestQueueAPI_SendNowRefused(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{terminput.ErrNotAtInput, http.StatusConflict, "not_at_input"},
		{terminput.ErrDraftInBox, http.StatusConflict, "draft_in_box"},
		{terminput.ErrMacInUse, http.StatusConflict, "mac_in_use"},
		{terminput.ErrNotInTerminal, http.StatusConflict, "not_supported"},
	} {
		r := newQueueAPI(t)
		term := r.withTerminal()
		term.err = c.err
		r.add("a", "")
		b := r.add("b", "")
		r.add("c", "")
		st, body := r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", `{"key":"n1"}`)
		wantCode(t, st, body, c.status, c.code, c.code)
		if got := queueTexts(map[string]any{"items": items(r, "s1")}); got != "a,b,c" {
			t.Fatalf("%s: queue %q, want the item kept in place", c.code, got)
		}
		if st, _ := r.do("PATCH", "/v1/sessions/s1/queue/"+b, `{"text":"b2"}`); st != 200 {
			t.Fatalf("%s: edit after a refusal: %d", c.code, st)
		}
		// The refusal is not remembered: the same key tries again.
		term.err = nil
		if st, body := r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", `{"key":"n1"}`); st != 200 || queueTexts(body) != "a,c" {
			t.Fatalf("%s: retry: %d %v", c.code, st, body)
		}
	}
}

func items(r *queueRig, session string) []any {
	var out []any
	for _, it := range r.store.Get(session).Items {
		out = append(out, map[string]any{"text": it.Text})
	}
	return out
}

func TestQueueAPI_SendNowErrors(t *testing.T) {
	r := newQueueAPI(t)
	term := r.withTerminal()
	a := r.add("a", "")
	st, body := r.do("POST", "/v1/sessions/s1/queue/nope/send-now", "")
	wantCode(t, st, body, 404, "unknown_item", "unknown item")
	st, body = r.do("POST", "/v1/sessions/s2/queue/"+a+"/send-now", "")
	wantCode(t, st, body, 404, "unknown_session", "unknown session")
	st, body = r.do("POST", "/v1/sessions/s1/queue/"+a+"/send-now", `{"key":"k","text":"x"}`)
	wantCode(t, st, body, 400, "bad_request", "unknown field")
	if _, err := r.store.MarkSending("s1", a); err != nil {
		t.Fatal(err)
	}
	st, body = r.do("POST", "/v1/sessions/s1/queue/"+a+"/send-now", "")
	wantCode(t, st, body, 409, "item_sending", "item in flight")
	r.api.TerminalEnabled = false
	b := r.add("b", "")
	st, body = r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", "")
	wantCode(t, st, body, 403, "disabled", "terminal input off")
	if len(term.calls) != 0 {
		t.Fatalf("calls %v", term.calls)
	}
}

// The composer's Send now: now on a terminal message types with SendNow,
// also while the queue runs with items waiting or answers one; only an item
// being typed (sending) refuses it. now with replace is a bad request.
func TestQueueAPI_TerminalMessageNow(t *testing.T) {
	r := newQueueAPI(t)
	term := r.withTerminal()
	first := r.add("first", "")
	r.add("second", "")
	st, body := r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"stop, wrong file","key":"m1","now":true}`)
	if st != 202 || !slices.Equal(term.calls, []string{`now s1 "stop, wrong file" m1`}) {
		t.Fatalf("now with items waiting: %d %v, calls %v", st, body, term.calls)
	}
	if _, err := r.store.MarkSending("s1", first); err != nil {
		t.Fatal(err)
	}
	st, body = r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"x","now":true}`)
	wantCode(t, st, body, 409, "busy", "now while an item is typed")
	if err := r.store.MarkAnswering("s1", first); err != nil {
		t.Fatal(err)
	}
	if st, body := r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"y","now":true}`); st != 202 {
		t.Fatalf("now while an item is answered: %d %v", st, body)
	}
	st, body = r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"z","now":true,"replace":true}`)
	wantCode(t, st, body, 400, "bad_request", "now and replace")
	st, body = r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"plain"}`)
	wantCode(t, st, body, 409, "busy", "without now the queue still blocks")
	if len(term.calls) != 2 {
		t.Fatalf("calls %v", term.calls)
	}
}

// Health offers send_now only while terminal input is on: an older server
// would refuse the now field, and Send now types into a terminal.
func TestQueueAPI_SendNowCapability(t *testing.T) {
	r := newQueueAPI(t)
	has := func() bool {
		_, body := r.do("GET", "/v1/health", "")
		caps, _ := body["capabilities"].([]any)
		return slices.Contains(caps, any(CapSendNow))
	}
	if has() {
		t.Fatal("send_now without terminal input")
	}
	r.withTerminal()
	if !has() {
		t.Fatal("no send_now with terminal input on")
	}
}

// Every Send now is noted to the dispatcher (QueueSentNow) before it is
// typed, from the queue route and from a terminal message with now, so a
// history read right after Enter never takes its turn for the owner's; a
// refused one is forgotten again, and a plain message is never noted.
func TestQueueAPI_SendNowTellsTheDispatcher(t *testing.T) {
	r := newQueueAPI(t)
	term := r.withTerminal()
	var live []string
	var typedBefore []int // terminal calls already made when each note was taken
	r.api.QueueSentNow = func(session, text string) func() {
		note := session + ":" + text
		live = append(live, note)
		typedBefore = append(typedBefore, len(term.calls))
		return func() { live = slices.DeleteFunc(live, func(n string) bool { return n == note }) }
	}
	r.add("a", "")
	b := r.add("b", "")
	if st, body := r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", ""); st != 200 {
		t.Fatalf("send now: %d %v", st, body)
	}
	if st, body := r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"stop","now":true}`); st != 202 {
		t.Fatalf("terminal now: %d %v", st, body)
	}
	term.err = terminput.ErrMacInUse
	r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"refused","now":true}`)
	a := r.store.Get("s1").Items[0].ID
	r.do("POST", "/v1/sessions/s1/queue/"+a+"/send-now", "")
	term.err = nil
	if err := r.store.Delete("s1", a); err != nil {
		t.Fatal(err)
	}
	r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"plain"}`)
	if !slices.Equal(live, []string{"s1:b", "s1:stop"}) {
		t.Fatalf("live notes %v", live)
	}
	if !slices.Equal(typedBefore, []int{0, 1, 2, 3}) {
		t.Fatalf("calls before each note %v, want each Send now noted before it is typed", typedBefore)
	}
}

// Both Send now routes refuse alike while another message is being typed:
// a queued item the dispatcher types (sending), or another Send now's
// claim, is 409 busy and types nothing.
func TestQueueAPI_SendNowRoutesAgreeWhileTyping(t *testing.T) {
	r := newQueueAPI(t)
	term := r.withTerminal()
	notes := 0
	r.api.QueueSentNow = func(string, string) func() { notes++; return func() { notes-- } }
	a := r.add("a", "")
	b := r.add("b", "")
	if _, err := r.store.MarkSending("s1", a); err != nil {
		t.Fatal(err)
	}
	st, body := r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", "")
	wantCode(t, st, body, 409, "busy", "queue route while an item is typed")
	st, body = r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"x","now":true}`)
	wantCode(t, st, body, 409, "busy", "terminal route while an item is typed")
	if err := r.store.Release("s1", a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.ClaimNow("s1", a); err != nil {
		t.Fatal(err)
	}
	st, body = r.do("POST", "/v1/sessions/s1/queue/"+b+"/send-now", "")
	wantCode(t, st, body, 409, "busy", "queue route while another Send now types")
	st, body = r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"x","now":true}`)
	wantCode(t, st, body, 409, "busy", "terminal route while another Send now types")
	if len(term.calls) != 0 || notes != 0 {
		t.Fatalf("calls %v, %d notes left by refusals", term.calls, notes)
	}
}
