package toolreg

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

var (
	// ErrExists is a source or set name the registry already has.
	ErrExists = errors.New("already in tool-sources.yaml")
	// ErrNoSource is a source the registry does not have.
	ErrNoSource = errors.New("no such source in tool-sources.yaml")
)

// AddSource adds source name and the set <name>:readonly listing tools to
// the registry at path, and returns the set's name. Every other line of the
// file, comments and entries Load drops included, is kept. A missing file
// starts from Default() (migrate readonly.json first: this does not). It
// refuses, writing nothing, a file Load rejects, a name the file already
// has as a source or set (ErrExists, broken entries included), and a
// source or set that would not load cleanly. The file is replaced
// atomically (0600); a link is followed and its target replaced.
func AddSource(path, name string, src Source, tools []string) (string, error) {
	set := name + ":readonly"
	if err := checkSource(name, src); err != nil {
		return "", fmt.Errorf("source %q: %w", name, err)
	}
	err := editFile(path, true, func(root *yaml.Node) error {
		sources, sets := section(root, "sources"), section(root, "sets")
		if hasKey(sources, name) {
			return fmt.Errorf("source %q: %w", name, ErrExists)
		}
		if hasKey(sets, set) {
			return fmt.Errorf("set %q: %w", set, ErrExists)
		}
		if err := appendEntry(sources, name, src); err != nil {
			return err
		}
		return appendEntry(sets, set, Set{Source: name, Tools: tools})
	}, func(before, after Registry) error {
		if err := after.SourceErrors[name]; err != nil {
			return err
		}
		return after.SetErrors[set]
	})
	if err != nil {
		return "", err
	}
	return set, nil
}

// RemoveSource removes source name, broken or not, and every set naming it
// from the registry at path, keeping every other line, and returns the
// removed sets, sorted. A missing file or source is an error (ErrNoSource
// for the source) and nothing is written. Pins are not touched.
func RemoveSource(path, name string) ([]string, error) {
	var removed []string
	err := editFile(path, false, func(root *yaml.Node) error {
		sources, sets := section(root, "sources"), section(root, "sets")
		if !deleteKey(sources, name) {
			return fmt.Errorf("source %q: %w", name, ErrNoSource)
		}
		if sets.Kind != yaml.MappingNode {
			return nil
		}
		kept := sets.Content[:0:0]
		for i := 0; i+1 < len(sets.Content); i += 2 {
			var s struct {
				Source string `yaml:"source"`
			}
			if sets.Content[i+1].Decode(&s) == nil && s.Source == name {
				removed = append(removed, sets.Content[i].Value)
				continue
			}
			kept = append(kept, sets.Content[i], sets.Content[i+1])
		}
		sets.Content = kept
		return nil
	}, nil)
	slices.Sort(removed)
	return removed, err
}

// editFile applies edit to path's YAML tree and writes the result when it
// loads with no error Load did not already report for the old file, and
// check (if set) accepts it. A missing path starts from Default() when
// create is set and is an error otherwise.
func editFile(path string, create bool, edit func(root *yaml.Node) error, check func(before, after Registry) error) error {
	target, data, err := readForEdit(path, create)
	if err != nil {
		return err
	}
	before, err := parse(path, data)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("tool sources %s: %w", path, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("tool sources %s: not a mapping", path)
	}
	if err := edit(doc.Content[0]); err != nil {
		return err
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode tool sources: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode tool sources: %w", err)
	}
	after, err := parse(path, b.Bytes())
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(before, after); err != nil {
			return err
		}
	}
	for _, m := range [][2]map[string]error{{before.SourceErrors, after.SourceErrors}, {before.SetErrors, after.SetErrors}} {
		for _, n := range slices.Sorted(maps.Keys(m[1])) {
			if m[0][n] == nil {
				return m[1][n]
			}
		}
	}
	if target == "" {
		return writeNew(path, b.Bytes())
	}
	return replace(target, data, b.Bytes())
}

// readForEdit is the file to replace (path, or a link's target) and its
// bytes; target is "" when path is missing and create starts it from
// Default().
func readForEdit(path string, create bool) (target string, data []byte, err error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if !create {
			return "", nil, fmt.Errorf("tool sources %s: %w", path, err)
		}
		data, err := Marshal(Default())
		return "", data, err
	}
	target, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("read tool sources %s: %w", path, err)
	}
	data, err = os.ReadFile(target) //nolint:gosec // G304: the user's own tool-sources.yaml
	if err != nil {
		return "", nil, fmt.Errorf("read tool sources %s: %w", path, err)
	}
	return target, data, nil
}

// replace writes data over target through a 0600 temp file renamed into
// place, unless target no longer holds was (someone edited it meanwhile).
func replace(target string, was, data []byte) error {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".tool-sources-*.yaml") // created 0600
	if err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after the rename
	if _, err := f.Write(data); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close() // the sync error is the one to report
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	now, err := os.ReadFile(target) //nolint:gosec // G304: the user's own tool-sources.yaml
	if err != nil || !bytes.Equal(now, was) {
		return fmt.Errorf("write %s: it changed while being edited; run the command again", target)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// section is root's mapping under key, made an empty block mapping when
// it is absent, null or empty.
func section(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			v := root.Content[i+1]
			if v.Kind != yaml.MappingNode {
				*v = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			if len(v.Content) == 0 {
				v.Style = 0
			}
			return v
		}
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v
}

func hasKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

func deleteKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

func appendEntry(m *yaml.Node, key string, v any) error {
	var val yaml.Node
	if err := val.Encode(v); err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &val)
	return nil
}
