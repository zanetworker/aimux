package toolreg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Pin is what a snapshot needs of one approved tool: the hash the gateway
// checks, the endpoint it was approved from ("" for a v1 pin: unknown) and
// when (zero: unknown), which GET /v1/tools reports. toolgateway owns the
// pins file; it hands its records over as a PinTable, so this package does
// not import it.
type Pin struct {
	Hash   string `json:"hash"`
	Source string `json:"source,omitempty"`
	// ApprovedAt is left out of the version: an approval time alone
	// changes nothing granted or served.
	ApprovedAt time.Time `json:"-"`
}

// PinTable maps source (server), then tool, to its pin.
type PinTable map[string]map[string]Pin

func (p PinTable) clone() PinTable {
	out := make(PinTable, len(p))
	for s, tools := range p {
		out[s] = maps.Clone(tools)
	}
	return out
}

// Endpoint is where a source's tools are listed from: its URL, its root
// for a notes source, pack:<name> for a cli source or its base URL. A pin records it at approval.
// A changed http-openapi spec path does not drop approvals: pins hash each
// tool definition (method and path template included), so changed operations
// show as changed.
func (s Source) Endpoint() string {
	switch s.Type {
	case TypeLocalNotes:
		return s.Root
	case TypeCLI:
		return "pack:" + s.Pack
	case TypeHTTPOpenAPI:
		return s.BaseURL
	}
	return s.URL
}

// SourceChanged reports whether a pin recorded from endpoint recorded no
// longer matches server's current endpoint (a source the registry dropped
// has none). An empty recorded endpoint (a v1 pin) is unknown, never
// changed.
func (r Registry) SourceChanged(server, recorded string) bool {
	return recorded != "" && recorded != r.Sources[server].Endpoint()
}

// Snapshot is one consistent view of the registry and the pins, with a
// version that is a hash of both. It is immutable: it copies what it is
// made from and its accessors return copies, so it can be shared across
// goroutines and kept by a turn while the files change.
type Snapshot struct {
	version string
	seq     uint64 // the Store's reload count when made; 0 outside a Store
	reg     Registry
	pins    PinTable
	err     error
}

// NewSnapshot is a snapshot of reg and pins; loadErr is why the registry or
// the pins did not load (what failed is then empty), kept for callers that
// report it and part of the version.
func NewSnapshot(reg Registry, pins PinTable, loadErr error) Snapshot {
	s := Snapshot{reg: cloneRegistry(reg), pins: pins.clone(), err: loadErr}
	s.version = s.hash()
	return s
}

func cloneRegistry(r Registry) Registry {
	out := Registry{Version: r.Version, Sources: maps.Clone(r.Sources), Sets: make(map[string]Set, len(r.Sets)),
		SourceErrors: maps.Clone(r.SourceErrors), SetErrors: maps.Clone(r.SetErrors)}
	if out.Sources == nil {
		out.Sources = map[string]Source{}
	}
	for n, s := range r.Sets {
		out.Sets[n] = Set{Source: s.Source, Tools: slices.Clone(s.Tools)}
	}
	return out
}

// hash is sha256 over the canonical JSON of the sources, the sets (tools
// sorted), the pins and the load error's text, so a broken config never
// shares a version with a good one; encoding/json writes map keys sorted.
func (s Snapshot) hash() string {
	sets := make(map[string]Set, len(s.reg.Sets))
	for n, set := range s.reg.Sets {
		sets[n] = Set{Source: set.Source, Tools: slices.Sorted(slices.Values(set.Tools))}
	}
	loadErr := ""
	if s.err != nil {
		loadErr = s.err.Error()
	}
	canon, _ := json.Marshal(struct { // plain strings and maps always encode
		Sources map[string]Source
		Sets    map[string]Set
		Pins    PinTable
		Err     string `json:",omitempty"`
	}{s.reg.Sources, sets, s.pins, loadErr})
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Version is the hash of the registry and pins: equal inputs, equal version.
func (s Snapshot) Version() string { return s.version }

// Short is the version's first 12 hex digits, for logs.
func (s Snapshot) Short() string {
	const prefix = len("sha256:")
	if len(s.version) < prefix+12 {
		return s.version
	}
	return s.version[prefix : prefix+12]
}

// Registry is a copy of the registry.
func (s Snapshot) Registry() Registry { return cloneRegistry(s.reg) }

// Pins is a copy of the pins.
func (s Snapshot) Pins() PinTable { return s.pins.clone() }

// Err is why the registry or the pins did not load, or nil.
func (s Snapshot) Err() error { return s.err }

// Pin is tool's pin on server.
func (s Snapshot) Pin(server, tool string) (Pin, bool) {
	p, ok := s.pins[server][tool]
	return p, ok
}

// SourceChanged reports whether tool's pin on server was approved from
// another endpoint than the registry now names (see Registry.SourceChanged).
func (s Snapshot) SourceChanged(server, tool string) bool {
	p, ok := s.pins[server][tool]
	return ok && s.reg.SourceChanged(server, p.Source)
}

// Before reports whether s was loaded by a Store before o.
func (s Snapshot) Before(o Snapshot) bool { return s.seq < o.seq }

// Store holds the current snapshot and swaps it whole on Reload, so a
// reader sees either the old or the new one, never a mix.
type Store struct {
	// LoadRegistry reads the registry (HomeRegistry); an error leaves the
	// snapshot's registry empty and is kept as its Err.
	LoadRegistry func() (Registry, error)
	// LoadPins reads the pins; an error fails closed: the snapshot has no
	// pins (nothing is granted or served) and carries the error, and Reload
	// returns it too.
	LoadPins func() (PinTable, error)

	mu  sync.Mutex // one Reload at a time
	cur atomic.Pointer[Snapshot]
	seq uint64
}

// Current is the current snapshot, loading the first one if none is.
func (st *Store) Current() Snapshot {
	if s := st.cur.Load(); s != nil {
		return *s
	}
	s, _, _ := st.Reload() // a failed load is a snapshot carrying its error
	return s
}

// Reload reads the registry and pins and swaps in a new snapshot when its
// version differs from the current one (changed); when only approval times
// differ it swaps them in too, keeping the version and changed false. A registry or pins read
// that fails leaves that part empty in the snapshot, which carries the
// error; err is the pins error, if any (the registry's is only carried).
func (st *Store) Reload() (s Snapshot, changed bool, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	pins, pinsErr := st.LoadPins()
	if pinsErr != nil {
		pins = nil
	}
	reg, regErr := st.LoadRegistry()
	if regErr != nil {
		reg = Registry{}
	}
	next := NewSnapshot(reg, pins, errors.Join(regErr, pinsErr))
	if cur := st.cur.Load(); cur != nil && cur.version == next.version {
		if !reflect.DeepEqual(cur.pins, next.pins) { // approval times only: same version, same load
			next.seq = cur.seq
			st.cur.Store(&next)
			return next, false, pinsErr
		}
		return *cur, false, pinsErr
	}
	st.seq++
	next.seq = st.seq
	st.cur.Store(&next)
	return next, true, pinsErr
}

// HomeRegistry is a Store.LoadRegistry reading the registry under home
// with no side effect (ReadHome): a reload never migrates or writes.
func HomeRegistry(home string) func() (Registry, error) {
	return func() (Registry, error) { return ReadHome(home) }
}
