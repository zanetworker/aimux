package sandboxlaunch

import (
	"context"
	"io"

	"github.com/zanetworker/aimux/internal/profile"
)

// Backend is the one way aimux reaches OpenShell. Plan 1 has one
// implementation, the openshell CLI (the SDK spike failed on file transfer;
// see the Task 6 report).
type Backend interface {
	// Ensure creates the profile's sandbox, or updates its policy when the
	// compiled digest changed, and returns once the sandbox is Ready.
	Ensure(ctx context.Context, c profile.Compiled) error
	// Exec runs args in the sandbox. mcpConfig (it holds a bearer token)
	// reaches the harness as a 0600 file that replaces
	// harnessconfig.MCPConfigPlaceholder in args; it is on no command line.
	// prompt reaches the harness on stdin, so page text and questions are on
	// no command line either and have no argument-size limit.
	// The ReadCloser streams stdout; Close returns the exit error.
	Exec(ctx context.Context, c profile.Compiled, mcpConfig, prompt string, args []string) (io.ReadCloser, error)
	// Download copies one file out of the sandbox to localPath (0600).
	Download(ctx context.Context, sandbox, remotePath, localPath string) error
}

// Launcher is what the engines hold: the one backend.
type Launcher struct{ Backend Backend }

// Ensure delegates to the backend.
func (l *Launcher) Ensure(ctx context.Context, c profile.Compiled) error {
	return l.Backend.Ensure(ctx, c)
}

// Exec delegates to the backend.
func (l *Launcher) Exec(ctx context.Context, c profile.Compiled, mcpConfig, prompt string, args []string) (io.ReadCloser, error) {
	return l.Backend.Exec(ctx, c, mcpConfig, prompt, args)
}
