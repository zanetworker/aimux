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
	_, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	wantCode(t, err, errcode.SandboxGatewayDown)

	// Another sandbox failure keeps its own text and gets no code.
	e.L = &sandboxlaunch.Launcher{Backend: &failingEnsure{}}
	_, err = e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if _, ok := errcode.Of(err); ok || err == nil {
		t.Fatalf("error %v, want an uncoded failure", err)
	}
}

type failingEnsure struct{ sbBackend }

func (*failingEnsure) Ensure(context.Context, profile.Compiled) error {
	return errors.New("sandbox aimux-work-readonly is in phase Failed")
}

// openshell can start and only fail when its exec ends: the exit error
// carries the unreachable gateway, and the turn is coded the same way.
func TestSandboxTurn_OpenShellGatewayDownAtExitIsCoded(t *testing.T) {
	e, b := newSandbox(t)
	b.script = func(string) (string, error, error) {
		return "", errors.New("exit status 1: error: transport error: tcp connect error: Connection refused"), nil
	}
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, _, err := sendCollect(t, e, sid, "q")
	wantCode(t, err, errcode.SandboxGatewayDown)
}
