package profilerun

import (
	"context"
	"errors"
	"testing"

	"github.com/zanetworker/aimux/internal/errcode"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	e, ok := errcode.Of(err)
	if !ok || e.Code != code {
		t.Fatalf("error %v: code %v, want %s", err, e, code)
	}
}

func TestSandboxTurn_GatewayFailuresAreCoded(t *testing.T) {
	e, _ := newLeaseSandbox(t, &fakeLeases{putErr: toolgateway.ErrGatewayNotRunning})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, _, err := sendCollect(t, e, sid, "q")
	wantCode(t, err, errcode.ToolGatewayNotRunning)

	e, _ = newLeaseSandbox(t, &fakeLeases{putErr: errors.New("tool gateway control: PUT /v1/leases/x: 500 refresh failed")})
	sid, _ = e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, _, err = sendCollect(t, e, sid, "q")
	wantCode(t, err, errcode.GatewayBackendDown)
}

func TestSandbox_ProfileThatDoesNotCompileIsCoded(t *testing.T) {
	e, _ := newSandbox(t)
	_, err := e.New(context.Background(), Meta{Profile: "broken"}, "")
	wantCode(t, err, errcode.ProfileUnavailable)
}

// downBackend is an OpenShell CLI that cannot reach its gateway.
type downBackend struct{ sbBackend }

func (*downBackend) Ensure(context.Context, profile.Compiled) error {
	return errors.New("openshell sandbox get aimux-work-readonly: exit status 1: error: transport error: tcp connect error: Connection refused (os error 61)")
}

func TestSandbox_OpenShellGatewayDownIsCoded(t *testing.T) {
	e, _ := newSandbox(t)
	e.L = &sandboxlaunch.Launcher{Backend: &downBackend{}}
	e.GatewayCheck = func(context.Context) error { return errors.New("openshell -g local status: exit status 1") }
	_, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	wantCode(t, err, errcode.SandboxGatewayDown)

	// The same "Connection refused" text with the gateway check passing
	// (an MCP server refusing, say) is not the OpenShell gateway.
	e.GatewayCheck = func(context.Context) error { return nil }
	_, err = e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if _, ok := errcode.Of(err); ok || err == nil {
		t.Fatalf("error %v, want an uncoded failure", err)
	}
	// No check wired: never coded from text.
	e.GatewayCheck = nil
	if _, err = e.New(context.Background(), Meta{Profile: "work-readonly"}, ""); err == nil {
		t.Fatal("want a failure")
	} else if _, ok := errcode.Of(err); ok {
		t.Fatalf("coded without a gateway check: %v", err)
	}
}

// openshell can start and only fail when its exec ends: when the gateway
// check then fails, the turn is coded the same way.
func TestSandboxTurn_OpenShellGatewayDownAtExitIsCoded(t *testing.T) {
	e, b := newSandbox(t)
	e.GatewayCheck = func(context.Context) error { return errors.New("down") }
	b.script = func(string) (string, error, error) {
		return "", errors.New("exit status 1: error: transport error: tcp connect error: Connection refused"), nil
	}
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, _, err := sendCollect(t, e, sid, "q")
	wantCode(t, err, errcode.SandboxGatewayDown)
}

// A sandbox step that failed because the turn was cancelled is not probed:
// no gateway check, no code.
func TestSandbox_CancelledFailureIsNotProbed(t *testing.T) {
	e, _ := newSandbox(t)
	probed := false
	e.GatewayCheck = func(context.Context) error { probed = true; return errors.New("down") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.sandboxFailure(ctx, errors.New("openshell: context canceled"))
	if probed {
		t.Fatal("the gateway was probed for a cancelled step")
	}
	if _, ok := errcode.Of(err); ok {
		t.Fatalf("coded: %v", err)
	}
}
