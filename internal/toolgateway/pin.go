// Package toolgateway pins the tools a client may call: a pin is a hash of
// what a tool accepts and promises, so a backend that changes a tool under
// the same name is noticed.
package toolgateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// pinned is the part of a tool a pin covers. Description and title are left
// out on purpose: backends reword them without changing behaviour.
var pinned = []string{"name", "inputSchema", "outputSchema", "annotations"}

// PinTool returns "sha256:<hex>" over Canonical(raw): the tool's name,
// inputSchema, outputSchema and annotations. Key order and whitespace in raw
// do not change it.
func PinTool(raw json.RawMessage) (string, error) {
	canon, err := Canonical(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Canonical is the part of a tool a pin covers (name, inputSchema,
// outputSchema, annotations) as compact JSON with sorted keys: what PinTool
// hashes and what a v2 pin stores as the reviewed definition.
func Canonical(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep large numbers exact
	var all map[string]any
	if err := dec.Decode(&all); err != nil {
		return nil, fmt.Errorf("decode tool: %w", err)
	}
	if name, _ := all["name"].(string); name == "" {
		return nil, errors.New("tool has no name")
	}
	keep := make(map[string]any, len(pinned))
	for _, k := range pinned {
		if v, ok := all[k]; ok {
			keep[k] = v
		}
	}
	canon, err := json.Marshal(keep) // map keys are written sorted
	if err != nil {
		return nil, fmt.Errorf("encode tool: %w", err)
	}
	return canon, nil
}

// Pins maps server, then tool, to its pin.
type Pins map[string]map[string]string

// Filter returns a copy of cat holding only the tools whose live pin equals
// the stored one. A tool that is unpinned, changed, or cannot be pinned is
// dropped, and a server left with no tools is dropped; cat is unchanged.
func (p Pins) Filter(cat *mcpproxy.Catalog) *mcpproxy.Catalog {
	out := &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{}}
	for s, e := range cat.Servers {
		kept := mcpproxy.CatalogEntry{FetchedAt: e.FetchedAt}
		for _, raw := range e.Tools {
			name, pin, ok := nameAndPin(raw)
			if ok && p[s][name] == pin {
				kept.Tools = append(kept.Tools, raw)
			}
		}
		if len(kept.Tools) > 0 {
			out.Servers[s] = kept
		}
	}
	return out
}

func nameAndPin(raw json.RawMessage) (name, pin string, ok bool) {
	var n struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return "", "", false
	}
	pin, err := PinTool(raw)
	if err != nil {
		return "", "", false
	}
	return n.Name, pin, true
}

// PinRecord is one approved tool: the hash the gateway checks, the
// canonical definition that was reviewed and hashed, when it was approved,
// and the source endpoint (URL or notes root) it was listed from. A record
// read from a v1 file has only Hash.
type PinRecord struct {
	Hash       string          `json:"hash"`
	Definition json.RawMessage `json:"definition,omitempty"`
	ApprovedAt time.Time       `json:"approved_at,omitzero"`
	Source     string          `json:"source,omitempty"`
}

// PinFile maps source (server), then tool, to its record: pins.json v2.
type PinFile map[string]map[string]PinRecord

// Hashes is f as the Pins the gateway checks.
func (f PinFile) Hashes() Pins {
	p := Pins{}
	for s, tools := range f {
		p[s] = make(map[string]string, len(tools))
		for t, r := range tools {
			p[s][t] = r.Hash
		}
	}
	return p
}

// clone is a deep copy of f (records hold no shared maps; Definition bytes
// are never written in place, so they are shared).
func (f PinFile) clone() PinFile {
	out := make(PinFile, len(f))
	for s, tools := range f {
		out[s] = make(map[string]PinRecord, len(tools))
		for t, r := range tools {
			out[s][t] = r
		}
	}
	return out
}

const pinsVersion = 2

// pinsWire is pins.json v2 on disk.
type pinsWire struct {
	Version int     `json:"version"`
	Sources PinFile `json:"sources"`
}

// PinStore keeps pins in a JSON file: v2 is written; v1 (source -> tool ->
// hash) is still read.
type PinStore struct{ Path string }

// DefaultPinsPath is ~/.aimux/tools/pins.json under home.
func DefaultPinsPath(home string) string {
	return filepath.Join(home, ".aimux", "tools", "pins.json")
}

// Load reads the pins' hashes, from a v1 or v2 file. A missing file is
// empty Pins.
func (s PinStore) Load() (Pins, error) {
	f, err := s.LoadFile()
	if err != nil {
		return nil, err
	}
	return f.Hashes(), nil
}

// LoadFile reads the pin records. A v1 file yields records with only Hash;
// a missing file is an empty PinFile; a version other than 1 or 2 is an
// error.
func (s PinStore) LoadFile() (PinFile, error) {
	data, err := os.ReadFile(s.Path) // #nosec G304 -- path comes from aimux config
	if errors.Is(err, os.ErrNotExist) {
		return PinFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tool pins %s: %w", s.Path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse tool pins %s: %w", s.Path, err)
	}
	var version int
	// v1 has no version; a v1 server named "version" holds an object, not a number.
	if v, ok := top["version"]; !ok || json.Unmarshal(v, &version) != nil {
		v1 := Pins{}
		if err := json.Unmarshal(data, &v1); err != nil {
			return nil, fmt.Errorf("parse tool pins %s: %w", s.Path, err)
		}
		f := PinFile{}
		for srv, tools := range v1 {
			f[srv] = make(map[string]PinRecord, len(tools))
			for t, h := range tools {
				f[srv][t] = PinRecord{Hash: h}
			}
		}
		return f, nil
	}
	if version != pinsVersion {
		return nil, fmt.Errorf("tool pins %s: version %d is not supported (want %d)", s.Path, version, pinsVersion)
	}
	var w pinsWire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parse tool pins %s: %w", s.Path, err)
	}
	if w.Sources == nil {
		w.Sources = PinFile{}
	}
	for src, tools := range w.Sources {
		for t, r := range tools {
			if r.Definition == nil {
				continue
			}
			var b bytes.Buffer // Save indents it; the stored form is the compact one that was hashed
			if err := json.Compact(&b, r.Definition); err != nil {
				return nil, fmt.Errorf("tool pins %s: %s/%s: definition: %w", s.Path, src, t, err)
			}
			r.Definition = b.Bytes()
			tools[t] = r
		}
	}
	return w.Sources, nil
}

// isV1 reports whether data is a v1 pins file: an object with no numeric
// version, as LoadFile tells them apart.
func isV1(data []byte) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return false
	}
	var version int
	v, ok := top["version"]
	return !ok || json.Unmarshal(v, &version) != nil
}

// backupV1 copies a v1 file at s.Path to s.Path.v1-backup (0600), once:
// builds before pins v2 read only v1, so a downgrade restores it. An
// existing backup is left alone.
func (s PinStore) backupV1() error {
	data, err := os.ReadFile(s.Path) // #nosec G304 -- path comes from aimux config
	if errors.Is(err, os.ErrNotExist) || (err == nil && !isV1(data)) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tool pins %s: %w", s.Path, err)
	}
	b, err := os.OpenFile(s.Path+".v1-backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- next to the pins file
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("back up v1 tool pins %s: %w", s.Path, err)
	}
	if _, err := b.Write(data); err != nil {
		_ = b.Close() // the write error is the one to report
		return fmt.Errorf("back up v1 tool pins %s: %w", s.Path, err)
	}
	if err := b.Close(); err != nil {
		return fmt.Errorf("back up v1 tool pins %s: %w", s.Path, err)
	}
	return nil
}

// Save writes f as a v2 file, atomically with owner-only permissions. The
// first write over a v1 file keeps it as pins.json.v1-backup.
func (s PinStore) Save(f PinFile) error {
	data, err := json.MarshalIndent(pinsWire{Version: pinsVersion, Sources: f}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tool pins: %w", err)
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.Path)+".tmp*")
	if err != nil {
		return fmt.Errorf("write tool pins %s: %w", s.Path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := s.backupV1(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return fmt.Errorf("write tool pins %s: %w", s.Path, err)
	}
	return nil
}
