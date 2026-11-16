// Package harnessconfig turns harness-neutral Capabilities and a Launch into
// launch config for one agent harness (argv for a print-mode run,
// session/new metadata for ACP).
//
// Harness config is cooperative: the agent applies it to itself. The enforced
// counterpart is the OpenShell policy. The adapter mirrors that policy so the
// model never sees tools it cannot use.
package harnessconfig

import "errors"

// ErrHarnessNotSupported is returned by For for any harness without an adapter.
var ErrHarnessNotSupported = errors.New("harness not supported yet")

// ToolGrantView is the MCP tools a run may call, read-only: the granted
// server names and each one's exact tool names. profile.ToolGrant
// implements it; a nil view grants nothing.
type ToolGrantView interface {
	Servers() []string
	Tools(server string) []string
}

// FileGrant is the host paths a run may read and write. The OpenShell
// policy enforces them; an adapter only mirrors them if its harness needs
// to know.
type FileGrant struct {
	Read, Write []string
}

// Capabilities is what a run is allowed to do, independent of the harness
// and of how it is launched. Every adapter (Claude Code today, Codex or
// Gemini later) reads the same Capabilities and Launch.
type Capabilities struct {
	Grant       ToolGrantView // MCP tools; nil grants none
	Web         bool
	Files       FileGrant
	InheritUser bool   // load the user's own harness settings
	Model       string // optional
}

// Launch modes, harness-neutral: each adapter maps them to its harness.
const (
	ModeHeadless    = "headless"    // one non-interactive run per turn (claude -p)
	ModeInteractive = "interactive" // a long-lived session over ACP
)

// Launch is how one run starts: its mode and which session it is.
type Launch struct {
	Mode      string // ModeHeadless or ModeInteractive
	ResumeID  string
	SessionID string // print mode: the id a new session gets (a UUID); not with ResumeID
}

// Spec is the launch config for one harness.
type Spec struct {
	Argv []string
	// MCPConfig renders the MCP config JSON (it holds the token); Argv refers
	// to it by MCPConfigPlaceholder.
	MCPConfig func(baseURL, token string) string
	ACPMeta   map[string]any
	// Env is exported into the harness process at launch (print mode).
	Env map[string]string
}

// Adapter builds a Spec for one harness.
type Adapter interface {
	Name() string
	Config(c Capabilities, l Launch) (Spec, error)
}

// For returns the adapter for a harness name.
func For(name string) (Adapter, error) {
	if name == "claude-code" {
		return claudeCode{}, nil
	}
	return nil, ErrHarnessNotSupported
}
