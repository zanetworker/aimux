package subagent

import "testing"

func TestAttrKeysEmpty(t *testing.T) {
	var k AttrKeys
	if !k.Empty() {
		t.Error("zero-value AttrKeys should be empty")
	}
	k = AttrKeys{ID: "agent_id"}
	if k.Empty() {
		t.Error("AttrKeys with ID set should not be empty")
	}
}

func TestAttrKeysExtract(t *testing.T) {
	keys := AttrKeys{ID: "agent_id", Type: "agent_type", ParentID: "parent_agent_id"}
	attrs := map[string]any{
		"agent_id":        "sub-123",
		"agent_type":      "Explore",
		"parent_agent_id": "main-456",
		"other_field":     "ignored",
	}
	info := keys.Extract(attrs)
	if info.ID != "sub-123" {
		t.Errorf("ID = %q, want %q", info.ID, "sub-123")
	}
	if info.Type != "Explore" {
		t.Errorf("Type = %q, want %q", info.Type, "Explore")
	}
	if info.ParentID != "main-456" {
		t.Errorf("ParentID = %q, want %q", info.ParentID, "main-456")
	}
}

func TestAttrKeysExtractMissingAttrs(t *testing.T) {
	keys := AttrKeys{ID: "agent_id", Type: "agent_type", ParentID: "parent_agent_id"}
	info := keys.Extract(map[string]any{"unrelated": "value"})
	if info.ID != "" || info.Type != "" || info.ParentID != "" {
		t.Errorf("expected zero Info, got %+v", info)
	}
}

func TestAttrKeysExtractEmptyKeys(t *testing.T) {
	var keys AttrKeys
	info := keys.Extract(map[string]any{"agent_id": "sub-123"})
	if info.ID != "" {
		t.Errorf("empty keys should produce zero Info, got ID=%q", info.ID)
	}
}

func TestInfoHasIdentity(t *testing.T) {
	if (Info{}).HasIdentity() {
		t.Error("zero Info should not have identity")
	}
	if !(Info{ID: "x"}).HasIdentity() {
		t.Error("Info with ID should have identity")
	}
	if !(Info{Type: "Explore"}).HasIdentity() {
		t.Error("Info with Type should have identity")
	}
}
