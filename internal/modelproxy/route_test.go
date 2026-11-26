package modelproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func routeOf(env map[string]string, model string) Route {
	return RouteOf(func(k string) string { return env[k] }, model)
}

func TestRouteOf(t *testing.T) {
	const key = "s3cretKEYs3cretKEY"
	for name, tc := range map[string]struct {
		env   map[string]string
		model string
		want  Route
	}{
		"nothing set goes direct": {nil, "claude-opus-5-5", Route{Mode: RouteDirect, Engine: EngineNameAnthropic, Model: "claude-opus-5-5"}},
		"set by env for both engines": {map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:7385/k/" + key, SessionEnvMarker: SessionEnvEngines},
			"claude-opus-5-5", Route{Through: true, Port: 7385, Mode: SessionEnvEngines, Engine: EngineNameAnthropic, Model: "claude-opus-5-5"}},
		"the same shell after /model to codex": {map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:7385/k/" + key, SessionEnvMarker: SessionEnvEngines},
			"codex:gpt-6.2-sol", Route{Through: true, Port: 7385, Mode: SessionEnvEngines, Engine: "codex", Model: "gpt-6.2-sol"}},
		"a named upstream": {map[string]string{"ANTHROPIC_BASE_URL": "http://localhost:7386", SessionEnvMarker: SessionEnvEngines},
			"lab:deepseek-v4", Route{Through: true, Port: 7386, Mode: SessionEnvEngines, Engine: "lab", Model: "deepseek-v4"}},
		"set for codex alone, model from the shell": {map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:7385", SessionEnvMarker: SessionEnvCodexed, "ANTHROPIC_MODEL": "codex:gpt-6.2-astra"},
			"", Route{Through: true, Port: 7385, Mode: SessionEnvCodexed, Engine: "codex", Model: "gpt-6.2-astra"}},
		"a shell set before the marker existed": {map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:7385", "ANTHROPIC_MODEL": "codex:gpt-6.2-sol"},
			"", Route{Through: true, Port: 7385, Mode: SessionEnvCodexed, Engine: "codex", Model: "gpt-6.2-sol"}},
		"another gateway is not the proxy": {map[string]string{"ANTHROPIC_BASE_URL": "https://gateway.example.com/v1"},
			"claude-opus-5-5", Route{Mode: RouteElsewhere, Model: "claude-opus-5-5"}},
		"an address that cannot be read":         {map[string]string{"ANTHROPIC_BASE_URL": "://nope"}, "", Route{Mode: RouteElsewhere}},
		"loopback with no port is not the proxy": {map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1"}, "", Route{Mode: RouteElsewhere}},
	} {
		got := routeOf(tc.env, tc.model)
		if got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, tc.want)
		}
		// The key travels in the address and must never come out of here.
		if out, _ := json.Marshal(got); strings.Contains(string(out), key) {
			t.Errorf("%s: the route holds the proxy key: %s", name, out)
		}
	}
}

// With another provider chosen, Claude Code never reads the base URL: the sessions go there, whatever else is set.
func TestRouteOf_AnotherProviderWins(t *testing.T) {
	for _, name := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		got := routeOf(map[string]string{name: "1", "ANTHROPIC_BASE_URL": "http://127.0.0.1:7385", SessionEnvMarker: SessionEnvEngines}, "claude-opus-5-5")
		if got != (Route{Mode: RouteElsewhere, Model: "claude-opus-5-5"}) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// In a shell set for Codex alone the proxy answers a plain model name with Codex, since no Claude login comes.
func TestRouteOf_APlainModelInACodexShellIsCodex(t *testing.T) {
	env := map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:7385", SessionEnvMarker: SessionEnvCodexed, "ANTHROPIC_MODEL": "codex:gpt-6.2-sol"}
	if got := routeOf(env, "claude-sonnet-5-5"); got.Engine != "codex" || got.Mode != SessionEnvCodexed {
		t.Fatalf("%+v", got)
	}
	// A named upstream is still that upstream.
	if got := routeOf(env, "lab:deepseek-v4"); got.Engine != "lab" {
		t.Fatalf("%+v", got)
	}
}

// A base URL the proxy does not serve (https on this machine) is not a route through it.
func TestRouteOf_HTTPSOnThisMachineIsNotTheProxy(t *testing.T) {
	if got := routeOf(map[string]string{"ANTHROPIC_BASE_URL": "https://127.0.0.1:7385"}, ""); got.Through || got.Mode != RouteElsewhere {
		t.Fatalf("%+v", got)
	}
}
