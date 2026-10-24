package mcpproxy

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRefreshCatalog(t *testing.T) {
	cfgs := testConfigs()
	cfgs["ghost"] = ServerConfig{Command: "/nonexistent/aimux-test-mcp"}
	cat := &Catalog{}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cat.Set("ghost", []json.RawMessage{json.RawMessage(`{"name":"kept"}`)}, old)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	errs := RefreshCatalog(ctx, cat, StdioStarter(cfgs), []string{"test", "ghost"})

	if len(cat.Servers["test"].Tools) != 2 || cat.Servers["test"].FetchedAt.Before(old) {
		t.Fatalf("test entry = %+v, want 2 fresh tools", cat.Servers["test"])
	}
	if errs["ghost"] == nil || errs["test"] != nil {
		t.Fatalf("errs = %v, want only ghost to fail", errs)
	}
	if e := cat.Servers["ghost"]; len(e.Tools) != 1 || !e.FetchedAt.Equal(old) {
		t.Fatalf("failed refresh replaced the old ghost entry: %+v", e)
	}
}

func TestRefreshCatalog_NoServers(t *testing.T) {
	cat := &Catalog{}
	if errs := RefreshCatalog(context.Background(), cat, StdioStarter(nil), nil); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
}
