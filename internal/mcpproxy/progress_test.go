package mcpproxy

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// A long call's progress reaches the session that made it, with that
// session's own token, so Claude Code's idle timer resets as it does for a
// direct server. Two sessions sharing one backend both use token 1: each
// must see only its own three updates.
func TestProxy_RelaysProgressToTheCallingSessionOnly(t *testing.T) {
	pool := NewPool(StdioStarter(testConfigs()), PoolOptions{})
	defer pool.Close()
	cat := &Catalog{}
	cat.Set("test", toolJSON("progress"), time.Now())
	base, _ := serveCatalog(t, cat, pool, DaemonOptions{})

	type outcome struct {
		updates []mcp.JSONRPCNotification
		err     error
	}
	call := func() outcome {
		c := httpClient(t, endpointURL(base, "test"))
		defer func() { _ = c.Close() }()
		var mu sync.Mutex
		var got []mcp.JSONRPCNotification
		c.OnNotification(func(n mcp.JSONRPCNotification) {
			if n.Method == "notifications/progress" {
				mu.Lock()
				got = append(got, n)
				mu.Unlock()
			}
		})
		req := callReq("progress", nil)
		req.Params.Meta = &mcp.Meta{ProgressToken: 1}
		_, err := c.CallTool(context.Background(), req)
		mu.Lock()
		defer mu.Unlock()
		return outcome{updates: append([]mcp.JSONRPCNotification(nil), got...), err: err}
	}

	results := make([]outcome, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = call() }(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("session %d: call failed: %v", i, r.err)
		}
		if len(r.updates) != 3 {
			t.Fatalf("session %d got %d progress updates, want its own 3", i, len(r.updates))
		}
		for _, n := range r.updates {
			if tok := n.Params.AdditionalFields["progressToken"]; tok != float64(1) {
				t.Fatalf("session %d got progressToken %v (%T), want its own token 1", i, tok, tok)
			}
		}
	}
}

// A call without a progress token gets no progress, as from a direct server.
func TestProxy_NoProgressTokenNoUpdates(t *testing.T) {
	pool := NewPool(StdioStarter(testConfigs()), PoolOptions{})
	defer pool.Close()
	cat := &Catalog{}
	cat.Set("test", toolJSON("progress"), time.Now())
	base, _ := serveCatalog(t, cat, pool, DaemonOptions{})
	c := httpClient(t, endpointURL(base, "test"))
	defer func() { _ = c.Close() }()
	var n int
	var mu sync.Mutex
	c.OnNotification(func(x mcp.JSONRPCNotification) {
		if x.Method == "notifications/progress" {
			mu.Lock()
			n++
			mu.Unlock()
		}
	})
	if _, err := c.CallTool(context.Background(), callReq("progress", nil)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 0 {
		t.Fatalf("got %d progress updates for a call without a token", n)
	}
}
