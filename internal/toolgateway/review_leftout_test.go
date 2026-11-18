package toolgateway

import (
	"reflect"
	"testing"
)

func held(src string, tools map[string]string) []ToolStatus {
	var out []ToolStatus
	for _, t := range []string{"a", "b", "c", "d", "e"} {
		if st, ok := tools[t]; ok {
			out = append(out, ToolStatus{Source: src, Tool: t, Status: st})
		}
	}
	return out
}

// A server is left out of a turn only when every tool granted on it is
// held back; one granted tool still served keeps it.
func TestLeftOut_OnlyServersWithEveryGrantedToolHeld(t *testing.T) {
	h := append(held("slack", map[string]string{"a": StatusChanged, "b": StatusChanged}),
		held("jira", map[string]string{"a": StatusChanged, "e": StatusNewOnServer})...)
	grant := map[string][]string{"slack": {"a", "b"}, "jira": {"a", "b"}, "wiki": {"a"}}
	if got := LeftOut(h, grant); !reflect.DeepEqual(got, []string{"slack"}) {
		t.Fatalf("left out %v, want [slack] (jira still serves b, wiki holds nothing back)", got)
	}
	if got := LeftOut(nil, grant); len(got) != 0 {
		t.Fatalf("nothing held: left out %v", got)
	}
	// A server granted no tools is never left out by this rule.
	if got := LeftOut(h, map[string][]string{"slack": {}}); len(got) != 0 {
		t.Fatalf("empty grant: left out %v", got)
	}
}

func TestHeldBackNotice_NamesALeftOutServer(t *testing.T) {
	h := held("slack", map[string]string{"a": StatusChanged, "b": StatusChanged, "c": StatusChanged, "d": StatusChanged, "e": StatusChanged})
	want := "slack: 5 tools changed, held for review, left out of this turn: run aimux tools status"
	if got := HeldBackNotice(h, []string{"slack"}); got != want {
		t.Fatalf("notice\n got %q\nwant %q", got, want)
	}
	mixed := append(held("slack", map[string]string{"a": StatusChanged, "b": StatusSourceChanged}),
		held("jira", map[string]string{"c": StatusChanged})...)
	want = "slack: 2 tools (1 changed, 1 source changed), held for review, left out of this turn; " +
		"1 tool held for review (jira: c changed): run aimux tools status"
	if got := HeldBackNotice(mixed, []string{"slack"}); got != want {
		t.Fatalf("notice\n got %q\nwant %q", got, want)
	}
	one := held("slack", map[string]string{"a": StatusChanged})
	if got := HeldBackNotice(one, []string{"slack"}); got != "slack: 1 tool changed, held for review, left out of this turn: run aimux tools status" {
		t.Fatalf("one tool: %q", got)
	}
}
