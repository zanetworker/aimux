package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
