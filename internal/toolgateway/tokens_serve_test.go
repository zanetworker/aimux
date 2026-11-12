package toolgateway_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// The real Tokens plugs into the daemon: a revoked token stops working at once.
func TestTokens_ServeHonoursRevocation(t *testing.T) {
	var tokens toolgateway.Tokens
	tok, err := tokens.Issue("sess-1", map[string][]string{"wiki": {"wiki_search"}})
	if err != nil {
		t.Fatal(err)
	}
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
	go func() { done <- mcpproxy.Serve(ctx, ln, servers, pool, mcpproxy.DaemonOptions{Tokens: &tokens}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done: //nolint:errcheck // the serve exit error is irrelevant to teardown
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	status := func(path string) int {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` +
			mcp.LATEST_PROTOCOL_VERSION + `","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
		r, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := status(mcpproxy.EndpointPath("wiki")); got != http.StatusOK {
		t.Fatalf("issued token: got %d, want 200", got)
	}
	if got := status(mcpproxy.EndpointPath("slack")); got != http.StatusForbidden {
		t.Fatalf("server outside the token: got %d, want 403", got)
	}
	tokens.Revoke("sess-1")
	if got := status(mcpproxy.EndpointPath("wiki")); got != http.StatusUnauthorized {
		t.Fatalf("revoked token: got %d, want 401", got)
	}
}
