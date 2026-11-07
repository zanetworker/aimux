package main

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/provider"
)

// The CLI, the web server and the orchestrator use the local providers,
// Claude then Codex, whatever the enabled flags say (as before the registry).
func TestMainWiring_LocalProvidersRegardlessOfConfig(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"codex": {Enabled: false}}}
	cfg.Kubernetes.Enabled, cfg.Kubernetes.RedisURL = true, "redis://x"
	local := provider.NewRegistry(t.TempDir(), cfg).Local()

	if got := strings.Join(providerNames(local), ","); got != "claude,codex" {
		t.Fatalf("providerNames = %s, want claude,codex", got)
	}
	if got := agentProviders(local); len(got) != 2 || got[0].Name() != "claude" || got[1].Name() != "codex" {
		t.Fatalf("agentProviders = %v", got)
	}
	parsers := traceParsers(local)
	if len(parsers) != 2 || parsers["claude"] == nil || parsers["codex"] == nil {
		t.Fatalf("traceParsers keys = %v, want claude and codex", parsers)
	}
	if turns, err := parsers["claude"]("/nonexistent/x.jsonl"); err == nil && len(turns) != 0 {
		t.Fatalf("claude parser on a missing file = %v, %v", turns, err)
	}
	if len(providerNames(nil)) != 0 || len(traceParsers(nil)) != 0 {
		t.Fatal("empty input should give empty output")
	}
}
