// Package parts is the one list of what aimux is made of: the parts an owner turns on, and the background
// services each needs. status, upgrade and setup read it, where each kept a list of its own.
//
// It imports only internal/secretref from the repository, for the form a key reference takes, so anything may
// read it.
package parts

import "slices"

// Service is one background service: a program the operating system keeps running.
type Service struct {
	ID    string // what `aimux upgrade` is given: "model-proxy"
	Name  string // what a table shows: "model proxy"
	Label string // its launchd label
	// Port is where it listens on 127.0.0.1 unless told otherwise. Where a running service is found stays each
	// service's own rule: the MCP proxy's recorded port, the collector's configured one, the gateway's socket.
	Port int
	// Serve is the arguments after the program that start it, before any that settings add.
	Serve []string
	// Managed: aimux upgrade moves it to its own copy of the program, and setup may write its job. The tool
	// gateway is not: it runs another session's copy, and is changed only with that session.
	Managed bool
	// Update is the command that brings it to the current build, and the one status names when it needs a restart.
	Update string
}

// Part is something an owner turns on or off: a name on the setup menu, and the services behind it.
type Part struct {
	ID    string // "inference-proxy"
	Name  string // "Inference proxy"
	About string // one line for the menu
	// Services are the ids of the services it needs. A part can have none.
	Services []string
	// Always: on with nothing to start, and never offered as a choice.
	Always bool
	// Available is false for a part that is named and not built yet.
	Available bool
}

var services = []Service{
	{ID: "model-proxy", Name: "model proxy", Label: "com.aimux.model-proxy", Port: 7385,
		Serve: []string{"model", "proxy", "serve"}, Managed: true, Update: "aimux upgrade model-proxy"},
	{ID: "mcp-proxy", Name: "mcp proxy", Label: "com.aimux.mcp-proxy", Port: 7381,
		Serve: []string{"mcp", "proxy", "serve"}, Managed: true, Update: "aimux upgrade mcp-proxy"},
	{ID: "collector", Name: "collector", Label: "com.azaalouk.aimux-collect", Port: 4318,
		Serve: []string{"collect"}, Managed: true, Update: "aimux upgrade collector"},
	{ID: "tool-gateway", Name: "tool gateway", Label: "com.aimux.tool-gateway", Port: 7390,
		Serve: []string{"mcp", "proxy", "gateway", "serve"}, Update: "aimux mcp proxy gateway install"},
}

var parts = []Part{
	{ID: "dashboard", Name: "Sessions dashboard", About: "your running and past sessions, in the terminal and the browser", Always: true, Available: true},
	{ID: "inference-proxy", Name: "Inference proxy", Available: true, Services: []string{"model-proxy"},
		About: "records each model call that passes it, and lets a session be answered by another engine"},
	{ID: "mcp-proxy", Name: "MCP proxy", Available: true, Services: []string{"mcp-proxy"},
		About: "one shared copy of each MCP server, started on demand"},
	{ID: "phone-access", Name: "Phone access", Available: true,
		About: "your sessions from your phone, through Tailscale"},
	{ID: "sandboxes", Name: "Sandboxes", Available: true, Services: []string{"tool-gateway"},
		About: "profiles that run in an OpenShell sandbox"},
	{ID: "schedules", Name: "Schedules", About: "agents that run on a schedule"},
	{ID: "evals", Name: "Evals", About: "finds your corrections after each session, proposes fixes and re-tests them"},
}

// Services are the background services, in the order they are upgraded. The result is the caller's own.
func Services() []Service {
	out := make([]Service, len(services))
	for i, s := range services {
		s.Serve = slices.Clone(s.Serve)
		out[i] = s
	}
	return out
}

// ServiceByID finds a service by the id an owner types.
func ServiceByID(id string) (Service, bool) {
	for _, s := range Services() {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// MustService is ServiceByID for an id written in code. A wrong one is a mistake in the program.
func MustService(id string) Service {
	s, ok := ServiceByID(id)
	if !ok {
		panic("parts: no service " + id)
	}
	return s
}

// Parts are the parts, in the order the setup menu shows them. The result is the caller's own.
func Parts() []Part {
	out := make([]Part, len(parts))
	for i, p := range parts {
		p.Services = slices.Clone(p.Services)
		out[i] = p
	}
	return out
}

// PartByID finds a part by the id an owner types.
func PartByID(id string) (Part, bool) {
	for _, p := range Parts() {
		if p.ID == id {
			return p, true
		}
	}
	return Part{}, false
}
