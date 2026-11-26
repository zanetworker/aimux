package modelproxy

import (
	"net/url"
	"strconv"
	"strings"
)

// Where a shell's Claude Code sessions go, besides the two forms `aimux model proxy env` sets.
const (
	RouteDirect    = "direct"    // no base URL: straight to Anthropic
	RouteElsewhere = "elsewhere" // a base URL that is not a proxy on this machine: another gateway
	RouteUnmarked  = "unmarked"  // a proxy on this machine, set by hand and not by `aimux model proxy env`

	EngineNameAnthropic = "anthropic"
)

// OtherProviderVars are the variables that make Claude Code use another provider and ignore the base URL.
var OtherProviderVars = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// Route is where the sessions of one shell go, and which engine answers one model. It never holds the proxy
// key, which the base URL may carry.
type Route struct {
	Through bool   `json:"through_proxy"`
	Port    int    `json:"port,omitempty"`
	Mode    string `json:"mode"`
	// Engine answers Model: "anthropic", "codex", or a named upstream. Empty when the sessions go elsewhere.
	Engine string `json:"engine,omitempty"`
	// Model is the model without its engine prefix; empty when none is known.
	Model string `json:"model,omitempty"`
}

// RouteOf reads a shell's route from its environment. model is the model a session is on now, which follows
// /model; empty means the one the shell pins, if any. A base URL counts as the proxy when it is a port on this
// machine; whether an aimux proxy answers there is for the caller to ask.
func RouteOf(getenv func(string) string, model string) Route {
	if model == "" {
		model = getenv("ANTHROPIC_MODEL")
	}
	engine, bare, named := EngineNameAnthropic, model, false
	if name, rest, ok := strings.Cut(model, ":"); ok && upstreamName.MatchString(name) {
		engine, bare, named = name, rest, true
	}
	// With one of these set, Claude Code talks to that provider and never reads the base URL.
	for _, other := range OtherProviderVars {
		if getenv(other) != "" {
			return Route{Mode: RouteElsewhere, Model: bare}
		}
	}
	base := getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		return Route{Mode: RouteDirect, Engine: engine, Model: bare}
	}
	u, err := url.Parse(base)
	if err != nil {
		return Route{Mode: RouteElsewhere}
	}
	port, _ := strconv.Atoi(u.Port())
	// The proxy serves plain http on this machine and nothing else.
	if host := u.Hostname(); u.Scheme != "http" || (host != "127.0.0.1" && host != "localhost") || port <= 0 {
		return Route{Mode: RouteElsewhere, Model: bare}
	}
	mode := getenv(SessionEnvMarker)
	switch {
	case mode == SessionEnvEngines || mode == SessionEnvCodexed:
	case engine == "codex":
		mode = SessionEnvCodexed // a shell set before the marker existed
	default:
		mode = RouteUnmarked
	}
	// A shell set for Codex alone sends the proxy key and no Claude login, so a model with no engine named is
	// answered by Codex.
	if mode == SessionEnvCodexed && !named {
		engine = "codex"
	}
	return Route{Through: true, Port: port, Mode: mode, Engine: engine, Model: bare}
}
