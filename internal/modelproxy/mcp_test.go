package modelproxy

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDisabledMCPArgs(t *testing.T) {
	args, err := disabledMCPArgs(context.Background(), fakeCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mcp_servers.github.enabled=false", `mcp_servers."odd.name".enabled=false`, "mcp_servers.off.enabled=false"} {
		i := slices.Index(args, want)
		if i < 1 || args[i-1] != "-c" {
			t.Errorf("args %q: want -c %s", args, want)
		}
	}
	if slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "codex_app") }) {
		t.Errorf("args %q name a plugin's server, which makes Codex reject the config", args)
	}
}

func TestDisabledMCPArgsListFailure(t *testing.T) {
	t.Setenv("AIMUX_FAKE_CODEX_MCP_FAIL", "1")
	if _, err := disabledMCPArgs(context.Background(), fakeCodex); err == nil || !strings.Contains(err.Error(), "MCP servers") {
		t.Fatalf("got %v, want an MCP servers error", err)
	}
}

func TestCodexBackendRefusesToStartWithMCPServersUnknown(t *testing.T) {
	backend, _ := newFakeBackend(t)
	t.Setenv("AIMUX_FAKE_CODEX_MCP_FAIL", "1")
	_, err := backend.Run(context.Background(), fakeTurn("hello"), func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "MCP servers") {
		t.Fatalf("got %v, want the backend to refuse when Codex's MCP servers cannot be listed", err)
	}
}
