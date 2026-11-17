package toolgateway_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

const graceTok = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func serveTokens(t *testing.T, tokens *toolgateway.Tokens) func() int {
	t.Helper()
	pool := mcpproxy.NewPool(func(context.Context, string) (mcpproxy.Backend, error) {
		return nil, errors.New("no backend in this test")
	}, mcpproxy.PoolOptions{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	servers := map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1")}
	go func() { done <- mcpproxy.Serve(ctx, ln, servers, pool, mcpproxy.DaemonOptions{Tokens: tokens}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("serve: %v", err)
		}
	})
	return func() int {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` +
			mcp.LATEST_PROTOCOL_VERSION + `","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
		r, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+mcpproxy.EndpointPath("wiki"), strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+graceTok)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Error(err)
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
}

func lease() toolgateway.Lease {
	return toolgateway.Lease{Session: "s", Token: graceTok, Allow: map[string][]string{"wiki": {"wiki_search"}}, Expires: time.Now().Add(time.Hour)}
}

// A call made while a just-started worker has not got its lease back is
// held and then served, not refused with 401.
func TestServe_HeldDuringStartupGraceThenServed(t *testing.T) {
	tokens := &toolgateway.Tokens{Grace: 2 * time.Second}
	tokens.StartGrace(time.Now())
	status := serveTokens(t, tokens)
	go func() { time.Sleep(200 * time.Millisecond); _ = tokens.Put(lease()) }()
	if got := status(); got != http.StatusOK {
		t.Fatalf("held call: %d, want 200", got)
	}
}

func TestServe_UnknownTokenAfterGraceIs401(t *testing.T) {
	tokens := &toolgateway.Tokens{Grace: 15 * time.Second}
	tokens.StartGrace(time.Now().Add(-time.Minute))
	status := serveTokens(t, tokens)
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", got)
	}
}

func TestServe_OverTheWaitBoundIs503(t *testing.T) {
	tokens := &toolgateway.Tokens{Grace: 500 * time.Millisecond, MaxWaiting: 1}
	tokens.StartGrace(time.Now())
	status := serveTokens(t, tokens)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = status() }() // takes the one slot
	time.Sleep(100 * time.Millisecond)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("second waiter: %d, want 503", got)
	}
	wg.Wait()
}
