package clipack

import (
	"encoding/json"
	"reflect"
	"testing"
)

type toolShape struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type                 string                    `json:"type"`
		Properties           map[string]map[string]any `json:"properties"`
		Required             []string                  `json:"required"`
		AdditionalProperties *bool                     `json:"additionalProperties"`
	} `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

func toolByName(t *testing.T, tools []json.RawMessage, name string) (toolShape, json.RawMessage) {
	t.Helper()
	for _, raw := range tools {
		var s toolShape
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.Name == name {
			return s, raw
		}
	}
	t.Fatalf("no tool %s", name)
	return toolShape{}, nil
}

func TestTools_ShapeAndVersionInAnnotations(t *testing.T) {
	p := gh(t)
	a, err := p.Tools("2.103.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(p.Commands) {
		t.Fatalf("%d tools for %d commands", len(a), len(p.Commands))
	}
	s, _ := toolByName(t, a, "gh_issue_list")
	if s.Description == "" || s.InputSchema.Type != "object" {
		t.Errorf("shape %+v", s)
	}
	if s.InputSchema.AdditionalProperties == nil || *s.InputSchema.AdditionalProperties {
		t.Error("additionalProperties should be false")
	}
	if !reflect.DeepEqual(s.InputSchema.Required, []string{"repo"}) {
		t.Errorf("required %v", s.InputSchema.Required)
	}
	props := s.InputSchema.Properties
	if props["repo"]["type"] != "string" || props["repo"]["pattern"] == nil {
		t.Errorf("repo %v", props["repo"])
	}
	if props["limit"]["type"] != "integer" || props["limit"]["minimum"] != float64(1) || props["limit"]["maximum"] != float64(100) {
		t.Errorf("limit %v", props["limit"])
	}
	if !reflect.DeepEqual(props["state"]["enum"], []any{"open", "closed", "all"}) || props["state"]["type"] != "string" {
		t.Errorf("state %v", props["state"])
	}
	ann := s.Annotations
	if ann["readOnlyHint"] != true || ann["x-aimux-pack"] != "gh@2.103" || ann["x-aimux-binary"] != "2.103.3" {
		t.Errorf("annotations %v", ann)
	}
	if !reflect.DeepEqual(ann["x-aimux-argv"], []any{"issue", "list", "--json", "number,title,state,url"}) {
		t.Errorf("x-aimux-argv %v", ann["x-aimux-argv"])
	}
	v, _ := toolByName(t, a, "gh_issue_view")
	if v.InputSchema.Properties["number"]["type"] != "integer" || !reflect.DeepEqual(v.InputSchema.Required, []string{"number", "repo"}) {
		t.Errorf("issue_view schema %+v", v.InputSchema)
	}
	r, _ := toolByName(t, a, "gh_release_list")
	if r.InputSchema.Properties["exclude-drafts"]["type"] != "boolean" {
		t.Errorf("release_list %v", r.InputSchema.Properties)
	}
}

func TestTools_BinaryPathInAnnotations(t *testing.T) {
	p := gh(t)
	a, err := p.Tools("2.103.3")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := toolByName(t, a, "gh_issue_list")
	if s.Annotations["x-aimux-binary-path"] != "/usr/bin/gh" {
		t.Errorf("annotations %v", s.Annotations)
	}
}

func TestTools_BindingsInAnnotations(t *testing.T) {
	p := gh(t)
	tools, err := p.Tools("1")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := toolByName(t, tools, "gh_issue_view")
	want := []any{
		map[string]any{"key": "repo", "flag": "--repo"},
		map[string]any{"key": "number", "positional": float64(0)},
	}
	if !reflect.DeepEqual(s.Annotations["x-aimux-bindings"], want) {
		t.Errorf("bindings %v", s.Annotations["x-aimux-bindings"])
	}
}
