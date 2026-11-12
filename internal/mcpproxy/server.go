package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// serverConfig holds what ServerOptions set.
type serverConfig struct {
	rejectLog func(server, tool string, err error)
	guard     CallGuard
}

// CallGuard vets a tool call before it reaches the backend. A non-nil error
// refuses the call.
type CallGuard func(ctx context.Context, server, tool string) error

// ServerOption changes how NewServers builds its servers.
type ServerOption func(*serverConfig)

// WithRejectLog calls fn for every tools/call a server fails, e.g. a call to
// a tool the catalog does not list.
func WithRejectLog(fn func(server, tool string, err error)) ServerOption {
	return func(c *serverConfig) { c.rejectLog = fn }
}

// WithCallGuard has every forwarded call checked by guard first. A refused
// call returns a tool error result naming the tool and the reason, and the
// backend is never reached; the reject log, if set, hears it too.
func WithCallGuard(guard CallGuard) ServerOption {
	return func(c *serverConfig) { c.guard = guard }
}

// buildConfig is what opts set.
func buildConfig(opts []ServerOption) serverConfig {
	var cfg serverConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// NewServers builds one MCP server per catalog server. Each lists that
// server's catalog tools under their original names and forwards calls
// through pool, so a backend starts on its first call. A tool the catalog
// holds in a form that cannot be decoded is left out.
func NewServers(cat *Catalog, pool *Pool, opts ...ServerOption) map[string]*server.MCPServer {
	var cfg serverConfig
	for _, o := range opts {
		o(&cfg)
	}
	out := make(map[string]*server.MCPServer, len(cat.Servers))
	for name, e := range cat.Servers {
		srv := newServer(name, pool, opts...)
		tools, _ := serverTools(name, e.Tools, pool, cfg)
		srv.SetTools(tools...)
		out[name] = srv
	}
	return out
}

// newServer is an MCP server for backend with no tools yet. Ending a client
// session closes that session's per-session backends.
func newServer(backend string, pool *Pool, opts ...ServerOption) *server.MCPServer {
	var cfg serverConfig
	for _, o := range opts {
		o(&cfg)
	}
	hooks := &server.Hooks{}
	if cfg.rejectLog != nil {
		hooks.AddOnError(func(ctx context.Context, id any, method mcp.MCPMethod, message any, err error) {
			if method != mcp.MethodToolsCall {
				return
			}
			tool := ""
			if req, ok := message.(*mcp.CallToolRequest); ok {
				tool = req.Params.Name
			}
			cfg.rejectLog(backend, tool, err)
		})
	}
	// A session token sees only its own tools; a request without one (stdio,
	// the admin Token) sees them all.
	hooks.AddAfterListTools(func(ctx context.Context, _ any, _ *mcp.ListToolsRequest, res *mcp.ListToolsResult) {
		a, ok := tokenAccessFrom(ctx)
		if !ok || res == nil {
			return
		}
		kept := make([]mcp.Tool, 0, len(res.Tools))
		for _, t := range res.Tools {
			if a.permits(backend, t.Name) {
				kept = append(kept, t)
			}
		}
		res.Tools = kept
	})
	hooks.AddOnUnregisterSession(func(ctx context.Context, s server.ClientSession) {
		pool.CloseSession(s.SessionID())
	})
	// listChanged: a reload that changes the server's tools tells connected
	// clients to list them again.
	return server.NewMCPServer(backend, "1", server.WithToolCapabilities(true), server.WithHooks(hooks))
}

// serverTools decodes backend's catalog tools into forwarding tools, and
// returns one message per tool it could not decode.
func serverTools(backend string, raw []json.RawMessage, pool *Pool, cfg serverConfig) ([]server.ServerTool, []string) {
	var tools []server.ServerTool
	var skipped []string
	for _, r := range raw {
		tool, err := DecodeTool(r)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", backend, err))
			continue
		}
		tools = append(tools, server.ServerTool{Tool: tool, Handler: forwarder(pool, backend, tool.Name, cfg)})
	}
	return tools, skipped
}

func forwarder(pool *Pool, backend, name string, cfg serverConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		refuse := func(err error) (*mcp.CallToolResult, error) {
			if cfg.rejectLog != nil {
				cfg.rejectLog(backend, name, err)
			}
			return mcp.NewToolResultError(fmt.Sprintf("tool %s refused: %v", name, err)), nil
		}
		// The session token's list comes first, then the pin check.
		if a, ok := tokenAccessFrom(ctx); ok && !a.permits(backend, name) {
			return refuse(errors.New("not allowed for this session"))
		}
		if cfg.guard != nil {
			if err := cfg.guard(ctx, backend, name); err != nil {
				return refuse(err)
			}
		}
		session := ""
		if cs := server.ClientSessionFromContext(ctx); cs != nil {
			session = cs.SessionID()
		}
		fwd := mcp.CallToolRequest{}
		fwd.Params.Name = name
		fwd.Params.Arguments = req.Params.Arguments
		fwd.Params.Meta = req.Params.Meta
		ctx, fwd = relayProgress(ctx, fwd) // its progress comes back to this session
		return pool.Call(ctx, backend, session, fwd), nil
	}
}

// SkippedTools is one message per catalog tool NewServers leaves out,
// "<server>: <why>", sorted by server. It never holds env values or tool
// arguments.
func SkippedTools(cat *Catalog) []string {
	var out []string
	for _, name := range sortedServerNames(cat.Servers) {
		_, skipped := serverTools(name, cat.Servers[name].Tools, nil, serverConfig{})
		out = append(out, skipped...)
	}
	return out
}
