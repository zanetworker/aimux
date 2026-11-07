package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
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

	if len(cat.Servers["test"].Tools) != 3 || cat.Servers["test"].FetchedAt.Before(old) {
		t.Fatalf("test entry = %+v, want the test server's 3 fresh tools", cat.Servers["test"])
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

func TestRefreshCatalog_StartsNothingOnceCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	start := func(context.Context, string) (Backend, error) { called = true; return &fakeBackend{}, nil }
	errs := RefreshCatalog(ctx, &Catalog{}, start, []string{"a", "b"})
	if called {
		t.Fatal("a server was started after the context ended")
	}
	if len(errs) != 2 || !errors.Is(errs["a"], context.Canceled) {
		t.Fatalf("errs %v, want both servers failed with the cancel", errs)
	}
}

// Cancelling a refresh (Ctrl-C) closes the backends it started, even one
// whose tools/list write is blocked because it stopped reading stdin.
func TestRefreshCatalog_CancelClosesStartedBackends(t *testing.T) {
	b := newDeafListBackend()
	start := func(context.Context, string) (Backend, error) { return b, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan map[string]error, 1)
	go func() { done <- RefreshCatalog(ctx, &Catalog{}, start, []string{"deaf"}) }()
	for b.lists.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case errs := <-done:
		if errs["deaf"] == nil {
			t.Fatal("a cancelled listing reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not return after cancel")
	}
	if !b.isClosed() {
		t.Fatal("the started backend was left running")
	}
}
