package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestWrap_CarriesCodeMessageAndCause(t *testing.T) {
	cause := errors.New("dial unix gateway.sock: connect: no such file or directory")
	err := fmt.Errorf("turn: %w", Wrap(ToolGatewayNotRunning, cause))
	e, ok := Of(err)
	if !ok || e.Code != ToolGatewayNotRunning {
		t.Fatalf("Of = %+v %v, want tool_gateway_not_running", e, ok)
	}
	if e.Message != "Workspace needs the tool gateway: run aimux mcp proxy gateway install" {
		t.Fatalf("message %q", e.Message)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause is not reachable through errors.Is")
	}
	if want := e.Message + ": " + cause.Error(); e.Error() != want {
		t.Fatalf("Error() = %q, want %q (the cause stays for logs)", e.Error(), want)
	}
}

func TestOf_UncodedAndNil(t *testing.T) {
	if _, ok := Of(errors.New("plain")); ok {
		t.Fatal("a plain error has a code")
	}
	if _, ok := Of(nil); ok {
		t.Fatal("nil has a code")
	}
	if e := Wrap(SandboxGatewayDown, nil); e.Error() != Message(SandboxGatewayDown) {
		t.Fatalf("no cause: Error() = %q", e.Error())
	}
}

// Every code has a message for a person, and no unknown code gets one.
func TestMessage_EveryCode(t *testing.T) {
	for _, c := range []string{ToolGatewayNotRunning, GatewayBackendDown, ImageInvalid, ProfileUnavailable, SandboxGatewayDown, ServerPortInUse, ServerCrashLoop} {
		if Message(c) == "" {
			t.Errorf("%s has no message", c)
		}
	}
	if Message("made_up") != "" {
		t.Fatal("an unknown code has a message")
	}
}
