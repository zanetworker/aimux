// Package errcode gives the errors clients act on a stable code and a
// message written for a person. The code travels end to end (remote API
// error bodies and turn_error events, the Wingman relay's error events);
// clients match on the code and show the message, never parse the text.
package errcode

import "errors"

// The stable codes. Remote API clients match on them.
const (
	ToolGatewayNotRunning = "tool_gateway_not_running" // the sandbox tool gateway worker is not running
	GatewayBackendDown    = "gateway_backend_down"     // the worker runs but did not take the turn's lease
	ImageInvalid          = "image_invalid"            // a question's image is refused
	ProfileUnavailable    = "profile_unavailable"      // the turn's profile does not resolve or compile
	SandboxGatewayDown    = "sandbox_gateway_down"     // the OpenShell gateway is not reachable
	ServerPortInUse       = "server_port_in_use"       // a managed server's port is taken by a process aimux did not start
	ServerCrashLoop       = "server_crash_loop"        // a managed server crashed 5 times in 5 minutes and is not restarted
)

var messages = map[string]string{
	ToolGatewayNotRunning: "Workspace needs the tool gateway: run aimux mcp proxy gateway install",
	GatewayBackendDown:    "The tool gateway did not take this turn's tools: check aimux mcp proxy gateway status",
	ImageInvalid:          "The image cannot be used: paste a PNG, JPEG, WebP or GIF of at most 1.5 MiB",
	ProfileUnavailable:    "This mode's profile could not be loaded: run aimux profile check",
	SandboxGatewayDown:    "The OpenShell gateway is not reachable: check it with openshell -g local status",
	ServerPortInUse:       "A server's port is in use by a process aimux did not start: check aimux servers status",
	ServerCrashLoop:       "A server keeps crashing and is no longer restarted: check aimux servers status, then aimux servers restart",
}

// Message is code's message for a person; "" for an unknown code.
func Message(code string) string { return messages[code] }

// Error is an error with a stable Code and a Message for a person; Err is
// the cause, kept for logs.
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap gives cause the code and the code's message.
func Wrap(code string, cause error) *Error {
	return &Error{Code: code, Message: Message(code), Err: cause}
}

// Of is the coded error in err's chain, if any.
func Of(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
