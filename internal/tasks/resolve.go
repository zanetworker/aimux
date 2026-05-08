package tasks

import "fmt"

// NewProvider creates a Provider based on the specified backend.
// backend can be:
//   - "gws": Use the gws CLI backend
//   - "mcp": Use the MCP server backend (requires mcpEndpoint)
//   - "auto" or "": Auto-detect available backend (prefers gws, falls back to mcp)
//
// Returns an error if the requested backend is unavailable or unknown.
func NewProvider(backend, mcpEndpoint string) (Provider, error) {
	switch backend {
	case "gws":
		return NewGWSProvider()
	case "mcp":
		return NewMCPProvider(mcpEndpoint)
	case "auto", "":
		if GWSAvailable() {
			return NewGWSProvider()
		}
		if mcpEndpoint != "" {
			return NewMCPProvider(mcpEndpoint)
		}
		return nil, fmt.Errorf("tasks: no backend available (gws not found, no mcp_endpoint configured)")
	default:
		return nil, fmt.Errorf("tasks: unknown backend %q", backend)
	}
}
