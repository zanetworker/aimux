// Package gatewayworker wires the sandbox tool gateway worker: the
// read-only gateway (toolgateway.ReadonlyServer with the real MCP builders
// and the aimux-tools runner) and its control socket, as `aimux mcp proxy
// gateway serve` runs it.
package gatewayworker

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// NewServer is the read-only gateway over home's registry and pins, with
// the real builders; listen is how it listens (net.Listen). Its token table
// checks pins against the snapshot it serves.
func NewServer(home, admin string, l *log.Logger, listen func(network, addr string) (net.Listener, error)) (*toolgateway.ReadonlyServer, *toolreg.Store, *toolgateway.Tokens) {
	store := toolgateway.NewStore(home)
	tokens := &toolgateway.Tokens{}
	srv := &toolgateway.ReadonlyServer{Store: store, Token: admin, Tokens: tokens, Log: l,
		Listen: listen, Build: mcpproxy.BuildReadonly, Serve: mcpproxy.Serve, Rebuild: mcpproxy.BuildReadonlyBackends,
		// openshell children get only what they need, not the worker's env.
		Runner: sandboxlaunch.RunnerTools(sandboxlaunch.MinimalEnvRunner(os.Environ()), filepath.Join(home, ".aimux", "sandboxes"))}
	tokens.Current = srv.Applied
	return srv, store, tokens
}

// StartupGrace is how long after a start the worker holds a call whose
// token it does not know yet, waiting for remote serve to put the lease.
const StartupGrace = 15 * time.Second

// Run serves the gateway and its control socket under home until ctx ends.
func Run(ctx context.Context, home string, l *log.Logger, listen func(network, addr string) (net.Listener, error)) error {
	admin, err := toolgateway.NewToken() // required by the server; never sent
	if err != nil {
		return fmt.Errorf("make the gateway's admin token: %w", err)
	}
	srv, store, tokens := NewServer(home, admin, l, listen)
	// Just after a (re)start no lease is known yet: hold calls with an
	// unknown token until remote serve puts their lease again.
	tokens.Grace = StartupGrace
	tokens.StartGrace(time.Now()) // RunWorker starts it again once leases can be put
	w := &toolgateway.Worker{Server: srv, Store: store, Tokens: tokens, Log: l}
	return toolgateway.RunWorker(ctx, w, srv.Start, toolgateway.ControlSocketPath(home))
}
