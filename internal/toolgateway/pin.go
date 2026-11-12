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

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// pinned is the part of a tool a pin covers. Description and title are left
// out on purpose: backends reword them without changing behaviour.
var pinned = []string{"name", "inputSchema", "outputSchema", "annotations"}

// PinTool returns "sha256:<hex>" over the tool's name, inputSchema,
// outputSchema and annotations. Key order and whitespace in raw do not
// change it.
func PinTool(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep large numbers exact
	var all map[string]any
	if err := dec.Decode(&all); err != nil {
		return "", fmt.Errorf("decode tool: %w", err)
	}
	if name, _ := all["name"].(string); name == "" {
		return "", errors.New("tool has no name")
	}
	keep := make(map[string]any, len(pinned))
	for _, k := range pinned {
		if v, ok := all[k]; ok {
			keep[k] = v
		}
	}
	canon, err := json.Marshal(keep) // map keys are written sorted
	if err != nil {
		return "", fmt.Errorf("encode tool: %w", err)
	}
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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

// PinStore keeps Pins in a JSON file.
type PinStore struct{ Path string }

// DefaultPinsPath is ~/.aimux/tools/pins.json under home.
func DefaultPinsPath(home string) string {
	return filepath.Join(home, ".aimux", "tools", "pins.json")
}

// Load reads the pins. A missing file is empty Pins.
func (s PinStore) Load() (Pins, error) {
	data, err := os.ReadFile(s.Path) // #nosec G304 -- path comes from aimux config
	if errors.Is(err, os.ErrNotExist) {
		return Pins{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tool pins %s: %w", s.Path, err)
	}
	p := Pins{}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse tool pins %s: %w", s.Path, err)
	}
	return p, nil
}

// Save writes the pins atomically with owner-only permissions.
func (s PinStore) Save(p Pins) error {
	data, err := json.MarshalIndent(p, "", "  ")
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
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return fmt.Errorf("write tool pins %s: %w", s.Path, err)
	}
	return nil
}
