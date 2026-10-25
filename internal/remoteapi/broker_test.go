package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/zanetworker/aimux/internal/acprun"
)

type fakeBackend struct {
	sends   atomic.Int32
	checkF  func(session string) error
	sendF   func(ctx context.Context, session, text string, onEvent func(acprun.Event)) (string, error)
	cancels atomic.Int32
}

func (f *fakeBackend) deps(b **Broker) BrokerDeps {
	return BrokerDeps{
		Check: func(s string) error {
			if f.checkF != nil {
				return f.checkF(s)
			}
			return nil
		},
		Send: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
			f.sends.Add(1)
			if f.sendF != nil {
				return f.sendF(ctx, s, text, on)
			}
			on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`)})
			return "end_turn", nil
		},
		Cancel: func(string) error { f.cancels.Add(1); return nil },
	}
}

func newBroker(t *testing.T, f *fakeBackend, ttl time.Duration) *Broker {
	t.Helper()
	var b *Broker
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: ttl})
	return b
}

// waitFor returns the session's events once one of kind appears.
func waitFor(t *testing.T, b *Broker, session, kind string) []Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		evs := b.Events(session, 0)
		for _, e := range evs {
			if e.Kind == kind {
				return evs
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s event for %s; have %+v", kind, session, b.Events(session, 0))
	return nil
}

func TestBroker_TurnStreamsEventsAndEnds(t *testing.T) {
	f := &fakeBackend{}
	b := newBroker(t, f, time.Second)
	turn, err := b.Start("s1", "hello", "k1")
	if err != nil || turn == "" {
		t.Fatalf("Start: %q %v", turn, err)
	}
	evs := waitFor(t, b, "s1", KindTurnDone)
	kinds := []string{}
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		if e.Turn != turn {
			t.Fatalf("event %d belongs to turn %q, want %q", i, e.Turn, turn)
		}
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 3 || kinds[0] != KindTurnStarted || kinds[1] != acprun.KindUpdate || kinds[2] != KindTurnDone {
		t.Fatalf("kinds %v, want turn_started, update, turn_done", kinds)
	}
	var done struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(evs[2].Data, &done); err != nil || done.StopReason != "end_turn" {
		t.Fatalf("turn_done data %s", evs[2].Data)
	}
}

func TestBroker_SameKeyIsOneTurn(t *testing.T) {
	f := &fakeBackend{}
	b := newBroker(t, f, time.Second)
	a, err := b.Start("s1", "hello", "retry-key")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	again, err := b.Start("s1", "hello", "retry-key")
	if err != nil || again != a {
		t.Fatalf("retry returned %q, %v; want the original turn %q", again, err, a)
	}
	if f.sends.Load() != 1 {
		t.Fatalf("sent %d times for one key, want 1", f.sends.Load())
	}
}

func TestBroker_BusyWhileATurnRuns(t *testing.T) {
	release := make(chan struct{})
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		<-release
		return "end_turn", nil
	}}
	b := newBroker(t, f, time.Second)
	if _, err := b.Start("s1", "one", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Start("s1", "two", "k2"); !errors.Is(err, acprun.ErrBusy) {
		t.Fatalf("second turn: %v, want ErrBusy", err)
	}
	if _, err := b.Start("s2", "other session", "k3"); err != nil {
		t.Fatalf("another session must not be blocked: %v", err)
	}
	close(release)
	waitFor(t, b, "s1", KindTurnDone)
	if _, err := b.Start("s1", "three", "k4"); err != nil {
		t.Fatalf("after the turn ended: %v", err)
	}
}

func TestBroker_CheckErrorIsImmediate(t *testing.T) {
	f := &fakeBackend{checkF: func(string) error { return acprun.ErrOpenInTerminal }}
	b := newBroker(t, f, time.Second)
	if _, err := b.Start("s1", "hi", "k1"); !errors.Is(err, acprun.ErrOpenInTerminal) {
		t.Fatalf("got %v, want ErrOpenInTerminal", err)
	}
	if f.sends.Load() != 0 {
		t.Fatal("sent to a session that failed the check")
	}
}

func TestBroker_SendErrorEndsTurnWithError(t *testing.T) {
	f := &fakeBackend{sendF: func(context.Context, string, string, func(acprun.Event)) (string, error) {
		return "", errors.New("adapter exited")
	}}
	b := newBroker(t, f, time.Second)
	if _, err := b.Start("s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	evs := waitFor(t, b, "s1", KindTurnError)
	if string(evs[len(evs)-1].Data) == "" {
		t.Fatal("turn_error carries no message")
	}
}

func permissionRequest() acp.RequestPermissionRequest {
	return acp.RequestPermissionRequest{
		SessionId: "s1",
		ToolCall:  acp.ToolCallUpdate{ToolCallId: "c1", Title: acp.Ptr("git push")},
		Options: []acp.PermissionOption{
			{OptionId: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
		},
	}
}

// approvalFlow runs a turn whose agent asks for permission and returns what
// the agent was told and the broker's events.
func approvalFlow(t *testing.T, ttl time.Duration, answer func(b *Broker, approvalID string)) (string, bool, []Event) {
	t.Helper()
	var gotID string
	var gotOK bool
	var b *Broker
	f := &fakeBackend{}
	f.sendF = func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		gotID, gotOK = b.Approver()(ctx, permissionRequest())
		return "end_turn", nil
	}
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: ttl})
	if _, err := b.Start("s1", "push it", "k1"); err != nil {
		t.Fatal(err)
	}
	evs := waitFor(t, b, "s1", KindApproval)
	var ap struct {
		ApprovalID string `json:"approvalId"`
	}
	for _, e := range evs {
		if e.Kind == KindApproval {
			_ = json.Unmarshal(e.Data, &ap)
		}
	}
	if ap.ApprovalID == "" {
		t.Fatal("approval event has no approvalId")
	}
	if answer != nil {
		answer(b, ap.ApprovalID)
	}
	return gotID, gotOK, waitFor(t, b, "s1", KindTurnDone)
}

func TestBroker_ApprovalAllowed(t *testing.T) {
	id, ok, evs := approvalFlow(t, 5*time.Second, func(b *Broker, approval string) {
		if err := b.Answer(approval, "allow"); err != nil {
			t.Fatal(err)
		}
		if err := b.Answer(approval, "allow"); !errors.Is(err, ErrNoSuchApproval) {
			t.Fatalf("second answer: %v, want ErrNoSuchApproval", err)
		}
	})
	if !ok || id != "allow" {
		t.Fatalf("agent got %q,%v; want allow", id, ok)
	}
	if !hasResolution(evs, "allowed") {
		t.Fatalf("no approval_resolved=allowed event in %v", kindsOf(evs))
	}
}

func TestBroker_ApprovalDenied(t *testing.T) {
	id, ok, evs := approvalFlow(t, 5*time.Second, func(b *Broker, approval string) {
		if err := b.Answer(approval, ""); err != nil {
			t.Fatal(err)
		}
	})
	if ok || id != "" {
		t.Fatalf("agent got %q,%v; want a denial", id, ok)
	}
	if !hasResolution(evs, "denied") {
		t.Fatalf("no approval_resolved=denied event in %v", kindsOf(evs))
	}
}

func TestBroker_ApprovalExpiresAsDenied(t *testing.T) {
	id, ok, evs := approvalFlow(t, 50*time.Millisecond, nil)
	if ok || id != "" {
		t.Fatalf("agent got %q,%v; want a denial after expiry", id, ok)
	}
	if !hasResolution(evs, "expired") {
		t.Fatalf("no approval_resolved=expired event in %v", kindsOf(evs))
	}
}

func TestBroker_UnknownOptionIsRejected(t *testing.T) {
	_, _, _ = approvalFlow(t, 5*time.Second, func(b *Broker, approval string) {
		if err := b.Answer(approval, "allow_everything"); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("got %v, want ErrInvalidOption", err)
		}
		_ = b.Answer(approval, "")
	})
}

func TestBroker_SubscribeReplaysThenFollows(t *testing.T) {
	release := make(chan struct{})
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(`{"n":1}`)})
		<-release
		on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(`{"n":2}`)})
		return "end_turn", nil
	}}
	b := newBroker(t, f, time.Second)
	if _, err := b.Start("s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", acprun.KindUpdate)
	ch, stop := b.Subscribe("s1", 1) // skip turn_started
	defer stop()
	first := <-ch
	if first.Seq != 2 || first.Kind != acprun.KindUpdate {
		t.Fatalf("replayed %+v, want seq 2 update", first)
	}
	close(release)
	next := <-ch
	last := <-ch
	if next.Seq != 3 || last.Kind != KindTurnDone {
		t.Fatalf("followed %+v then %+v, want seq 3 update then turn_done", next, last)
	}
}

func TestBroker_Cancel(t *testing.T) {
	release := make(chan struct{})
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		select {
		case <-release:
			return "end_turn", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	b := newBroker(t, f, time.Second)
	if err := b.Cancel("s1"); !errors.Is(err, acprun.ErrNotRunning) || f.cancels.Load() != 0 {
		t.Fatalf("cancel with no turn: %v (adapter cancels %d), want ErrNotRunning and the adapter untouched", err, f.cancels.Load())
	}
	if _, err := b.Start("s1", "work", "k"); err != nil {
		t.Fatal(err)
	}
	if err := b.Cancel("s1"); err != nil {
		t.Fatal(err)
	}
	if f.cancels.Load() != 1 {
		t.Fatal("graceful cancel not forwarded to the agent")
	}
	evs := waitFor(t, b, "s1", KindTurnDone)
	var done struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(evs[len(evs)-1].Data, &done)
	if done.StopReason != "cancelled" {
		t.Fatalf("stop reason %q, want cancelled", done.StopReason)
	}
	close(release)
}

func hasResolution(evs []Event, outcome string) bool {
	for _, e := range evs {
		if e.Kind != KindApprovalResolved {
			continue
		}
		var r struct {
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(e.Data, &r) == nil && r.Outcome == outcome {
			return true
		}
	}
	return false
}

func kindsOf(evs []Event) []string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func TestBroker_DropsAvailableCommandsUpdates(t *testing.T) {
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"x"}]}`)})
		on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`)})
		return "end_turn", nil
	}}
	b := newBroker(t, f, time.Second)
	if _, err := b.Start("s1", "hi", "k"); err != nil {
		t.Fatal(err)
	}
	for _, e := range waitFor(t, b, "s1", KindTurnDone) {
		if strings.Contains(string(e.Data), "available_commands_update") {
			t.Fatal("the slash-command list was passed to clients")
		}
	}
}

func TestBroker_EventsEmptyIsNotNil(t *testing.T) {
	b := newBroker(t, &fakeBackend{}, time.Second)
	if evs := b.Events("never-used", 0); evs == nil {
		t.Fatal("Events returned nil; JSON clients get null instead of []")
	}
}

// Two sends with the same key race: the second is still in Check while the
// first turn runs and finishes. The message must run once.
func TestBroker_SameKeyRacingThroughCheckRunsOnce(t *testing.T) {
	gate := make(chan struct{})
	var checks atomic.Int32
	f := &fakeBackend{checkF: func(string) error {
		if checks.Add(1) == 1 {
			<-gate // the first caller is slow in Check
		}
		return nil
	}}
	b := newBroker(t, f, time.Second)
	slow := make(chan string)
	go func() { id, _ := b.Start("s1", "pay the invoice", "same-key"); slow <- id }()
	for checks.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	fast, err := b.Start("s1", "pay the invoice", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnDone)
	close(gate)
	if got := <-slow; got != fast {
		t.Fatalf("racing retry got turn %q, want the existing turn %q", got, fast)
	}
	time.Sleep(50 * time.Millisecond)
	if f.sends.Load() != 1 {
		t.Fatalf("the message ran %d times for one key", f.sends.Load())
	}
}

// Cancel arrives right after the 202, while the turn is still before the
// manager (looking up the session): the turn must not start anyway.
func TestBroker_CancelBeforeTheRunExists(t *testing.T) {
	sawCancelled := make(chan bool, 1)
	f := &fakeBackend{}
	f.sendF = func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		<-ctx.Done() // the run never registers in time; only the turn context can stop it
		sawCancelled <- true
		return "", ctx.Err()
	}
	b := NewBroker(BrokerDeps{
		Check: func(string) error { return nil },
		Send: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
			f.sends.Add(1)
			return f.sendF(ctx, s, text, on)
		},
		Cancel: func(string) error { return acprun.ErrNotRunning },
	}, BrokerOptions{})
	if _, err := b.Start("s1", "pay the invoice", "k"); err != nil {
		t.Fatal(err)
	}
	if err := b.Cancel("s1"); err != nil {
		t.Fatalf("cancel of an accepted turn: %v", err)
	}
	select {
	case <-sawCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the accepted turn kept going after Cancel")
	}
	waitFor(t, b, "s1", KindTurnDone)
	if b.Busy("s1") {
		t.Fatal("session still busy after the cancelled turn ended")
	}
}

func TestBroker_CancelWithNoTurn(t *testing.T) {
	b := NewBroker(BrokerDeps{Cancel: func(string) error { return acprun.ErrNotRunning }}, BrokerOptions{})
	if err := b.Cancel("s1"); !errors.Is(err, acprun.ErrNotRunning) {
		t.Fatalf("got %v, want ErrNotRunning", err)
	}
}

// The adapter ends its turn (for example after a cancel) without withdrawing
// a permission request. That approval must stop being answerable.
func TestBroker_ApprovalExpiresWhenItsTurnEnds(t *testing.T) {
	var b *Broker
	decided := make(chan bool, 1)
	f := &fakeBackend{}
	f.sendF = func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		go func() { // #nosec G118 -- on purpose: the permission request outlives the prompt
			_, ok := b.Approver()(context.Background(), permissionRequest())
			decided <- ok
		}()
		for len(b.Events(s, 0)) < 2 { // wait until the approval is posted
			time.Sleep(time.Millisecond)
		}
		return "cancelled", nil
	}
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: time.Hour})
	if _, err := b.Start("s1", "push", "k"); err != nil {
		t.Fatal(err)
	}
	evs := waitFor(t, b, "s1", KindTurnDone)
	var ap struct {
		ApprovalID string `json:"approvalId"`
	}
	for _, e := range evs {
		if e.Kind == KindApproval {
			_ = json.Unmarshal(e.Data, &ap)
		}
	}
	select {
	case ok := <-decided:
		if ok {
			t.Fatal("the stale approval was granted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the approval stayed pending after its turn ended")
	}
	if err := b.Answer(ap.ApprovalID, "allow"); !errors.Is(err, ErrNoSuchApproval) {
		t.Fatalf("answering an approval of a finished turn: %v, want ErrNoSuchApproval", err)
	}
	if !hasResolution(waitFor(t, b, "s1", KindApprovalResolved), "expired") {
		t.Fatal("no approval_resolved=expired event for the finished turn")
	}
}

// While Cancel is signalling the agent, no new turn may start: otherwise the
// agent cancel, meant for the old turn, would stop the new one.
func TestBroker_NoNewTurnWhileCancelling(t *testing.T) {
	inCancel := make(chan struct{})
	release := make(chan struct{})
	b := NewBroker(BrokerDeps{
		Check: func(string) error { return nil },
		Send: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
			if text == "first" {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "end_turn", nil
		},
		Cancel: func(string) error { close(inCancel); <-release; return nil },
	}, BrokerOptions{})
	if _, err := b.Start("s1", "first", "k1"); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Cancel("s1") }()
	<-inCancel
	started := make(chan error, 1)
	go func() {
		for {
			if _, err := b.Start("s1", "second", "k2"); !errors.Is(err, acprun.ErrBusy) {
				started <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case err := <-started:
		t.Fatalf("a new turn started (%v) while the old one's cancel was in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatalf("the next turn after the cancel: %v", err)
	}
}

// A cancel stuck on a wedged adapter must not freeze the broker: other
// sessions keep streaming, listing and starting turns.
func TestBroker_WedgedCancelDoesNotBlockOtherSessions(t *testing.T) {
	inCancel := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	b := NewBroker(BrokerDeps{
		Check: func(string) error { return nil },
		Send: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
			if s == "s1" {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "end_turn", nil
		},
		Cancel: func(string) error { close(inCancel); <-release; return nil },
	}, BrokerOptions{})
	if _, err := b.Start("s1", "first", "k1"); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Cancel("s1") }()
	<-inCancel
	done := make(chan error, 1)
	go func() {
		_ = b.Busy("s2")
		_ = b.Events("s1", 0)
		_, err := b.Start("s2", "hello", "k2")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start on another session: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker frozen while one session's cancel is stuck")
	}
}
