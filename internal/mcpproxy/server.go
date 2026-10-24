package mcpproxy

import (
	"context"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// NewServer builds the proxy's MCP server from the catalog. Each backend tool
// is exposed as ExposedName(server, tool) and forwarded through pool under
// its original name. name is what clients register the proxy as (it counts
// toward Claude Code's tool name limit). It also returns one message per
// catalog tool it could not expose.
func NewServer(cat *Catalog, pool *Pool, name string) (*server.MCPServer, []string) {
	hooks := &server.Hooks{}
	hooks.AddOnUnregisterSession(func(ctx context.Context, s server.ClientSession) {
		pool.CloseSession(s.SessionID())
	})
	srv := server.NewMCPServer(name, "1", server.WithToolCapabilities(false), server.WithHooks(hooks))

	servers := make([]string, 0, len(cat.Servers))
	for s := range cat.Servers {
		servers = append(servers, s)
	}
	sort.Strings(servers) // deterministic order, so name collisions resolve the same way every run

	maxLen := MaxToolNameLen(name)
	used := map[string]bool{}
	var skipped []string
	for _, backend := range servers {
		for _, raw := range cat.Servers[backend].Tools {
			tool, err := DecodeTool(raw)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", backend, err))
				continue
			}
			original := tool.Name
			tool.Name = ExposedName(backend, original, maxLen)
			if used[tool.Name] {
				skipped = append(skipped, fmt.Sprintf("%s: tool %s collides with exposed name %s", backend, original, tool.Name))
				continue
			}
			used[tool.Name] = true
			tool.Description = "[" + backend + "] " + tool.Description
			srv.AddTool(tool, forwarder(pool, backend, original))
		}
	}
	return srv, skipped
}

func forwarder(pool *Pool, backend, original string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		session := ""
		if cs := server.ClientSessionFromContext(ctx); cs != nil {
			session = cs.SessionID()
		}
		fwd := mcp.CallToolRequest{}
		fwd.Params.Name = original
		fwd.Params.Arguments = req.Params.Arguments
		fwd.Params.Meta = req.Params.Meta // keeps the progress token
		return pool.Call(ctx, backend, session, fwd), nil
	}
}
