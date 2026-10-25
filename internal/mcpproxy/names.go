// Package mcpproxy serves many stdio MCP servers behind one MCP endpoint,
// starting each backend on its first tool call and sharing it across clients.
package mcpproxy

import (
	"crypto/sha256"
	"encoding/hex"
)

// claudeToolNameLimit is Claude Code's cap on mcp__<server>__<tool>. A longer
// name breaks the session once tool search loads it (claude-code#97616).
const claudeToolNameLimit = 64

// MaxToolNameLen is the longest tool name the proxy may expose when Claude
// Code registers the proxy under proxyName.
func MaxToolNameLen(proxyName string) int {
	return claudeToolNameLimit - len("mcp__"+proxyName+"__")
}

// ExposedName returns the name a backend tool is exposed under:
// "<server>__<tool>", or, when that exceeds max, a prefix of it plus a short
// hash of the full name so distinct tools stay distinct and stable.
func ExposedName(server, tool string, max int) string {
	name := server + "__" + tool
	if len(name) <= max {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:])[:6]
	return name[:max-len(suffix)] + suffix
}
