package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
)

// ToolGrant is the MCP tools a compiled profile may call: server -> exact
// tool names. It is immutable: it keeps its own copy of the input, and every
// accessor returns a fresh copy, so the policy, the gateway token scope and
// the harness, which all render from one grant, cannot drift apart. The zero
// value grants nothing. It satisfies harnessconfig.ToolGrantView.
type ToolGrant struct {
	servers map[string][]string // never handed out; values sorted and unique
}

// NewToolGrant copies servers, sorting and deduplicating each tool list.
func NewToolGrant(servers map[string][]string) ToolGrant {
	if len(servers) == 0 {
		return ToolGrant{}
	}
	m := make(map[string][]string, len(servers))
	for s, tools := range servers {
		m[s] = slices.Compact(slices.Sorted(slices.Values(tools)))
		if m[s] == nil {
			m[s] = []string{}
		}
	}
	return ToolGrant{servers: m}
}

// Servers is the granted server names, sorted.
func (g ToolGrant) Servers() []string { return slices.Sorted(maps.Keys(g.servers)) }

// Tools is the tools granted on server, sorted; nil for a server not granted.
func (g ToolGrant) Tools(server string) []string { return slices.Clone(g.servers[server]) }

// Empty reports whether the grant has no servers.
func (g ToolGrant) Empty() bool { return len(g.servers) == 0 }

// Scope is a deep copy of the grant as a map, the shape a gateway token takes.
func (g ToolGrant) Scope() map[string][]string {
	out := make(map[string][]string, len(g.servers))
	for s, tools := range g.servers {
		out[s] = slices.Clone(tools)
	}
	return out
}

// Digest is "sha256:" over the grant's canonical JSON (keys and tools
// sorted); equal grants have equal digests whatever order they were built in.
func (g ToolGrant) Digest() string {
	b, _ := json.Marshal(g.Scope()) // map[string][]string always marshals
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
