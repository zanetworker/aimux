package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// NewServers builds one MCP server per catalog server. Each lists that
// server's catalog tools under their original names and forwards calls
// through pool, so a backend starts on its first call. A tool the catalog
// holds in a form that cannot be decoded is left out.
func NewServers(cat *Catalog, pool *Pool) map[string]*server.MCPServer {
	out := make(map[string]*server.MCPServer, len(cat.Servers))
	for name, e := range cat.Servers {
		srv := newServer(name, pool)
		tools, _ := serverTools(name, e.Tools, pool)
		srv.SetTools(tools...)
		out[name] = srv
	}
	return out
}

// newServer is an MCP server for backend with no tools yet. Ending a client
// session closes that session's per-session backends.
func newServer(backend string, pool *Pool) *server.MCPServer {
	hooks := &server.Hooks{}
	hooks.AddOnUnregisterSession(func(ctx context.Context, s server.ClientSession) {
		pool.CloseSession(s.SessionID())
	})
	// listChanged: a reload that changes the server's tools tells connected
	// clients to list them again.
	return server.NewMCPServer(backend, "1", server.WithToolCapabilities(true), server.WithHooks(hooks))
}

// serverTools decodes backend's catalog tools into forwarding tools, and
// returns one message per tool it could not decode.
func serverTools(backend string, raw []json.RawMessage, pool *Pool) ([]server.ServerTool, []string) {
	var tools []server.ServerTool
	var skipped []string
	for _, r := range raw {
		tool, err := DecodeTool(r)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", backend, err))
			continue
		}
		tools = append(tools, server.ServerTool{Tool: tool, Handler: forwarder(pool, backend, tool.Name)})
	}
	return tools, skipped
}

func forwarder(pool *Pool, backend, name string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		_, skipped := serverTools(name, cat.Servers[name].Tools, nil)
		out = append(out, skipped...)
	}
	return out
}
