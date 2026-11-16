package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// PinFile.Table carries each record's hash, recorded source and approval
// time, never its definition.
func TestPinFileTable(t *testing.T) {
	f := PinFile{"jira": {"a": record(t, tool("a", "string"), t0), "b": {Hash: "h"}}}
	got := f.Table()
	if got["jira"]["a"] != (toolreg.Pin{Hash: f["jira"]["a"].Hash, Source: "http://127.0.0.1:8001/mcp", ApprovedAt: t0}) || got["jira"]["b"] != (toolreg.Pin{Hash: "h"}) {
		t.Fatalf("table = %+v", got)
	}
}

// PinsOf is a snapshot's pins as Review reads them: hash, source and
// approval time of each, no definition (a snapshot keeps none).
func TestPinsOf(t *testing.T) {
	f := PinFile{"jira": {"a": record(t, tool("a", "string"), t0), "b": {Hash: "h"}}}
	got := PinsOf(toolreg.NewSnapshot(toolreg.Registry{}, f.Table(), nil))
	want := PinFile{"jira": {"a": {Hash: f["jira"]["a"].Hash, Source: "http://127.0.0.1:8001/mcp", ApprovedAt: t0}, "b": {Hash: "h"}}}
	if len(got) != 1 || len(got["jira"]) != 2 || got["jira"]["a"].Hash != want["jira"]["a"].Hash || got["jira"]["a"].Source != want["jira"]["a"].Source ||
		!got["jira"]["a"].ApprovedAt.Equal(t0) || got["jira"]["a"].Definition != nil || got["jira"]["b"].Hash != "h" || !got["jira"]["b"].ApprovedAt.IsZero() {
		t.Fatalf("PinsOf = %+v", got)
	}
	if empty := PinsOf(toolreg.NewSnapshot(toolreg.Registry{}, nil, errors.New("pins unreadable"))); len(empty) != 0 {
		t.Fatalf("PinsOf a failed load = %+v, want none", empty)
	}
}

// NewStore reads the registry and pins under home; a pin change is a new
// version after Reload, an approved_at-only change is not.
func TestNewStore_ReadsHome(t *testing.T) {
	home := t.TempDir()
	ps := PinStore{Path: DefaultPinsPath(home)}
	rec := record(t, tool("search_issues_jql", "string"), t0)
	if err := ps.Save(PinFile{"jira": {"search_issues_jql": rec}}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(home)
	s := st.Current()
	if _, ok := s.Registry().Sources["jira"]; !ok {
		t.Fatal("default registry not loaded")
	}
	if p, ok := s.Pin("jira", "search_issues_jql"); !ok || p.Hash != rec.Hash {
		t.Fatalf("pin = %+v %v", p, ok)
	}
	rec.ApprovedAt = rec.ApprovedAt.Add(1)
	if err := ps.Save(PinFile{"jira": {"search_issues_jql": rec}}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := st.Reload(); err != nil || changed {
		t.Fatalf("approved_at only: changed=%v err=%v", changed, err)
	}
	rec.Hash = "sha256:other"
	if err := ps.Save(PinFile{"jira": {"search_issues_jql": rec}}); err != nil {
		t.Fatal(err)
	}
	if n, changed, err := st.Reload(); err != nil || !changed || n.Version() == s.Version() {
		t.Fatalf("hash change: changed=%v err=%v", changed, err)
	}
	if err := writeRaw(filepath.Join(home, ".aimux", "tools", "pins.json"), "{bad"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Reload(); err == nil {
		t.Fatal("a broken pins file must fail the reload")
	}
}

// The gateway serves a snapshot's pins without the source_changed ones:
// Filter hides them and the call guard refuses them with ErrSourceChanged,
// while the other tools pass.
func TestGatewayPins_SourceChangedHiddenAndRefused(t *testing.T) {
	ok := tool("ok", "string")
	moved := tool("moved", "string")
	reg := toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"jira": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"}},
		Sets: map[string]toolreg.Set{"jira:readonly": {Source: "jira", Tools: []string{"ok", "moved"}}}}
	pins := toolreg.PinTable{"jira": {
		"ok":    {Hash: mustPin(t, string(ok)), Source: "http://127.0.0.1:8001/mcp"},
		"moved": {Hash: mustPin(t, string(moved)), Source: "http://127.0.0.1:7000/mcp"},
	}}
	gp := GatewayPins(toolreg.NewSnapshot(reg, pins, nil))
	cat := &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{"jira": {Tools: []json.RawMessage{ok, moved}}}}
	kept := gp.Filter(cat).Servers["jira"].Tools
	if len(kept) != 1 || string(kept[0]) != string(ok) {
		t.Fatalf("filter kept %s", kept)
	}
	guard := gp.Guard(func(context.Context, string) ([]json.RawMessage, error) { return []json.RawMessage{ok, moved}, nil })
	if err := guard(context.Background(), "jira", "ok"); err != nil {
		t.Errorf("ok refused: %v", err)
	}
	if err := guard(context.Background(), "jira", "moved"); !errors.Is(err, ErrSourceChanged) {
		t.Errorf("moved = %v, want ErrSourceChanged", err)
	}
	if served := ServedPins(toolreg.NewSnapshot(reg, pins, nil)); len(served["jira"]) != 1 || served["jira"]["ok"] == "" {
		t.Errorf("served pins = %v", served)
	}
}

func writeRaw(p, body string) error { return os.WriteFile(p, []byte(body), 0o600) }
