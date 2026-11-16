package httpsource_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// The pin covers the method and path template (x-aimux-http) and the base
// URL, not the description.
func TestTools_HashIncludesMethodAndPath(t *testing.T) {
	op := httpsource.Operation{ID: "issues_list", Path: "/repos/{owner}/{repo}/issues", Description: "List issues", Params: []httpsource.Param{
		{Name: "owner", In: "path", Type: "string", Required: true},
		{Name: "state", In: "query", Type: "string", Enum: []string{"open", "closed"}},
	}}
	hash := func(o httpsource.Operation, base string) string {
		t.Helper()
		raws, err := httpsource.Tools("github", base, []httpsource.Operation{o})
		if err != nil || len(raws) != 1 {
			t.Fatalf("tools: %v %d", err, len(raws))
		}
		h, err := toolgateway.PinTool(raws[0])
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := hash(op, "https://api.github.com")
	moved := op
	moved.Path = "/repos/{owner}/{repo}/pulls"
	if hash(moved, "https://api.github.com") == h {
		t.Error("a new path template kept the pin")
	}
	if hash(op, "https://api.example.com") == h {
		t.Error("a new base URL kept the pin")
	}
	described := op
	described.Description = "something else"
	if hash(described, "https://api.github.com") != h {
		t.Error("a description change changed the pin")
	}

	raws, err := httpsource.Tools("github", "https://api.github.com", []httpsource.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	var tool struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Annotations map[string]any `json:"annotations"`
		InputSchema struct {
			Properties           map[string]map[string]any `json:"properties"`
			Required             []string                  `json:"required"`
			AdditionalProperties bool                      `json:"additionalProperties"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(raws[0], &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Name != "github_issues_list" || tool.Description != "List issues" || tool.Annotations["readOnlyHint"] != true ||
		tool.Annotations["x-aimux-http"] != "GET /repos/{owner}/{repo}/issues" || tool.Annotations["x-aimux-base"] != "https://api.github.com" {
		t.Fatalf("tool = %s", raws[0])
	}
	if tool.InputSchema.AdditionalProperties || !reflect.DeepEqual(tool.InputSchema.Required, []string{"owner"}) ||
		tool.InputSchema.Properties["state"]["type"] != "string" || len(tool.InputSchema.Properties) != 2 {
		t.Fatalf("schema = %s", raws[0])
	}
}

// Enums reach the input schema in their parameter's type.
func TestTools_EnumSchemaTypes(t *testing.T) {
	op := httpsource.Operation{ID: "a", Path: "/a", Params: []httpsource.Param{
		{Name: "flag", In: "query", Type: "boolean", Enum: []string{"true"}},
		{Name: "n", In: "query", Type: "integer", Enum: []string{"1", "2"}},
	}}
	raws, err := httpsource.Tools("s", "https://h.example.com", []httpsource.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	var tool struct {
		InputSchema struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(raws[0], &tool); err != nil {
		t.Fatal(err)
	}
	if got := tool.InputSchema.Properties["flag"]["enum"]; !reflect.DeepEqual(got, []any{true}) {
		t.Errorf("flag enum = %v", got)
	}
	if got := tool.InputSchema.Properties["n"]["enum"]; !reflect.DeepEqual(got, []any{float64(1), float64(2)}) {
		t.Errorf("n enum = %v", got)
	}
}
