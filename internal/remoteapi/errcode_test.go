package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/errcode"
)

// A turn that fails with a coded error ends with a turn_error carrying the
// code and the message for a person; the raw error stays in error.
func TestBroker_TurnErrorCarriesCodeAndMessage(t *testing.T) {
	f := &fakeBackend{sendF: func(context.Context, string, string, func(acprun.Event)) (string, error) {
		return "", fmt.Errorf("tool gateway: %w", errcode.Wrap(errcode.ToolGatewayNotRunning, errors.New("dial: no such file")))
	}}
	b := newBroker(t, f, time.Second)
	if _, err := b.Start("s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	evs := waitFor(t, b, "s1", KindTurnError)
	var d TurnErrorData
	if err := json.Unmarshal(evs[len(evs)-1].Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Code != errcode.ToolGatewayNotRunning || d.Message != errcode.Message(errcode.ToolGatewayNotRunning) || d.Error == "" {
		t.Fatalf("turn_error %+v, want code, message and the raw error", d)
	}

	// An uncoded failure has no code; the message is the error text.
	f.sendF = func(context.Context, string, string, func(acprun.Event)) (string, error) {
		return "", errors.New("adapter exited")
	}
	if _, err := b.Start("s2", "hi", "k2"); err != nil {
		t.Fatal(err)
	}
	evs = waitFor(t, b, "s2", KindTurnError)
	d = TurnErrorData{}
	_ = json.Unmarshal(evs[len(evs)-1].Data, &d)
	if d.Code != "" || d.Message != "adapter exited" {
		t.Fatalf("uncoded turn_error %+v", d)
	}
}

// A coded failure answers with its status and code, and the body's error
// is the message for a person (the cause is not shown).
func TestWriteFailure_CodedError(t *testing.T) {
	for code, status := range map[string]int{
		errcode.ToolGatewayNotRunning: http.StatusServiceUnavailable,
		errcode.GatewayBackendDown:    http.StatusServiceUnavailable,
		errcode.SandboxGatewayDown:    http.StatusServiceUnavailable,
		errcode.ProfileUnavailable:    http.StatusConflict,
	} {
		rec := httptest.NewRecorder()
		writeFailure(rec, fmt.Errorf("wrapped: %w", errcode.Wrap(code, errors.New("secret cause"))))
		var body ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != status || body.Code != code || body.Error != errcode.Message(code) {
			t.Errorf("%s: %d %+v, want %d with the message only", code, rec.Code, body, status)
		}
	}
}

// A coded turn_error carries only the message for a person: no openshell
// output, sandbox name or gateway URL from the cause, which goes to the
// serve log instead.
func TestBroker_CodedTurnErrorHidesTheCause(t *testing.T) {
	cause := errors.New("sandbox aimux-work-readonly: openshell -g local sandbox get: error: transport error: http://127.0.0.1:8080 Connection refused")
	f := &fakeBackend{sendF: func(context.Context, string, string, func(acprun.Event)) (string, error) {
		return "", errcode.Wrap(errcode.SandboxGatewayDown, cause)
	}}
	logged := make(chan string, 1)
	block := make(chan struct{}) // a log writer that never returns until the end
	defer close(block)
	var b *Broker
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: time.Second,
		Logf: func(format string, args ...any) { logged <- fmt.Sprintf(format, args...); <-block }})
	if _, err := b.Start("s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	evs := waitFor(t, b, "s1", KindTurnError)
	raw := string(evs[len(evs)-1].Data)
	for _, leak := range []string{"aimux-work-readonly", "sandbox get", "127.0.0.1:8080", "transport error"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("turn_error carries %q: %s", leak, raw)
		}
	}
	var d TurnErrorData
	_ = json.Unmarshal(evs[len(evs)-1].Data, &d)
	if d.Error != errcode.Message(errcode.SandboxGatewayDown) || d.Code != errcode.SandboxGatewayDown {
		t.Fatalf("turn_error %+v", d)
	}
	if line := <-logged; !strings.Contains(line, "aimux-work-readonly") {
		t.Fatalf("logged %q, want the cause", line)
	}
}

// A stalled log writer holds one line; the rest are dropped, not queued
// without bound, and turns still end.
func TestBroker_StalledLogIsBounded(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var b *Broker
	f := &fakeBackend{sendF: func(context.Context, string, string, func(acprun.Event)) (string, error) {
		return "", errcode.Wrap(errcode.SandboxGatewayDown, errors.New("cause"))
	}}
	b = NewBroker(f.deps(&b), BrokerOptions{ApprovalTimeout: time.Second, Logf: func(string, ...any) { <-block }})
	for i := 0; i < causeQueue+20; i++ {
		b.logCause("line")
	}
	if n := len(b.causes); n > causeQueue {
		t.Fatalf("%d lines queued, want at most %d", n, causeQueue)
	}
	if _, err := b.Start("s1", "hi", "k1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "s1", KindTurnError) // the turn still ends
}
