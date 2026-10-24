package mcpproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/zanetworker/aimux/internal/netguard"
)

// DaemonOptions configures Serve. Zero values give the defaults.
type DaemonOptions struct {
	// IdleTimeout is how long a backend may sit unused before it is stopped.
	// Default 10 minutes.
	IdleTimeout time.Duration
	// ReapEvery is how often idle backends are checked. Default 1 minute.
	ReapEvery time.Duration
}

// Serve exposes srv over Streamable HTTP at /mcp on ln, for local clients
// only, and stops idle backends in pool. It returns when ctx is cancelled,
// after closing every backend.
func Serve(ctx context.Context, ln net.Listener, srv *server.MCPServer, pool *Pool, opt DaemonOptions) error {
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 10 * time.Minute
	}
	if opt.ReapEvery <= 0 {
		opt.ReapEvery = time.Minute
	}
	mux := http.NewServeMux()
	// Clients such as Claude Code exit without ending their MCP session;
	// expiring idle sessions runs the unregister hook, which stops their
	// per-session backends. A client that comes back after expiry gets a new
	// session, as the MCP spec requires.
	mux.Handle("/mcp", server.NewStreamableHTTPServer(srv, server.WithSessionIdleTTL(opt.IdleTimeout)))
	httpSrv := &http.Server{Handler: netguard.LocalOnly(mux), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		t := time.NewTicker(opt.ReapEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pool.Reap(opt.IdleTimeout)
			}
		}
	}()

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		pool.Close()
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := httpSrv.Shutdown(shutCtx)
	pool.Close()
	if errors.Is(err, context.DeadlineExceeded) {
		// Long-lived streams did not finish; the backends are closed anyway.
		_ = httpSrv.Close()
		return nil
	}
	return err
}
