package toolgateway

import (
	"context"
	"io"
	"log"
	"slices"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

type nopTools struct{}

func (nopTools) Ensure(context.Context) error { return nil }
func (nopTools) RunTool(context.Context, []string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}

// The runner sandbox is bound per snapshot (its policy follows the pins),
// so the servers' config carries the binding for the snapshot served.
func TestReadonlyServer_ConfigBindsRunnerToSnapshot(t *testing.T) {
	reg := toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{
		"github": {Type: toolreg.TypeCLI, Pack: "gh"},
		"api":    {Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: "https://a.example"},
		"wiki":   {Type: toolreg.TypeLocalNotes, Root: "/w"},
	}}
	snap := toolreg.NewSnapshot(reg, nil, nil)
	var boundTo []string
	r := &ReadonlyServer{Log: log.New(io.Discard, "", 0), Runner: func(s toolreg.Snapshot) func(string) mcpproxy.ToolRunner {
		boundTo = append(boundTo, s.Version())
		return func(string) mcpproxy.ToolRunner { return nopTools{} }
	}}
	cfg := r.configOf(snap)
	if cfg.Tools == nil || !slices.Equal(boundTo, []string{snap.Version()}) {
		t.Fatalf("tools %v, bound to %v", cfg.Tools != nil, boundTo)
	}
	if got := backendNames(cfg); !slices.Equal(got, []string{"github", "wiki"}) {
		t.Fatalf("backends = %v", got)
	}
	// A runner source restarts on every new snapshot: a new pin can change
	// the runner policy under it. The others move only when their config does.
	next := r.configOf(toolreg.NewSnapshot(reg, toolreg.PinTable{"github": {"gh_repo_view": {Hash: "h"}}}, nil))
	if !moved(cfg, next, "github") || moved(cfg, next, "wiki") {
		t.Fatalf("moved github %v wiki %v", moved(cfg, next, "github"), moved(cfg, next, "wiki"))
	}

	// No runner binding: no runner backends, as before.
	plain := (&ReadonlyServer{Log: log.New(io.Discard, "", 0)}).configOf(snap)
	if plain.Tools != nil || !slices.Equal(backendNames(plain), []string{"wiki"}) || moved(plain, plain, "github") {
		t.Fatalf("unbound: %v", backendNames(plain))
	}
}

// A bound runner is a func, never DeepEqual: the config comparisons that
// decide whether a background recheck was superseded must ignore it.
func TestSameConfig_IgnoresRunnerBinding(t *testing.T) {
	r := &ReadonlyServer{Log: log.New(io.Discard, "", 0), Runner: func(toolreg.Snapshot) func(string) mcpproxy.ToolRunner {
		return func(string) mcpproxy.ToolRunner { return nopTools{} }
	}}
	snap := toolreg.NewSnapshot(toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"jira": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:1/mcp"}}}, nil, nil)
	a := r.configOf(snap)
	if !sameConfig(a, a) || !sameConfig(a, r.configOf(snap)) {
		t.Fatal("same config with a runner binding compared unequal")
	}
	b := r.configOf(toolreg.NewSnapshot(toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"jira": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:2/mcp"}}}, nil, nil))
	if sameConfig(a, b) {
		t.Fatal("a moved source compared equal")
	}
}
