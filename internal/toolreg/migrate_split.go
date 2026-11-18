package toolreg

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MigrateSplit writes servers.yaml (from tool-sources.yaml's sources) and
// tool-sets.yaml (from its sets) in dir, keeping the comments on each entry,
// and keeps the old file as tool-sources.yaml.migrated. Pins are not
// touched: a server keeps its name, so its pins still apply. It refuses when
// either new file exists.
func MigrateSplit(dir string) error {
	legacy := filepath.Join(dir, "tool-sources.yaml")
	if splitMode(dir) {
		return fmt.Errorf("%s or %s already exists in %s; nothing was changed", serversFile, setsFile, dir)
	}
	data, err := os.ReadFile(legacy) //nolint:gosec // G304: the user's own tool-sources.yaml
	if err != nil {
		return fmt.Errorf("read %s: %w", legacy, err)
	}
	before, err := parse(legacy, data)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return fmt.Errorf("%s: cannot read its structure", legacy)
	}
	root := doc.Content[0]
	sources, sets := section(root, "sources"), section(root, "sets")
	if sources == nil {
		sources = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if sets == nil {
		sets = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	srv, err := encodeTop(root.HeadComment, "servers", sources)
	if err != nil {
		return err
	}
	st, err := encodeTop("", "sets", sets)
	if err != nil {
		return err
	}
	serversPath, setsPath := filepath.Join(dir, serversFile), filepath.Join(dir, setsFile)
	if err := writeAtomic(serversPath, srv); err != nil {
		return err
	}
	if err := writeAtomic(setsPath, st); err != nil {
		_ = os.Remove(serversPath)
		return err
	}
	after, err := loadSplit(dir, nil)
	if err == nil && !samePolicy(before, after) {
		err = errors.New("they would grant sandboxes a different set of tools")
	}
	if err != nil {
		_ = os.Remove(serversPath)
		_ = os.Remove(setsPath)
		return fmt.Errorf("the split files did not reproduce tool-sources.yaml, so nothing was changed: %w", err)
	}
	if err := os.Rename(legacy, legacy+".migrated"); err != nil {
		return errors.Join(fmt.Errorf("wrote %s and %s, but could not rename %s", serversPath, setsPath, legacy), err)
	}
	return nil
}

func encodeTop(comment, key string, body *yaml.Node) ([]byte, error) {
	top := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: comment, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "version"}, {Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, body,
	}}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

// writeAtomic writes data to path (mode 0600) through a temp file in the
// same directory, refusing to replace an existing file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".split-*.yaml")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Link(tmp, path); err != nil { // fails if path exists
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// samePolicy reports whether two registries grant the same tools from the
// same sources.
func samePolicy(a, b Registry) bool {
	if len(a.Sets) != len(b.Sets) {
		return false
	}
	for n, s := range a.Sets {
		t, ok := b.Sets[n]
		if !ok || t.Source != s.Source || strings.Join(t.Tools, ",") != strings.Join(s.Tools, ",") || a.Sources[s.Source] != b.Sources[t.Source] {
			return false
		}
	}
	return true
}
