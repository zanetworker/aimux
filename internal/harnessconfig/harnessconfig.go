// Package harnessconfig turns a harness-neutral Intent into launch config for
// one agent harness (argv for a print-mode run, session/new metadata for ACP).
//
// Harness config is cooperative: the agent applies it to itself. The enforced
// counterpart is the OpenShell policy. The adapter mirrors that policy so the
// model never sees tools it cannot use.
package harnessconfig

import "errors"

// ErrHarnessNotSupported is returned by For for any harness without an adapter.
var ErrHarnessNotSupported = errors.New("harness not supported yet")

// Intent is what a run is allowed to do, independent of the harness.
type Intent struct {
	Mode        string              // "print" or "acp"
	Model       string              // optional
	Tools       map[string][]string // MCP server -> exact tool names
	Web, Files  bool
	InheritUser bool // load the user's own harness settings
	ResumeID    string
	SessionID   string // print mode: the id a new session gets (a UUID); not with ResumeID
}

// Spec is the launch config for one harness.
type Spec struct {
	Argv []string
	// MCPConfig renders the MCP config JSON (it holds the token); Argv refers
	// to it by MCPConfigPlaceholder.
	MCPConfig func(baseURL, token string) string
	ACPMeta   map[string]any
}

// Adapter builds a Spec for one harness.
type Adapter interface {
	Name() string
	Config(i Intent) (Spec, error)
}

// For returns the adapter for a harness name.
func For(name string) (Adapter, error) {
	if name == "claude-code" {
		return claudeCode{}, nil
	}
	return nil, ErrHarnessNotSupported
}
