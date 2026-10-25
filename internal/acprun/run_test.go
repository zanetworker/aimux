package acprun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"golang.org/x/sys/unix"
)

const (
	fakeAgentEnv  = "AIMUX_ACPRUN_FAKE_AGENT"
	fakeNoLoadEnv = "AIMUX_ACPRUN_FAKE_NOLOAD"
)

// TestMain lets the test binary act as an ACP agent, so the tests drive a
// real process over the real protocol.
func TestMain(m *testing.M) {
	if os.Getenv(fakeAgentEnv) == "1" {
		runFakeAgent()
		return
	}
	os.Exit(m.Run())
}

type fakeAgent struct {
	conn      *acp.AgentSideConnection
	mu        sync.Mutex
	cancelled chan struct{}
}

func runFakeAgent() {
	a := &fakeAgent{}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	<-a.conn.Done()
}

func (a *fakeAgent) Initialize(ctx context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{LoadSession: os.Getenv(fakeNoLoadEnv) != "1"},
	}, nil
}

func (a *fakeAgent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if p.SessionId != "sess-1" {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown session " + string(p.SessionId)})
	}
	// Replayed history, which the runner must not report as new events.
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateUserMessageText("old turn")})
	return acp.LoadSessionResponse{}, nil
}

func (a *fakeAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	text := ""
	if len(p.Prompt) > 0 && p.Prompt[0].Text != nil {
		text = p.Prompt[0].Text.Text
	}
	say := func(s string) {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText(s)})
	}
	switch text {
	case "ask":
		resp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: p.SessionId,
			ToolCall:  acp.ToolCallUpdate{ToolCallId: "call-1", Title: acp.Ptr("git push")},
			Options: []acp.PermissionOption{
				{OptionId: "allow", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
			},
		})
		switch {
		case err != nil:
			say("permission: error")
		case resp.Outcome.Selected != nil:
			say("permission: " + string(resp.Outcome.Selected.OptionId))
		default:
			say("permission: cancelled")
		}
	case "slow":
		// Like a real agent, take a moment before the turn is cancellable; a
		// cancel arriving in that gap finds no turn and is ignored.
		time.Sleep(100 * time.Millisecond)
		a.mu.Lock()
		a.cancelled = make(chan struct{})
		ch := a.cancelled
		a.mu.Unlock()
		select {
		case <-ch:
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		case <-time.After(20 * time.Second):
		}
	case "crash":
		os.Exit(5)
	case "flood":
		for i := 0; i < 1500; i++ {
			say("x")
		}
	default:
		say("echo: ")
		say(text)
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *fakeAgent) Cancel(ctx context.Context, p acp.CancelNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelled != nil {
		close(a.cancelled)
		a.cancelled = nil
	}
	return nil
}

func (a *fakeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (a *fakeAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (a *fakeAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (a *fakeAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (a *fakeAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{}, errors.New("not supported by the fake agent")
}
func (a *fakeAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, errors.New("not supported by the fake agent")
}
func (a *fakeAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
func (a *fakeAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func fakeSpec(t *testing.T, extraEnv ...string) Spec {
	t.Helper()
	return Spec{Command: os.Args[0], Cwd: t.TempDir(), Env: append([]string{fakeAgentEnv + "=1"}, extraEnv...)}
}

func start(t *testing.T, spec Spec, session string, approve Approver) *Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := Start(ctx, spec, session, approve)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// messageText joins the agent_message_chunk texts in events.
func messageText(t *testing.T, events []Event) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		if e.Kind != KindUpdate {
			continue
		}
		var u struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(e.Data, &u); err != nil {
			t.Fatalf("event %d: %v", e.Seq, err)
		}
		if u.SessionUpdate == "agent_message_chunk" {
			b.WriteString(u.Content.Text)
		}
	}
	return b.String()
}

func prompt(t *testing.T, r *Run, text string) ([]Event, string) {
	t.Helper()
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stop, err := r.Prompt(ctx, text, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("Prompt(%q): %v", text, err)
	}
	return events, stop
}

func TestRun_LoadsSessionAndStreamsReply(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	events, stop := prompt(t, r, "hello")
	if stop != "end_turn" {
		t.Fatalf("stop reason %q, want end_turn", stop)
	}
	if got := messageText(t, events); got != "echo: hello" {
		t.Fatalf("reply %q, want %q", got, "echo: hello")
	}
	if strings.Contains(string(mustJSON(t, events)), "old turn") {
		t.Fatal("history replayed by session/load was reported as a new event")
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestStart_UnknownSessionFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := Start(ctx, fakeSpec(t), "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v, want an error naming the session", err)
	}
}

func TestStart_AgentWithoutLoadSessionFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := Start(ctx, fakeSpec(t, fakeNoLoadEnv+"=1"), "sess-1", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot load") {
		t.Fatalf("got %v, want an error saying the agent cannot load sessions", err)
	}
}

func TestRun_PermissionApproved(t *testing.T) {
	var asked acp.RequestPermissionRequest
	approve := func(ctx context.Context, req acp.RequestPermissionRequest) (string, bool) {
		asked = req
		return "allow", true
	}
	r := start(t, fakeSpec(t), "sess-1", approve)
	events, _ := prompt(t, r, "ask")
	if got := messageText(t, events); got != "permission: allow" {
		t.Fatalf("agent saw %q, want permission: allow", got)
	}
	if asked.ToolCall.Title == nil || *asked.ToolCall.Title != "git push" {
		t.Fatalf("approver got %+v", asked.ToolCall)
	}
	if !hasKind(events, KindPermission) {
		t.Fatal("no permission event was emitted for the client")
	}
}

func TestRun_PermissionDeniedPicksReject(t *testing.T) {
	approve := func(context.Context, acp.RequestPermissionRequest) (string, bool) { return "", false }
	r := start(t, fakeSpec(t), "sess-1", approve)
	events, _ := prompt(t, r, "ask")
	if got := messageText(t, events); got != "permission: reject" {
		t.Fatalf("agent saw %q, want permission: reject", got)
	}
}

func TestRun_NoApproverDenies(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	events, _ := prompt(t, r, "ask")
	if got := messageText(t, events); got != "permission: reject" {
		t.Fatalf("agent saw %q, want permission: reject", got)
	}
}

func TestRun_UnknownOptionFromApproverIsDenied(t *testing.T) {
	approve := func(context.Context, acp.RequestPermissionRequest) (string, bool) { return "allow_everything", true }
	r := start(t, fakeSpec(t), "sess-1", approve)
	events, _ := prompt(t, r, "ask")
	if got := messageText(t, events); got != "permission: reject" {
		t.Fatalf("agent saw %q, want permission: reject for an option it never offered", got)
	}
}

func TestRun_Cancel(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	type res struct {
		stop string
		err  error
	}
	done := make(chan res)
	go func() {
		stop, err := r.Prompt(context.Background(), "slow", func(Event) {})
		done <- res{stop, err}
	}()
	time.Sleep(200 * time.Millisecond)
	r.Cancel()
	select {
	case got := <-done:
		if got.err != nil || got.stop != "cancelled" {
			t.Fatalf("got %q, %v; want cancelled", got.stop, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return after Cancel")
	}
}

func TestRun_AgentCrashIsAnError(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := r.Prompt(ctx, "crash", func(Event) {}); err == nil {
		t.Fatal("Prompt on a crashed agent returned no error")
	}
}

func TestRun_EventsReplayAfterSeq(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	first, _ := prompt(t, r, "one")
	second, _ := prompt(t, r, "two")
	last := first[len(first)-1].Seq
	replay := r.Events(last)
	if len(replay) != len(second) || replay[0].Seq != last+1 {
		t.Fatalf("Events(%d) returned %d events starting at %d, want the %d from the second prompt", last, len(replay), replay[0].Seq, len(second))
	}
	if got := messageText(t, replay); got != "echo: two" {
		t.Fatalf("replay text %q", got)
	}
	if all := r.Events(0); len(all) != len(first)+len(second) {
		t.Fatalf("Events(0) returned %d, want all %d", len(all), len(first)+len(second))
	}
}

func TestRun_CloseStopsTheAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := Start(ctx, fakeSpec(t), "sess-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	pid := r.PID()
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("agent %d still running after Close", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := r.Prompt(ctx, "hello", func(Event) {}); err == nil {
		t.Fatal("Prompt after Close succeeded")
	}
}

func hasKind(events []Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdapterEnv(t *testing.T) {
	got := adapterEnv([]string{"PATH=/bin", "CLAUDECODE=1", "HOME=/h"}, []string{"EXTRA=1"})
	want := map[string]bool{"PATH=/bin": true, "HOME=/h": true, "EXTRA=1": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Fatalf("unexpected %q in %v (CLAUDECODE must be dropped so Claude does not refuse to start nested)", kv, got)
		}
	}
}

func TestStart_AdapterHasNoControllingTerminal(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	sid, err := unix.Getsid(r.PID())
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := unix.Getsid(0)
	// Its own session: it cannot open /dev/tty and read keystrokes meant for
	// aimux's approval prompt.
	if sid != r.PID() || sid == mine {
		t.Fatalf("adapter session %d (pid %d, aimux session %d); want the adapter to lead its own session", sid, r.PID(), mine)
	}
}

func TestRun_SDKDoesNotLogToTheDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	r := start(t, fakeSpec(t), "sess-1", nil)
	prompt(t, r, "hello")
	_ = r.Close()
	time.Sleep(100 * time.Millisecond) // let the connection notice the closed peer
	if buf.Len() != 0 {
		t.Fatalf("the ACP SDK logged to the default logger, which ends up in the user's output:\n%s", buf.String())
	}
}

func TestRun_EventHistoryIsBounded(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	events, _ := prompt(t, r, "flood")
	if len(events) != 1500 {
		t.Fatalf("callback got %d events, want all 1500 streamed", len(events))
	}
	kept := r.Events(0)
	if len(kept) != maxRunEvents {
		t.Fatalf("kept %d events, want the last %d", len(kept), maxRunEvents)
	}
	if kept[len(kept)-1].Seq != 1500 || kept[0].Seq != int64(1500-maxRunEvents+1) {
		t.Fatalf("kept seqs %d..%d, want the newest", kept[0].Seq, kept[len(kept)-1].Seq)
	}
}

// Cancel that arrives before Prompt sends anything ends that prompt at once.
func TestRun_CancelBeforePromptEndsIt(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	r.Cancel()
	events, stop := prompt(t, r, "hello")
	if stop != "cancelled" || len(events) != 0 {
		t.Fatalf("stop %q with %d events, want cancelled and nothing sent", stop, len(events))
	}
	if _, stop := prompt(t, r, "hello"); stop != "end_turn" {
		t.Fatalf("next prompt %q: the pending cancel must apply only once", stop)
	}
}

// Cancel sent in the instant the prompt is being dispatched reaches the
// agent before it has a turn and is ignored; the turn must still stop.
func TestRun_CancelDuringDispatchStillStopsTheTurn(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	done := make(chan string, 1)
	go func() {
		stop, _ := r.Prompt(context.Background(), "slow", func(Event) {})
		done <- stop
	}()
	for {
		r.mu.Lock()
		p := r.prompting
		r.mu.Unlock()
		if p {
			break
		}
		time.Sleep(50 * time.Microsecond)
	}
	r.Cancel() // the agent has not registered the "slow" turn yet
	select {
	case stop := <-done:
		if stop != "cancelled" {
			t.Fatalf("stop %q, want cancelled", stop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancel during dispatch was lost; the turn kept running")
	}
}

// A re-sent cancel names only the session, so it must never go out once the
// prompt it was for has ended and the next one has started.
func TestRun_CancelRetryNeverReachesTheNextPrompt(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	r.mu.Lock()
	r.prompting = true // a prompt the agent ignores the first cancel for
	r.promptGen++
	r.mu.Unlock()
	done := make(chan string, 1)
	var once sync.Once
	r.beforeCancelRetry = func() {
		once.Do(func() {
			r.mu.Lock()
			r.prompting = false // that prompt ends...
			r.mu.Unlock()
			go func() { // ...and the next turn starts while the retry is in flight
				stop, _ := r.Prompt(context.Background(), "slow", func(Event) {})
				done <- stop
			}()
			time.Sleep(300 * time.Millisecond)
		})
	}
	r.Cancel()
	select {
	case stop := <-done:
		t.Fatalf("next prompt ended %q: a retried cancel for the earlier prompt stopped it", stop)
	case <-time.After(1500 * time.Millisecond):
	}
	r.Cancel()
	<-done
}
