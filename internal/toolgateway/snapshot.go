package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"weak"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// ErrSourceChanged means the tool was approved from another endpoint than
// the registry now names; `aimux tools approve` re-stamps it.
var ErrSourceChanged = errors.New("tool source changed since it was pinned")

// Table is f as the pins a toolreg snapshot holds: each record's hash,
// recorded source and approval time.
func (f PinFile) Table() toolreg.PinTable {
	out := make(toolreg.PinTable, len(f))
	for s, tools := range f {
		out[s] = make(map[string]toolreg.Pin, len(tools))
		for t, r := range tools {
			out[s][t] = toolreg.Pin{Hash: r.Hash, Source: r.Source, ApprovedAt: r.ApprovedAt}
		}
	}
	return out
}

// PinsOf is snap's pins as Review reads them: each record's hash, source
// and approval time, without a definition (a snapshot keeps none).
func PinsOf(snap toolreg.Snapshot) PinFile {
	out := PinFile{}
	for s, tools := range snap.Pins() {
		out[s] = make(map[string]PinRecord, len(tools))
		for t, p := range tools {
			out[s][t] = PinRecord{Hash: p.Hash, Source: p.Source, ApprovedAt: p.ApprovedAt}
		}
	}
	return out
}

// NewStore is the snapshot store over home: the registry read with no side
// effect (toolreg.ReadHome: a readonly.json not migrated yet is read in
// memory) and the pins in DefaultPinsPath. Reloads never write.
func NewStore(home string) *toolreg.Store {
	pins := PinStore{Path: DefaultPinsPath(home)}
	return &toolreg.Store{
		LoadRegistry: toolreg.HomeRegistry(home),
		LoadPins: func() (toolreg.PinTable, error) {
			f, err := pins.LoadFile()
			if err != nil {
				return nil, err
			}
			return f.Table(), nil
		},
	}
}

// ServedPins is snap's pins as the gateway and the compiler use them: each
// tool's hash, leaving out tools whose pin is source_changed.
func ServedPins(snap toolreg.Snapshot) Pins {
	out := Pins{}
	for s, tools := range snap.Pins() {
		for t, p := range tools {
			if snap.SourceChanged(s, t) {
				continue
			}
			if out[s] == nil {
				out[s] = map[string]string{}
			}
			out[s][t] = p.Hash
		}
	}
	return out
}

// SnapshotPins is the mcpproxy.ToolPins the gateway builds its servers
// with: Pins (ServedPins of Snap) filter the listing and check each call,
// and a call to a source_changed tool is refused with ErrSourceChanged.
type SnapshotPins struct {
	Pins Pins
	Snap toolreg.Snapshot
	// seen, if set, hears each catalog Filter is given (the backends'
	// allowed live tools), so the gateway knows what it listed.
	seen func(cat *mcpproxy.Catalog)
	// listed, if set, hears each live listing a call's check made of a
	// backend, so the gateway notices a change between its builds.
	listed func(server string, tools []json.RawMessage)
	// verifiers are the ones Guard made, which Changed invalidates; nil
	// (a SnapshotPins not from GatewayPins) keeps none.
	verifiers *verifierSet
	// onChanged, if set, hears each server Changed is told about.
	onChanged func(server string)
	// applied, if set, is the snapshot the gateway serves now: a call to a
	// server whose identity there differs from Snap's is refused, so a
	// server built under an older identity takes no call once a refresh
	// has applied the new one, even before the rebuild replaces it.
	applied func() toolreg.Snapshot
}

var _ mcpproxy.ToolPins = SnapshotPins{}

// GatewayPins is snap as the gateway's ToolPins.
func GatewayPins(snap toolreg.Snapshot) SnapshotPins {
	return SnapshotPins{Pins: ServedPins(snap), Snap: snap, verifiers: &verifierSet{}}
}

// Filter keeps the catalog tools whose live pin matches a served pin.
func (p SnapshotPins) Filter(cat *mcpproxy.Catalog) *mcpproxy.Catalog {
	if p.seen != nil {
		p.seen(cat)
	}
	return p.Pins.Filter(cat)
}

// Guard checks each call against the served pins, naming a source change.
func (p SnapshotPins) Guard(list func(ctx context.Context, server string) ([]json.RawMessage, error)) func(ctx context.Context, server, tool string) error {
	if p.listed != nil {
		inner := list
		list = func(ctx context.Context, server string) ([]json.RawMessage, error) {
			tools, err := inner(ctx, server)
			if err == nil {
				p.listed(server, tools)
			}
			return tools, err
		}
	}
	v := p.Pins.Verifier(list)
	p.verifiers.add(v)
	check := v.Check
	moved := func(server, tool string) error {
		if cur := p.current(); cur.Version() != "" && cur.PinSource(server) != p.Snap.PinSource(server) {
			return fmt.Errorf("%s/%s: %w (the server's identity changed); run aimux tools status", server, tool, ErrSourceChanged)
		}
		return nil
	}
	return func(ctx context.Context, server, tool string) error {
		if err := moved(server, tool); err != nil {
			return err
		}
		if p.Snap.SourceChanged(server, tool) {
			return fmt.Errorf("%s/%s: %w; run aimux tools status", server, tool, ErrSourceChanged)
		}
		if err := check(ctx, server, tool); err != nil {
			return err
		}
		// Again after the check, which may list live and take a while: a
		// refresh applied meanwhile has retired this server.
		return moved(server, tool)
	}
}

// current is the applied snapshot, or a zero one when none is applied yet
// (the gateway is still starting, so its build is the one served).
func (p SnapshotPins) current() toolreg.Snapshot {
	if p.applied == nil {
		return toolreg.Snapshot{}
	}
	return p.applied()
}

// Changed hears that server sent tools/list_changed: each guard's verifier
// lists it again on its next call, and onChanged, if set, hears it. It
// does not block (a backend's notification goroutine calls it).
func (p SnapshotPins) Changed(server string) {
	p.verifiers.invalidate(server)
	if p.onChanged != nil {
		p.onChanged(server)
	}
}

// verifierSet is the verifiers a SnapshotPins' guards check with, held
// weakly: the gateway rebuilds a backend (and so makes a guard) on every
// relist, and a guard whose servers were dropped lets its verifier go.
type verifierSet struct {
	mu sync.Mutex
	vs []weak.Pointer[Verifier]
}

func (s *verifierSet) add(v *Verifier) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(nil)
	s.vs = append(s.vs, weak.Make(v))
}

// invalidate invalidates server on every verifier still in use.
func (s *verifierSet) invalidate(server string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(func(v *Verifier) { v.Invalidate(server) })
}

// prune drops the verifiers collected since and runs fn, if set, on each
// one left. s.mu is held.
func (s *verifierSet) prune(fn func(*Verifier)) {
	kept := s.vs[:0]
	for _, w := range s.vs {
		if v := w.Value(); v != nil {
			if fn != nil {
				fn(v)
			}
			kept = append(kept, w)
		}
	}
	clear(s.vs[len(kept):])
	s.vs = kept
}
