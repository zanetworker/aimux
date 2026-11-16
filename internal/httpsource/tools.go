package httpsource

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// Tools describes ops as MCP tools for source, in order: <source>_<ID>,
// read-only, with the method and path template (x-aimux-http) and the base
// URL (x-aimux-base) in the annotations so a pin covers where a call goes.
// The description is not pinned.
func Tools(source, baseURL string, ops []Operation) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(ops))
	for _, op := range ops {
		props := map[string]any{}
		var required []string
		for _, p := range op.Params {
			props[p.Name] = p.schema()
			if p.Required {
				required = append(required, p.Name)
			}
		}
		sort.Strings(required)
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		description := op.Description
		if description == "" {
			description = "GET " + op.Path
		}
		raw, err := json.Marshal(map[string]any{
			"name":        ToolName(source, op),
			"description": description,
			"inputSchema": schema,
			"annotations": map[string]any{
				"readOnlyHint": true,
				"x-aimux-http": "GET " + op.Path,
				"x-aimux-base": baseURL,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("encode tool %s: %w", ToolName(source, op), err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// schema is p as a JSON Schema property.
func (p Param) schema() map[string]any {
	s := map[string]any{"type": p.Type}
	switch {
	case len(p.Enum) == 0:
	case p.Type == "boolean":
		bools := make([]bool, 0, len(p.Enum))
		for _, e := range p.Enum {
			if b, err := strconv.ParseBool(e); err == nil {
				bools = append(bools, b)
			}
		}
		s["enum"] = bools
	case p.Type == "integer":
		ints := make([]int64, 0, len(p.Enum))
		for _, e := range p.Enum {
			if n, err := strconv.ParseInt(e, 10, 64); err == nil {
				ints = append(ints, n)
			}
		}
		s["enum"] = ints
	default:
		s["enum"] = p.Enum
	}
	return s
}
