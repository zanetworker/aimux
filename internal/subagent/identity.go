package subagent

// Info holds provider-agnostic subagent identity.
type Info struct {
	ID       string // unique subagent identifier
	Type     string // "Explore", "Plan", custom agent name
	ParentID string // parent subagent/session ID
}

// HasIdentity returns true if any identity field is set.
func (i Info) HasIdentity() bool {
	return i.ID != "" || i.Type != ""
}

// AttrKeys tells the OTEL receiver which attribute names
// to extract for subagent identity.
type AttrKeys struct {
	ID       string // e.g. "agent_id"
	Type     string // e.g. "agent_type"
	ParentID string // e.g. "parent_agent_id"
}

// Empty returns true if no keys are configured.
func (k AttrKeys) Empty() bool {
	return k.ID == "" && k.Type == "" && k.ParentID == ""
}

// Extract reads subagent identity from a generic attribute map.
func (k AttrKeys) Extract(attrs map[string]any) Info {
	if k.Empty() {
		return Info{}
	}
	str := func(key string) string {
		if key == "" {
			return ""
		}
		v, _ := attrs[key].(string)
		return v
	}
	return Info{
		ID:       str(k.ID),
		Type:     str(k.Type),
		ParentID: str(k.ParentID),
	}
}
