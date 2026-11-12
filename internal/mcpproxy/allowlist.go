package mcpproxy

// Allowlist maps a backend name to the tool names it may expose.
type Allowlist map[string][]string

// Allowed returns a copy holding only the backends and tools a lists, in
// catalog order; c is unchanged. A listed tool the catalog lacks is ignored,
// and a backend left with no tools is dropped.
func (c *Catalog) Allowed(a Allowlist) *Catalog {
	out := &Catalog{Servers: map[string]CatalogEntry{}}
	for s, e := range c.Servers {
		want := map[string]bool{}
		for _, n := range a[s] {
			want[n] = true
		}
		if len(want) == 0 {
			continue
		}
		kept := CatalogEntry{FetchedAt: e.FetchedAt}
		for _, raw := range e.Tools {
			// A tool that cannot be decoded is dropped on purpose: fail closed.
			if tool, err := DecodeTool(raw); err == nil && want[tool.Name] {
				kept.Tools = append(kept.Tools, raw)
			}
		}
		if len(kept.Tools) > 0 {
			out.Servers[s] = kept
		}
	}
	return out
}
