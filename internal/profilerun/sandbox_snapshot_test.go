package profilerun

import (
	"context"
	"reflect"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// The turn's token is issued under the snapshot the turn compiled with: when
// the gateway already serves a newer snapshot, the token loses only the
// tool whose pin changed in between, and keeps the rest.
func TestSandboxEngine_TokenCarriesTheCompiledSnapshot(t *testing.T) {
	pins := toolreg.PinTable{"jira": {"search": {Hash: "j1"}}, "wiki": {"read_page": {Hash: "w1"}}}
	st := &toolreg.Store{LoadRegistry: func() (toolreg.Registry, error) { return toolreg.Registry{Version: 1}, nil },
		LoadPins: func() (toolreg.PinTable, error) { return pins, nil }}
	compiledAt := st.Current()
	pins = toolreg.PinTable{"jira": {"search": {Hash: "j2"}}, "wiki": {"read_page": {Hash: "w1"}}}
	if _, changed, err := st.Reload(); err != nil || !changed {
		t.Fatalf("reload: %v %v", changed, err)
	}

	e, b := newSandbox(t)
	b.tokens.Current = st.Current // the gateway serves the reloaded snapshot
	e.Compile = func(p string) (profile.Compiled, error) {
		c := compiledFor(p)
		c.Snapshot = compiledAt
		return c, nil
	}
	s, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendCollect(t, e, s, "q"); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"jira": {}, "wiki": {"read_page"}}
	if got := b.execs[0].allow; !reflect.DeepEqual(got, want) {
		t.Fatalf("token allowed %v during the turn, want %v", got, want)
	}
}
