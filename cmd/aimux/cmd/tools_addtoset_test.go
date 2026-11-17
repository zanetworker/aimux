package cmd

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// A tool the server offers that no set lists shows as new_on_server, and
// approve --add-to-set adds it to the set and pins it in one step.
func TestToolsApprove_AddToSet_PinsAndAdds(t *testing.T) {
	r := newToolsRig(t)
	r.live["jira"] = append(r.live["jira"], rawTool("fresh", "string"))
	st, err := runToolsOK(t, r.env, "status")
	if err != nil || !strings.Contains(st, "new_on_server") || !strings.Contains(st, "fresh") {
		t.Fatalf("status lacks the new tool: %v\n%s", err, st)
	}
	out, stderr, err := runTools(t, r.env, "approve", "jira/fresh", "--add-to-set", "jira:readonly")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, stderr)
	}
	if !strings.Contains(out, `"name":"fresh"`) || !strings.Contains(out, "approved jira:readonly/fresh") {
		t.Fatalf("output lacks the definition or the approval:\n%s", out)
	}
	reg, err := toolreg.Load(toolreg.DefaultPath(r.env.Home))
	if err != nil || !slices.Contains(reg.Sets["jira:readonly"].Tools, "fresh") {
		t.Fatalf("set tools %v (%v)", reg.Sets["jira:readonly"].Tools, err)
	}
	pins, err := (toolgateway.PinStore{Path: r.pinsPath()}).LoadFile()
	if err != nil || pins["jira"]["fresh"].Hash == "" {
		t.Fatalf("fresh not pinned: %v", err)
	}
	if !strings.Contains(stderr, "does not mark this tool read-only") {
		t.Fatalf("no read-only warning for a tool without readOnlyHint:\n%s", stderr)
	}
}

func TestToolsApprove_AddToSet_ReadOnlyHintNoWarning(t *testing.T) {
	r := newToolsRig(t)
	r.live["jira"] = append(r.live["jira"], json.RawMessage(`{"name":"fresh","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`))
	_, stderr, err := runTools(t, r.env, "approve", "jira/fresh", "--add-to-set", "jira:readonly")
	if err != nil || strings.Contains(stderr, "read-only") {
		t.Fatalf("err %v stderr %q", err, stderr)
	}
}

func TestToolsApprove_AddToSet_Refusals(t *testing.T) {
	r := newToolsRig(t)
	r.live["jira"] = append(r.live["jira"], rawTool("fresh", "string"))
	before, _ := os.ReadFile(toolreg.DefaultPath(r.env.Home))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"approve", "slack/fresh", "--add-to-set", "jira:readonly"}, "set jira:readonly lists tools of jira"},
		{[]string{"approve", "jira/nope", "--add-to-set", "jira:readonly"}, "jira does not offer nope"},
		{[]string{"approve", "jira/fresh", "--add-to-set", "nope:readonly"}, "no set nope:readonly"},
		{[]string{"approve", "jira/fresh", "jira/get", "--add-to-set", "jira:readonly"}, "one <source>/<tool>"},
		{[]string{"approve", "--all", "--add-to-set", "jira:readonly"}, "one <source>/<tool>"},
	} {
		_, _, err := runTools(t, r.env, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err %v, want %q", tc.args, err, tc.want)
		}
	}
	after, _ := os.ReadFile(toolreg.DefaultPath(r.env.Home))
	if string(before) != string(after) {
		t.Fatal("a refused --add-to-set changed tool-sources.yaml")
	}
	if _, err := os.Stat(r.pinsPath()); err == nil {
		t.Fatal("a refused --add-to-set wrote pins")
	}
}

// The definition approve shows is the one it pins: a server that changes
// the tool between the two listings is refused.
func TestToolsApprove_AddToSet_PinsTheDefinitionShown(t *testing.T) {
	r := newToolsRig(t)
	r.live["jira"] = append(r.live["jira"], rawTool("fresh", "string"))
	calls := 0
	inner := r.env.Catalog
	r.env.Catalog = func(ctx context.Context, cfg mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error) {
		calls++
		if calls == 2 { // the listing approve pins from: the server changed the tool
			r.live["jira"][len(r.live["jira"])-1] = rawTool("fresh", "number")
		}
		return inner(ctx, cfg)
	}
	_, _, err := runTools(t, r.env, "approve", "jira/fresh", "--add-to-set", "jira:readonly")
	if err == nil {
		t.Fatal("pinned a definition other than the one shown")
	}
	if pins, _ := (toolgateway.PinStore{Path: r.pinsPath()}).LoadFile(); pins["jira"]["fresh"].Hash != "" {
		t.Fatal("fresh was pinned")
	}
	if reg, _ := toolreg.Load(toolreg.DefaultPath(r.env.Home)); slices.Contains(reg.Sets["jira:readonly"].Tools, "fresh") {
		t.Fatal("a refused approve left fresh in the set")
	}
}

func TestToolsApprove_AddToSet_JSONStaysValid(t *testing.T) {
	r := newToolsRig(t)
	r.live["jira"] = append(r.live["jira"], rawTool("fresh", "string"))
	out, stderr, err := runTools(t, r.env, "--json", "approve", "jira/fresh", "--add-to-set", "jira:readonly")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if !strings.Contains(stderr, `"name":"fresh"`) {
		t.Fatalf("the definition preview should go to stderr in --json mode:\n%s", stderr)
	}
}
