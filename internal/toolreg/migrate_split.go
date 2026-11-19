package toolreg

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// MigrateSplit writes servers.yaml (from tool-sources.yaml's sources) and
// tool-sets.yaml (from its sets) in dir, keeping the comments on each entry,
// and keeps the old file as tool-sources.yaml.migrated. Pins are not
// touched: a server keeps its name, so its pins still apply. It refuses when
// servers.yaml exists; an existing tool-sets.yaml is kept aside (see
// keepAside) and put back if the migration fails.
func MigrateSplit(dir string) error {
	legacy := filepath.Join(dir, "tool-sources.yaml")
	unlock, err := lockMigrate(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if splitMode(dir) {
		return fmt.Errorf("%s already exists in %s; nothing was changed", serversFile, dir)
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
	// Check the result in memory before writing anything.
	after, err := loadSplitData(dir, srv, st)
	if err == nil && !splitCheck(before, after) {
		err = errors.New("they would grant sandboxes a different set of tools")
	}
	if err != nil {
		return fmt.Errorf("the split files would not reproduce tool-sources.yaml, so nothing was written: %w", err)
	}
	serversPath, setsPath := filepath.Join(dir, serversFile), filepath.Join(dir, setsFile)
	// tool-sets.yaml first (an existing one is kept aside), servers.yaml
	// last: split mode starts only when servers.yaml exists, so a crash in
	// between leaves tool-sources.yaml in charge.
	backup := ""
	if _, err := os.Lstat(setsPath); err == nil {
		// Never lose a tool-sets.yaml someone wrote, or a crashed run left:
		// keep it beside the new one, under a name no earlier backup has.
		if backup, err = keepAside(setsPath); err != nil {
			return fmt.Errorf("keep the existing %s: %w; nothing was changed", setsPath, err)
		}
	}
	var published os.FileInfo
	// restore takes back only the file this migration published (not one
	// another writer put there since), and puts the backup back only where
	// nothing else is now.
	restore := func() error {
		var errs []error
		if published != nil {
			errs = append(errs, takeBack(setsPath, published, st))
		}
		if backup != "" {
			if err := renameNoReplace(backup, setsPath); err != nil {
				errs = append(errs, fmt.Errorf("your earlier %s is kept as %s (%w)", setsPath, backup, err))
			}
		}
		return errors.Join(errs...)
	}
	if published, err = writeSplitFile(setsPath, st); err != nil {
		return errors.Join(err, restore())
	}
	if _, err := writeSplitFile(serversPath, srv); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Someone else created servers.yaml: leave it, and put the sets
			// back so they never pair with an inventory this run did not write.
			return errors.Join(fmt.Errorf("%w; %s was created by someone else meanwhile and is left as it is", err, serversPath), restore())
		}
		if _, lerr := os.Lstat(serversPath); lerr == nil {
			// servers.yaml was kept (edited meanwhile): split mode is on, so
			// its tool-sets.yaml stays with it rather than mismatch.
			return fmt.Errorf("%w; %s was changed by someone else meanwhile and is kept, so %s is kept too: check both before using them", err, serversPath, setsPath)
		}
		return errors.Join(err, restore())
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

// keepAside moves path to path.before-migrate, or path.before-migrate.N
// for the first N free, in one atomic rename that never replaces an
// existing file, and returns the name it used.
func keepAside(path string) (string, error) {
	for n := 0; n < 1000; n++ {
		name := path + ".before-migrate"
		if n > 0 {
			name = fmt.Sprintf("%s.%d", name, n)
		}
		err := renameNoReplace(path, name)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return name, nil
	}
	return "", errors.New("too many earlier backups; remove old tool-sets.yaml.before-migrate files")
}

// takeBack removes path only while it is still the file published, holding
// exactly the bytes written: it moves whatever is at path aside in one
// step, deletes it when it is that file unchanged, and otherwise moves it
// back (another writer's file, or an in-place edit, is never deleted; if
// it cannot go back, the error names where it is).
func takeBack(path string, published os.FileInfo, wrote []byte) error {
	aside := fmt.Sprintf("%s.rollback-%d", path, os.Getpid())
	if err := renameNoReplace(path, aside); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("take back %s: %w", path, err)
	}
	if fi, err := os.Lstat(aside); err == nil && os.SameFile(fi, published) {
		if b, err := os.ReadFile(aside); err == nil && bytes.Equal(b, wrote) { //nolint:gosec // G304: the file this migration wrote
			return os.Remove(aside)
		}
	}
	if err := renameNoReplace(aside, path); err != nil {
		return fmt.Errorf("another writer's %s is kept as %s (%w)", path, aside, err)
	}
	return nil
}

// writeSplitFile is writeAtomic; tests replace it to fail a write.
// It returns the file it published.
var writeSplitFile = writeAtomic

// writeAtomic writes data to path (mode 0600) through a temp file in the
// same directory, synced to disk with its directory, refusing to replace
// an existing file.
func writeAtomic(path string, data []byte) (os.FileInfo, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".split-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Link(tmp, path); err != nil { // fails if path exists
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if err := syncDirFn(dir); err != nil {
		// Not durable: take it back rather than leave it half-published.
		return nil, errors.Join(fmt.Errorf("write %s: %w", path, err), takeBack(path, fi, data))
	}
	return fi, nil
}

// splitCheck is samePolicy; tests replace it.
var splitCheck = samePolicy

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

// lockMigrate lets one migration run at a time in dir.
func lockMigrate(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".migrate.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: a lock beside the user's own config
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: an open file descriptor is a small non-negative int
		_ = f.Close()
		return nil, errors.New("another aimux servers migrate (or a tools edit) is running; try again when it finishes")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil //nolint:gosec // G115: as above
}

// syncDirFn is syncDir; tests replace it.
var syncDirFn = syncDir

// syncDir flushes dir's entries (a new link) to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the config directory
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }() // read-only
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
