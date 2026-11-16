package profile

import (
	"reflect"
	"testing"

	"github.com/zanetworker/aimux/internal/toolreg"
)

func snapshotFor(pins toolreg.PinTable) toolreg.Snapshot {
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"jira": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"}},
		Sets:    map[string]toolreg.Set{"jira:readonly": {Source: "jira", Tools: []string{"search", "get_issue"}}}}
	return toolreg.NewSnapshot(reg, pins, nil)
}

// CompileAt grants from the snapshot's registry and pins and records the
// snapshot, so the token the turn issues carries the same version.
func TestCompileAt_RecordsSnapshot(t *testing.T) {
	m := workReadonly(t)
	snap := snapshotFor(toolreg.PinTable{"jira": {"search": {Hash: "a"}, "get_issue": {Hash: "b"}}})
	c, err := CompileAt(m, snap, testGW)
	if err != nil {
		t.Fatal(err)
	}
	if c.Snapshot.Version() != snap.Version() {
		t.Errorf("compiled at %q, want %q", c.Snapshot.Version(), snap.Version())
	}
	if !reflect.DeepEqual(c.Grant.Scope(), map[string][]string{"jira": {"get_issue", "search"}}) {
		t.Errorf("grant = %v", c.Grant.Scope())
	}
}

// A source_changed pin is not granted: the gateway would hide it.
func TestCompileAt_SkipsSourceChangedTools(t *testing.T) {
	m := workReadonly(t)
	snap := snapshotFor(toolreg.PinTable{"jira": {"search": {Hash: "a", Source: "http://127.0.0.1:8001/mcp"}, "get_issue": {Hash: "b", Source: "http://old/mcp"}}})
	c, err := CompileAt(m, snap, testGW)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Grant.Scope(), map[string][]string{"jira": {"search"}}) {
		t.Errorf("grant = %v, want only search", c.Grant.Scope())
	}
}
