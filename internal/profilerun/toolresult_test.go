package profilerun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
)

// A sandbox turn hands each tool result to the turn's collector and never
// to the client: the client's events carry no tool_result update.
func TestSandboxEngine_ToolResultsReachCollectorNotClient(t *testing.T) {
	e, _ := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []ToolResult
	ctx := WithToolResults(context.Background(), func(r ToolResult) { got = append(got, r) })
	var kinds []string
	if _, err := e.Send(ctx, sid, "q", func(ev acprun.Event) {
		var u struct {
			Kind string `json:"sessionUpdate"`
		}
		_ = json.Unmarshal(ev.Data, &u)
		kinds = append(kinds, u.Kind)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "mcp__jira__search" || got[0].Output != "no access" || !got[0].Failed {
		t.Fatalf("collector got %+v", got)
	}
	for _, k := range kinds {
		if strings.Contains(k, "result") {
			t.Fatalf("client saw a %q update", k)
		}
	}
	// Without a collector the turn runs as before.
	sid2, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if _, evs, err := sendCollect(t, e, sid2, "q"); err != nil || len(evs) == 0 {
		t.Fatalf("turn without a collector: %v, %d events", err, len(evs))
	}
}
