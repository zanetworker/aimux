package clipack

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Tools returns one MCP tool definition per command: name <pack>_<command>,
// the description, an inputSchema that mirrors what Argv accepts, and
// annotations naming the pack version, the installed binary version and
// the fixed argv. The binary version is in the annotations so a tool pin
// changes when the CLI under it does.
func (p Pack) Tools(binaryVersion string) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(p.Commands))
	for _, c := range p.Commands {
		props := map[string]any{}
		var required []string
		for k, f := range c.Flags {
			props[k] = f.schema()
			if f.Required {
				required = append(required, k)
			}
		}
		for _, a := range c.Args {
			props[a.Name] = a.schema()
			if a.Required {
				required = append(required, a.Name)
			}
		}
		sort.Strings(required)
		schema := map[string]any{
			"type":                 "object",
			"properties":           props,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		argv := append(append([]string{}, c.Argv...), c.Fixed...)
		// bindings is how each input key reaches argv, in argv order (flags
		// sorted by key, then positionals), so the pin covers it.
		keys := make([]string, 0, len(c.Flags))
		for k := range c.Flags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		bindings := make([]map[string]any, 0, len(keys)+len(c.Args))
		for _, k := range keys {
			bindings = append(bindings, map[string]any{"key": k, "flag": "--" + k})
		}
		for i, a := range c.Args {
			bindings = append(bindings, map[string]any{"key": a.Name, "positional": i})
		}
		tool := map[string]any{
			"name":        p.Name + "_" + c.Name,
			"description": c.Description,
			"inputSchema": schema,
			"annotations": map[string]any{
				"readOnlyHint":        true,
				"x-aimux-pack":        p.Name + "@" + p.Version,
				"x-aimux-binary":      binaryVersion,
				"x-aimux-binary-path": p.Binary,
				"x-aimux-argv":        argv,
				"x-aimux-bindings":    bindings,
			},
		}
		raw, err := json.Marshal(tool)
		if err != nil {
			return nil, fmt.Errorf("encode tool %s_%s: %w", p.Name, c.Name, err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// schema is f as a JSON Schema property.
func (f Flag) schema() map[string]any {
	switch f.Type {
	case "int":
		return map[string]any{"type": "integer", "minimum": f.Min, "maximum": f.Max}
	case "bool":
		return map[string]any{"type": "boolean"}
	case "enum":
		return map[string]any{"type": "string", "enum": f.Values}
	default:
		return map[string]any{"type": "string", "pattern": f.anchored()}
	}
}
