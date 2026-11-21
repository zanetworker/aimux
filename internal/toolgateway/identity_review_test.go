package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolreg"
)

// splitRegistry is the registry a managed jira server running exe with
// args gets once the supervisor can vouch for its process and the loader
// stops holding managed sets (ruling b): its source and set, and its
// identity. Built directly, since the loader holds managed sets for now.
func splitRegistry(t *testing.T, _ string, exe string, args ...string) toolreg.Registry {
	t.Helper()
	srv := toolreg.Server{Command: append([]string{exe}, args...), URL: "http://127.0.0.1:8001/mcp"}
	id, err := srv.Identity(toolreg.HashFile)
	if err != nil {
		t.Fatal(err)
	}
	return toolreg.Registry{Version: 1,
		Sources:    map[string]toolreg.Source{"jira": {Type: toolreg.TypeMCPHTTP, URL: srv.URL}},
		Sets:       map[string]toolreg.Set{"jira:readonly": {Source: "jira", Tools: []string{"search"}}},
		Identities: map[string]string{"jira": id}}
}

func searchLive() map[string]LiveSource {
	return map[string]LiveSource{"jira": {Tools: map[string]json.RawMessage{"search": tool("search", "string")}}}
}

// Review Focus 4: the owner changes a managed server's args after approving
// its tools; the gateway serves none of them until approved again.
func TestReview_ArgsChangedIsSourceChanged(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jira-mcp")
	if err := os.WriteFile(exe, []byte("v1"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	before := splitRegistry(t, dir, exe, "--port", "8001")
	pins, _, err := Approve(before, PinFile{}, searchLive(), []string{"jira:readonly"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if src := pins["jira"]["search"].Source; !strings.HasPrefix(src, "id:") {
		t.Fatalf("approval recorded %q, want the server identity", src)
	}
	if st := Review(before, pins, searchLive()); st[0].Status != StatusActive {
		t.Fatalf("before the change: %+v", st[0])
	}

	after := splitRegistry(t, dir, exe, "--port", "8001", "--debug")
	st := Review(after, pins, searchLive())
	if st[0].Status != StatusSourceChanged || !st[0].NeedsReview() || !strings.Contains(st[0].Reason, "servers.yaml") {
		t.Fatalf("after the args change: %+v, want source_changed naming servers.yaml", st[0])
	}
	snap := toolreg.NewSnapshot(after, pins.Table(), nil)
	if _, ok := ServedPins(snap)["jira"]["search"]; ok {
		t.Fatal("the gateway must not serve a tool whose server changed")
	}

	again, _, err := Approve(after, pins, searchLive(), []string{"jira:readonly"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st := Review(after, again, searchLive()); st[0].Status != StatusActive {
		t.Fatalf("after approving again: %+v", st[0])
	}
}

// A v2 pin recorded the server's URL; it still matches the same server,
// and the next approval rewrites it to the identity.
func TestReview_V2EndpointPinStillMatches(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jira-mcp")
	if err := os.WriteFile(exe, []byte("v1"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	reg := splitRegistry(t, dir, exe)
	pins := PinFile{"jira": {"search": record(t, tool("search", "string"), t0)}} // Source: the jira URL
	if st := Review(reg, pins, searchLive()); st[0].Status != StatusActive {
		t.Fatalf("endpoint pin: %+v, want active", st[0])
	}
	out, _, err := Approve(reg, pins, searchLive(), []string{"jira:readonly"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if src := out["jira"]["search"].Source; src != reg.PinSource("jira") {
		t.Fatalf("after approve: source %q, want the identity %q", src, reg.PinSource("jira"))
	}
}

// A listing the gateway made of an earlier definition of a server (same
// URL, another binary) says nothing about the server now: it is unknown,
// so an approval never pins the old binary's tools under the new identity.
func TestLiveFor_ListingOfAnotherIdentityIsUnknown(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jira-mcp")
	if err := os.WriteFile(exe, []byte("v1"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	before := splitRegistry(t, dir, exe)
	obs := map[string]Observed{"jira": {LiveSource: searchLive()["jira"], Endpoint: before.PinSource("jira")}}
	if _, ok := LiveFor(before, obs)["jira"]; !ok {
		t.Fatal("a listing of the same identity is live")
	}
	if err := os.WriteFile(exe, []byte("v2"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	after := splitRegistry(t, dir, exe)
	if _, ok := LiveFor(after, obs)["jira"]; ok {
		t.Fatal("a listing of another identity must be unknown")
	}
}

// A guard built under an older snapshot refuses a call once the applied
// snapshot gives its server another identity, before the rebuild replaces
// it; under the same identity the call is checked as usual.
func TestGuard_RefusesOnceAppliedIdentityChanges(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jira-mcp")
	if err := os.WriteFile(exe, []byte("v1"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	reg := splitRegistry(t, dir, exe)
	pins, _, err := Approve(reg, PinFile{}, searchLive(), []string{"jira:readonly"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	old := toolreg.NewSnapshot(reg, pins.Table(), nil)
	if err := os.WriteFile(exe, []byte("v2"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	next := toolreg.NewSnapshot(splitRegistry(t, dir, exe), pins.Table(), nil)

	list := func(context.Context, string) ([]json.RawMessage, error) {
		return []json.RawMessage{tool("search", "string")}, nil
	}
	p := GatewayPins(old)
	applied := old
	p.applied = func() toolreg.Snapshot { return applied }
	guard := p.Guard(list)
	if err := guard(context.Background(), "jira", "search"); err != nil {
		t.Fatalf("same identity: %v", err)
	}
	applied = next
	if err := guard(context.Background(), "jira", "search"); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("after the identity changed: %v, want ErrSourceChanged", err)
	}
}

// approve rewriting a URL pin to the identity it already matched (format
// only) keeps a running turn's lease; the same rewrite across a real
// identity change drops the tool.
func TestTokens_URLPinUpgradeKeepsLease(t *testing.T) {
	const url = "http://127.0.0.1:8001/mcp"
	idA, idB := "id:"+strings.Repeat("a", 64), "id:"+strings.Repeat("b", 64)
	reg := func(id string) toolreg.Registry {
		r := staleRegistry()
		r.Identities = map[string]string{"jira": id}
		return r
	}
	pins := func(src string) toolreg.PinTable {
		return toolreg.PinTable{"jira": {"keep": {Hash: "k", Source: src}}}
	}
	for _, c := range []struct {
		name    string
		next    string
		pin     string
		allowed bool
	}{{"same identity", idA, idA, true}, {"identity changed", idB, idB, false}, {"URL pin kept, unrelated change", idA, url, true}} {
		m := newMemStore(reg(idA), pins(url))
		old := m.Current()
		var tk Tokens
		var current toolreg.Snapshot
		tk.Current = func() toolreg.Snapshot { return current }
		tok, err := tk.Issue("s", map[string][]string{"jira": {"keep"}}, old)
		if err != nil {
			t.Fatal(err)
		}
		m.reg, m.pins = reg(c.next), pins(c.pin)
		m.reg.Sets["other:readonly"] = toolreg.Set{Source: "slack", Tools: []string{"x"}} // a new version either way
		current = m.reload(t)
		_, got, _ := tk.Lookup(tok)
		if allowed := len(got["jira"]) == 1; allowed != c.allowed {
			t.Errorf("%s: allowed = %v, want %v", c.name, allowed, c.allowed)
		}
	}
}

// A refresh that applies a new identity while the guard is listing (the
// call's live check) is caught after the listing too.
func TestGuard_IdentityChangeDuringCheck(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jira-mcp")
	if err := os.WriteFile(exe, []byte("v1"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	reg := splitRegistry(t, dir, exe)
	pins, _, err := Approve(reg, PinFile{}, searchLive(), []string{"jira:readonly"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	old := toolreg.NewSnapshot(reg, pins.Table(), nil)
	next := toolreg.NewSnapshot(splitRegistry(t, dir, exe, "--other"), pins.Table(), nil)
	applied := old
	list := func(context.Context, string) ([]json.RawMessage, error) {
		applied = next // the refresh lands mid-check
		return []json.RawMessage{tool("search", "string")}, nil
	}
	p := GatewayPins(old)
	p.applied = func() toolreg.Snapshot { return applied }
	if err := p.Guard(list)(context.Background(), "jira", "search"); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("err = %v, want ErrSourceChanged", err)
	}
}

// aimux servers migrate during a turn: the lease was issued under the
// single-file registry (URL sources), the new snapshot has identities and
// the same URL pins. The turn keeps its tools.
func TestTokens_MigrationKeepsLease(t *testing.T) {
	const url = "http://127.0.0.1:8001/mcp"
	m := newMemStore(staleRegistry(), toolreg.PinTable{"jira": {"keep": {Hash: "k", Source: url}}})
	old := m.Current()
	var tk Tokens
	var current toolreg.Snapshot
	tk.Current = func() toolreg.Snapshot { return current }
	tok, err := tk.Issue("s", map[string][]string{"jira": {"keep"}}, old)
	if err != nil {
		t.Fatal(err)
	}
	m.reg = staleRegistry()
	m.reg.Identities = map[string]string{"jira": "id:" + strings.Repeat("a", 64)}
	current = m.reload(t)
	if _, got, _ := tk.Lookup(tok); len(got["jira"]) != 1 {
		t.Fatalf("migration dropped the tool: %v", got)
	}
}
