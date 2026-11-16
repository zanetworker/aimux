package toolreg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func snapReg() Registry {
	return Registry{Version: 1,
		Sources: map[string]Source{"jira": {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"}, "notes": {Type: TypeLocalNotes, Root: "/n"}},
		Sets:    map[string]Set{"jira:readonly": {Source: "jira", Tools: []string{"b", "a"}}},
	}
}

func snapPins() PinTable {
	return PinTable{"jira": {"a": {Hash: "sha256:1", Source: "http://127.0.0.1:8001/mcp"}, "b": {Hash: "sha256:2"}}}
}

// Identical inputs give the same version, whatever the map or tool order;
// any registry or pin change gives another.
func TestSnapshotVersion_StableAndSensitive(t *testing.T) {
	base := NewSnapshot(snapReg(), snapPins(), nil).Version()
	if base == "" || base != NewSnapshot(snapReg(), snapPins(), nil).Version() {
		t.Fatalf("version not stable: %q", base)
	}
	reordered := snapReg()
	reordered.Sets["jira:readonly"] = Set{Source: "jira", Tools: []string{"a", "b"}}
	if v := NewSnapshot(reordered, snapPins(), nil).Version(); v != base {
		t.Errorf("tool order changed the version: %s != %s", v, base)
	}
	edits := map[string]func(*Registry, PinTable){
		"source url": func(r *Registry, _ PinTable) {
			r.Sources["jira"] = Source{Type: TypeMCPHTTP, URL: "http://127.0.0.1:9/mcp"}
		},
		"set tools": func(r *Registry, _ PinTable) { r.Sets["jira:readonly"] = Set{Source: "jira", Tools: []string{"a"}} },
		"new set":   func(r *Registry, _ PinTable) { r.Sets["notes:readonly"] = Set{Source: "notes", Tools: []string{"x"}} },
		"pin hash": func(_ *Registry, p PinTable) {
			p["jira"]["a"] = Pin{Hash: "sha256:9", Source: "http://127.0.0.1:8001/mcp"}
		},
		"pin source":  func(_ *Registry, p PinTable) { p["jira"]["b"] = Pin{Hash: "sha256:2", Source: "http://x/mcp"} },
		"pin removed": func(_ *Registry, p PinTable) { delete(p["jira"], "b") },
	}
	for name, edit := range edits {
		r, p := snapReg(), snapPins()
		edit(&r, p)
		if v := NewSnapshot(r, p, nil).Version(); v == base {
			t.Errorf("%s: version did not change", name)
		}
	}
}

// The snapshot copies its inputs and hands out copies: changing either
// afterwards changes nothing in it.
func TestSnapshot_Immutable(t *testing.T) {
	r, p := snapReg(), snapPins()
	s := NewSnapshot(r, p, nil)
	v := s.Version()
	r.Sources["jira"] = Source{Type: TypeMCPHTTP, URL: "http://evil/mcp"}
	r.Sets["jira:readonly"].Tools[0] = "z"
	p["jira"]["a"] = Pin{Hash: "x"}
	got := s.Registry()
	got.Sources["slack"] = Source{}
	got.Sets["jira:readonly"].Tools[0] = "y"
	if s.Version() != v || s.Registry().Sources["jira"].URL != "http://127.0.0.1:8001/mcp" || len(s.Registry().Sources) != 2 {
		t.Errorf("snapshot changed through its inputs or outputs: %+v", s.Registry())
	}
	if pin, _ := s.Pin("jira", "a"); pin.Hash != "sha256:1" {
		t.Errorf("pin changed through its input: %+v", pin)
	}
	if tools := s.Registry().Sets["jira:readonly"].Tools; tools[0] == "y" || tools[0] == "z" {
		t.Errorf("set tools shared: %v", tools)
	}
}

// A pin recorded from another endpoint than the source's current one is
// source_changed; an empty recorded source (v1) is unknown and never is.
func TestSnapshot_SourceChanged(t *testing.T) {
	p := snapPins()
	p["jira"]["c"] = Pin{Hash: "sha256:3", Source: "http://127.0.0.1:7000/mcp"}
	p["notes"] = map[string]Pin{"r": {Hash: "h", Source: "/n"}}
	s := NewSnapshot(snapReg(), p, nil)
	for _, c := range []struct {
		server, tool string
		want         bool
	}{
		{"jira", "a", false}, // same URL
		{"jira", "b", false}, // v1: unknown
		{"jira", "c", true},  // another URL
		{"notes", "r", false},
		{"jira", "missing", false},
	} {
		if got := s.SourceChanged(c.server, c.tool); got != c.want {
			t.Errorf("SourceChanged(%s, %s) = %v, want %v", c.server, c.tool, got, c.want)
		}
	}
	if !snapReg().SourceChanged("gone", "http://x") || snapReg().SourceChanged("gone", "") {
		t.Error("a pin on a source the registry no longer has is changed unless unknown")
	}
}

func TestSnapshot_Before(t *testing.T) {
	st := testStore(t, snapReg(), snapPins())
	a := st.Current()
	st.pins["jira"]["a"] = Pin{Hash: "sha256:new"}
	b, changed, err := st.Reload()
	if err != nil || !changed {
		t.Fatalf("reload: changed=%v err=%v", changed, err)
	}
	if !a.Before(b) || b.Before(a) || a.Before(a) {
		t.Errorf("Before: a<b=%v b<a=%v a<a=%v", a.Before(b), b.Before(a), a.Before(a))
	}
}

// fakeStore drives Store from in-memory inputs.
type fakeInputs struct {
	mu      sync.Mutex
	reg     Registry
	pins    PinTable
	pinsErr error
}

type testStoreT struct {
	*Store
	in   *fakeInputs
	pins PinTable // edit then Reload (single goroutine tests only)
}

func testStore(t *testing.T, reg Registry, pins PinTable) *testStoreT {
	t.Helper()
	in := &fakeInputs{reg: reg, pins: pins}
	st := &Store{
		LoadRegistry: func() (Registry, error) { in.mu.Lock(); defer in.mu.Unlock(); return in.reg, nil },
		LoadPins: func() (PinTable, error) {
			in.mu.Lock()
			defer in.mu.Unlock()
			return in.pins, in.pinsErr
		},
	}
	return &testStoreT{Store: st, in: in, pins: pins}
}

// Current loads once; Reload reports changed only when the inputs changed,
// and a failed pins read keeps the snapshot it had.
func TestStore_ReloadChangedFlag(t *testing.T) {
	st := testStore(t, snapReg(), snapPins())
	first := st.Current()
	if first.Version() == "" {
		t.Fatal("Current did not load")
	}
	if s, changed, err := st.Reload(); err != nil || changed || s.Version() != first.Version() {
		t.Fatalf("unchanged reload: changed=%v err=%v", changed, err)
	}
	st.in.mu.Lock()
	st.in.reg.Sources["jira"] = Source{Type: TypeMCPHTTP, URL: "http://127.0.0.1:9/mcp"}
	st.in.mu.Unlock()
	s, changed, err := st.Reload()
	if err != nil || !changed || s.Version() == first.Version() || st.Current().Version() != s.Version() {
		t.Fatalf("changed reload: changed=%v err=%v", changed, err)
	}
	if first.Registry().Sources["jira"].URL != "http://127.0.0.1:8001/mcp" {
		t.Error("an earlier snapshot changed after a reload")
	}
}

// A registry that does not load is an empty registry carrying the error
// (served as nothing); its pins still load.
func TestStore_RegistryErrorIsCarried(t *testing.T) {
	st := &Store{
		LoadRegistry: func() (Registry, error) { return Registry{}, errors.New("sorces: unknown key") },
		LoadPins:     func() (PinTable, error) { return snapPins(), nil },
	}
	s := st.Current()
	if s.Err() == nil || len(s.Registry().Sets) != 0 {
		t.Fatalf("err=%v sets=%v", s.Err(), s.Registry().Sets)
	}
	if _, ok := s.Pin("jira", "a"); !ok {
		t.Error("pins dropped with the registry")
	}
}

// HomeRegistry reads the registry under home (Default when absent), and
// a readonly.json not migrated yet in memory, writing nothing.
func TestHomeRegistry_DefaultWhenAbsent(t *testing.T) {
	legacy := t.TempDir()
	writeFile(t, LegacyPath(legacy), legacyJSON)
	st := &Store{LoadRegistry: HomeRegistry(legacy), LoadPins: func() (PinTable, error) { return nil, nil }}
	if _, ok := st.Current().Registry().Sources["wiki"]; !ok {
		t.Error("readonly.json not read")
	}
	if _, err := os.Lstat(DefaultPath(legacy)); err == nil {
		t.Error("a reload migrated readonly.json")
	}
	st = &Store{LoadRegistry: HomeRegistry(t.TempDir()), LoadPins: func() (PinTable, error) { return nil, nil }}
	if _, ok := st.Current().Registry().Sources["jira"]; !ok {
		t.Error("absent registry is not Default")
	}
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "aimux", "tool-sources.yaml"), goodRegistry)
	st = &Store{LoadRegistry: HomeRegistry(home), LoadPins: func() (PinTable, error) { return nil, nil }}
	if _, ok := st.Current().Registry().Sources["notes"]; !ok {
		t.Error("registry under home not read")
	}
}

// Parallel reloads and readers never see a torn snapshot (run with -race).
func TestStore_ConcurrentReloadAndRead(t *testing.T) {
	st := testStore(t, snapReg(), snapPins())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 50 {
				st.in.mu.Lock()
				reg := snapReg()
				if (i+j)%2 == 0 {
					reg.Sources["jira"] = Source{Type: TypeMCPHTTP, URL: "http://127.0.0.1:9/mcp"}
				}
				st.in.reg = reg
				st.in.mu.Unlock()
				if _, _, err := st.Reload(); err != nil {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				s := st.Current()
				if NewSnapshot(s.Registry(), s.Pins(), nil).Version() != s.Version() {
					t.Error("snapshot's version does not match its contents")
				}
			}
		}()
	}
	wg.Wait()
}

// A pins file that does not read fails closed: the snapshot swapped in has
// no pins (nothing is granted or served) and carries the error; fixing the
// file restores the pins on the next reload, under the first version again.
func TestStore_PinsErrorFailsClosedAndRecovers(t *testing.T) {
	st := testStore(t, snapReg(), snapPins())
	first := st.Current()
	st.in.mu.Lock()
	st.in.pinsErr = errors.New("parse tool pins /h/.aimux/tools/pins.json: invalid character")
	st.in.mu.Unlock()
	s, changed, err := st.Reload()
	if err == nil || !changed || st.Current().Version() != s.Version() {
		t.Fatalf("pins error: changed=%v err=%v", changed, err)
	}
	if len(s.Pins()) != 0 || s.Err() == nil || !strings.Contains(s.Err().Error(), "pins.json") {
		t.Fatalf("pins error snapshot: pins=%v err=%v", s.Pins(), s.Err())
	}
	if _, ok := s.Pin("jira", "a"); ok {
		t.Fatal("a pin survived a pins read error")
	}
	st.in.mu.Lock()
	st.in.pinsErr = nil
	st.in.mu.Unlock()
	back, changed, err := st.Reload()
	if err != nil || !changed || back.Version() != first.Version() {
		t.Fatalf("recovery: changed=%v err=%v same version=%v", changed, err, back.Version() == first.Version())
	}
}

// A load error is part of the version: an empty registry that fails to load
// and one that loads empty never share a version, nor do two errors.
func TestSnapshotVersion_IncludesErrors(t *testing.T) {
	ok := NewSnapshot(Registry{}, nil, nil).Version()
	bad := NewSnapshot(Registry{}, nil, errors.New("sorces: unknown key")).Version()
	other := NewSnapshot(Registry{}, nil, errors.New("version must be 1")).Version()
	if ok == bad || bad == other || bad != NewSnapshot(Registry{}, nil, errors.New("sorces: unknown key")).Version() {
		t.Fatalf("ok=%s bad=%s other=%s", ok, bad, other)
	}
}

// A pins rewrite that only changes an approval time keeps the version (and
// reports no change), but the snapshot's pins carry the new time, so
// GET /v1/tools shows when the current hash was approved.
func TestStore_ReloadRefreshesApprovalTimes(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	pins := PinTable{"jira": {"a": {Hash: "sha256:1", ApprovedAt: t1}}}
	ts := testStore(t, snapReg(), pins)
	first := ts.Current()
	ts.in.mu.Lock()
	ts.in.pins = PinTable{"jira": {"a": {Hash: "sha256:1", ApprovedAt: t1.Add(time.Hour)}}}
	ts.in.mu.Unlock()
	s, changed, err := ts.Reload()
	if err != nil || changed || s.Version() != first.Version() {
		t.Fatalf("Reload = %s, %v, %v; want the same version, unchanged", s.Short(), changed, err)
	}
	if p, _ := ts.Current().Pin("jira", "a"); !p.ApprovedAt.Equal(t1.Add(time.Hour)) {
		t.Fatalf("approved_at %v, want the new one", p.ApprovedAt)
	}
	if first.Before(ts.Current()) || ts.Current().Before(first) {
		t.Fatal("an approval-time refresh counted as a new load")
	}
}
