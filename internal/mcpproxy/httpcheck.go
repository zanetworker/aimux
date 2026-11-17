package mcpproxy

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// HTTPReach is what one unauthenticated (or header-carrying) MCP initialize
// request found out about an HTTP server.
type HTTPReach int

const (
	Reachable   HTTPReach = iota // answered; listing its tools decides
	NeedsOAuth                   // 401 with a WWW-Authenticate challenge
	Unreachable                  // refused, DNS failure, or no answer in time
)

// httpCheckTimeout bounds one check.
var httpCheckTimeout = 5 * time.Second

// Kept-direct reasons from a check.
const (
	reasonRefusedHeaders = "the server refused its headers (401)"
	reasonUnreachable    = "not reachable now; run install again when it is up"
)

const checkInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"aimux-install-check","version":"1"}}}`

// CheckHTTPServer POSTs one MCP initialize request with headers and reads
// only the status line and WWW-Authenticate; the body is discarded. 502,
// 503 and 504 mean down (Unreachable); any status other than those and a
// challenged 401 is Reachable: listing decides.
func CheckHTTPServer(ctx context.Context, url string, headers map[string]string) HTTPReach {
	ctx, cancel := context.WithTimeout(ctx, httpCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(checkInitialize))
	if err != nil {
		return Unreachable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Unreachable
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != "":
		return NeedsOAuth
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
		return Unreachable // down behind a proxy or load balancer: tried again next run
	}
	return Reachable
}

// checkAll runs check for each entry in parallel and splits the result:
// the names to move, every server kept direct with its reason, and the
// subset that needs OAuth (recorded so status can name them without a
// network call). All three are sorted by name.
func checkAll(ctx context.Context, entries map[string]HTTPEntry, check func(context.Context, string, map[string]string) HTTPReach) (move []string, kept []KeptDirect, oauth []KeptDirect) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := check(ctx, e.URL, e.Headers)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r == Reachable:
				move = append(move, name)
			case r == NeedsOAuth && len(e.Headers) == 0:
				k := KeptDirect{Server: name, Reason: ReasonNeedsOAuth}
				kept, oauth = append(kept, k), append(oauth, k)
			case r == NeedsOAuth:
				kept = append(kept, KeptDirect{Server: name, Reason: reasonRefusedHeaders})
			default:
				kept = append(kept, KeptDirect{Server: name, Reason: reasonUnreachable})
			}
		}()
	}
	wg.Wait()
	sort.Strings(move)
	byName := func(l []KeptDirect) { sort.Slice(l, func(a, b int) bool { return l[a].Server < l[b].Server }) }
	byName(kept)
	byName(oauth)
	return move, kept, oauth
}
