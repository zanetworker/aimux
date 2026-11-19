package toolreg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A second crashed run never overwrites the first run's backup: each
// tool-sets.yaml kept aside gets its own name.
func TestMigrateSplit_TwoCrashedRunsKeepFirstBackup(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml.before-migrate"), "version: 1\n# owner's original\nsets: {}\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# left by a crashed run\nsets: {}\n")
	if err := MigrateSplit(dir); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml.before-migrate")) //nolint:gosec // test file
	if !strings.Contains(string(first), "owner's original") {
		t.Fatalf("the first backup was overwritten: %s", first)
	}
	kept := false
	matches, _ := filepath.Glob(filepath.Join(dir, "tool-sets.yaml.before-migrate*"))
	for _, m := range matches {
		if b, _ := os.ReadFile(m); strings.Contains(string(b), "crashed run") { //nolint:gosec // test file
			kept = true
		}
	}
	if !kept {
		t.Fatalf("the second tool-sets.yaml was lost; backups: %v", matches)
	}
}

// When servers.yaml cannot be written, the owner's tool-sets.yaml is put
// back where it was.
func TestMigrateSplit_FailedServersWriteRestoresToolSets(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# hand-written\nsets: {}\n")
	old := writeSplitFile
	writeSplitFile = func(path string, data []byte) (os.FileInfo, error) {
		if filepath.Base(path) == serversFile {
			return nil, errors.New("disk full")
		}
		return old(path, data)
	}
	defer func() { writeSplitFile = old }()
	if err := MigrateSplit(dir); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the write error", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "tool-sets.yaml")) //nolint:gosec // test file
	if err != nil || !strings.Contains(string(b), "hand-written") {
		t.Fatalf("owner's tool-sets.yaml not restored: %v %s", err, b)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "tool-sets.yaml.before-migrate*")); len(m) != 0 {
		t.Fatalf("restored, so no backup should remain: %v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, serversFile)); err == nil {
		t.Fatal("servers.yaml must not exist after a failed write")
	}
}

// Editing tool-sources.yaml waits for no migration: while one runs, the
// edit refuses instead of racing it.
func TestEdits_RefuseDuringMigrate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tool-sources.yaml")
	writeYAML(t, p, legacyFixture)
	unlock, err := lockMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := AddSource(p, "notes", Source{Type: TypeLocalNotes, Root: "/n"}, []string{"wiki_read"}); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Errorf("AddSource: %v", err)
	}
	if _, err := RemoveSource(p, "jira"); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Errorf("RemoveSource: %v", err)
	}
	if _, err := AddToSet(p, "jira:readonly", "new_tool"); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Errorf("AddToSet: %v", err)
	}
}

// servers.yaml published but its directory not synced: the migration
// takes servers.yaml back, so tool-sources.yaml stays in charge and a
// retry works.
func TestMigrateSplit_DirSyncFailureRollsBackServers(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# hand-written\nsets: {}\n")
	old := syncDirFn
	calls := 0
	syncDirFn = func(d string) error {
		calls++
		if calls == 2 { // after servers.yaml is linked
			return errors.New("sync failed")
		}
		return old(d)
	}
	err := MigrateSplit(dir)
	syncDirFn = old
	if err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, serversFile)); err == nil {
		t.Fatal("servers.yaml must be taken back")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml")); !strings.Contains(string(b), "hand-written") { //nolint:gosec // test file
		t.Fatalf("owner's tool-sets.yaml not restored: %s", b)
	}
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil || len(reg.Sets) != 2 {
		t.Fatalf("legacy must rule: %v %v", reg.Sets, err)
	}
	if err := MigrateSplit(dir); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// A tool-sets.yaml another writer created during the migration is never
// deleted or overwritten; the owner's earlier one stays in its backup.
func TestMigrateSplit_KeepsAnotherWritersToolSets(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# owner\nsets: {}\n")
	old := writeSplitFile
	writeSplitFile = func(path string, data []byte) (os.FileInfo, error) {
		if filepath.Base(path) == setsFile {
			if err := os.WriteFile(path, []byte("version: 1\n# other writer\nsets: {}\n"), 0o600); err != nil {
				return nil, err
			}
		}
		return old(path, data)
	}
	defer func() { writeSplitFile = old }()
	err := MigrateSplit(dir)
	if err == nil || !strings.Contains(err.Error(), ".before-migrate") {
		t.Fatalf("err = %v, want one naming where the owner's file is", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml")); !strings.Contains(string(b), "other writer") { //nolint:gosec // test file
		t.Fatalf("the other writer's file was replaced: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml.before-migrate")); !strings.Contains(string(b), "# owner") { //nolint:gosec // test file
		t.Fatalf("the owner's backup is gone: %s", b)
	}
}

// A registry that is a link from a read-only config directory stays
// editable: no lock can be made there, and no migration can write there.
func TestEdits_ReadOnlyDirWithLinkedRegistry(t *testing.T) {
	target := filepath.Join(t.TempDir(), "tool-sources.yaml")
	writeYAML(t, target, legacyFixture)
	dir := t.TempDir()
	link := filepath.Join(dir, "tool-sources.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }() //nolint:gosec // test dir
	if _, err := AddSource(link, "notes", Source{Type: TypeLocalNotes, Root: "/n"}, []string{"wiki_read"}); err != nil {
		t.Fatalf("AddSource through a link in a read-only dir: %v", err)
	}
}

// Another writer replaces the tool-sets.yaml the migration published, then
// servers.yaml fails: the rollback leaves the replacement, and the owner's
// earlier file stays in its backup.
func TestMigrateSplit_RollbackKeepsAReplacement(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# owner\nsets: {}\n")
	old := writeSplitFile
	writeSplitFile = func(path string, data []byte) (os.FileInfo, error) {
		if filepath.Base(path) == serversFile {
			// an editor saves tool-sets.yaml atomically meanwhile
			tmp := filepath.Join(dir, "editor.tmp")
			if err := os.WriteFile(tmp, []byte("version: 1\n# editor\nsets: {}\n"), 0o600); err != nil {
				return nil, err
			}
			if err := os.Rename(tmp, filepath.Join(dir, setsFile)); err != nil {
				return nil, err
			}
			return nil, errors.New("disk full")
		}
		return old(path, data)
	}
	defer func() { writeSplitFile = old }()
	err := MigrateSplit(dir)
	if err == nil || !strings.Contains(err.Error(), ".before-migrate") {
		t.Fatalf("err = %v, want one naming the backup", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml")); !strings.Contains(string(b), "# editor") { //nolint:gosec // test file
		t.Fatalf("the editor's file was rolled back: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tool-sets.yaml.before-migrate")); !strings.Contains(string(b), "# owner") { //nolint:gosec // test file
		t.Fatalf("the owner's backup is gone: %s", b)
	}
}

// A published file edited in place before the rollback is kept: the
// rollback deletes only bytes it wrote.
func TestTakeBack_KeepsAnInPlaceEdit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "servers.yaml")
	fi, err := writeAtomic(p, []byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("version: 1\n# edited\n"), 0o600); err != nil { // same inode
		t.Fatal(err)
	}
	if err := takeBack(p, fi, []byte("version: 1\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "# edited") { //nolint:gosec // test file
		t.Fatalf("in-place edit lost: %q", b)
	}
	if err := takeBack(p, fi, []byte("version: 1\n# edited\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("the file as written must be taken back")
	}
}

// Where the filesystem has no atomic no-replace rename, nothing is moved:
// a two-step fallback could delete an editor's save between the steps.
func TestRenameNoClobber_RefusesWithoutAtomic(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeYAML(t, from, "a")
	unsupported := func(string, string) error { return syscall.ENOTSUP }
	if err := renameNoClobber(from, to, unsupported); err == nil || !strings.Contains(err.Error(), "no-replace") {
		t.Fatalf("err = %v, want a refusal naming the missing no-replace rename", err)
	}
	if _, err := os.Lstat(from); err != nil {
		t.Fatal("from must stay where it was")
	}
	if _, err := os.Lstat(to); err == nil {
		t.Fatal("to must not be created")
	}
}

// servers.yaml edited in place before a failed directory sync is kept, so
// the published tool-sets.yaml is kept with it: the two never mismatch.
func TestMigrateSplit_KeptServersKeepsItsSets(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	old := syncDirFn
	calls := 0
	syncDirFn = func(d string) error {
		calls++
		if calls == 2 { // servers.yaml linked; an editor saves it in place
			f, err := os.OpenFile(filepath.Join(d, serversFile), os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // test file
			if err == nil {
				_, _ = f.WriteString("# edited\n")
				_ = f.Close()
			}
			return errors.New("sync failed")
		}
		return old(d)
	}
	defer func() { syncDirFn = old }()
	err := MigrateSplit(dir)
	if err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("err = %v, want one saying both files are kept", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, serversFile)); err != nil {
		t.Fatal("the edited servers.yaml must be kept")
	}
	if _, err := os.Lstat(filepath.Join(dir, setsFile)); err != nil {
		t.Fatal("tool-sets.yaml must stay with the kept servers.yaml")
	}
}

// Someone else creates servers.yaml before the migration publishes its own:
// their file is left alone and the sets are rolled back to the owner's.
func TestMigrateSplit_ServersCreatedByAnotherWriter(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "tool-sources.yaml"), legacyFixture)
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\n# owner\nsets: {}\n")
	old := writeSplitFile
	writeSplitFile = func(path string, data []byte) (os.FileInfo, error) {
		if filepath.Base(path) == serversFile {
			if err := os.WriteFile(path, []byte("version: 1\n# operator\nservers: {}\n"), 0o600); err != nil {
				return nil, err
			}
		}
		return old(path, data)
	}
	defer func() { writeSplitFile = old }()
	if err := MigrateSplit(dir); err == nil {
		t.Fatal("want an error")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, serversFile)); !strings.Contains(string(b), "# operator") { //nolint:gosec // test file
		t.Fatalf("the operator's servers.yaml was touched: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, setsFile)); !strings.Contains(string(b), "# owner") { //nolint:gosec // test file
		t.Fatalf("the sets were not rolled back to the owner's: %s", b)
	}
}

// The migrate lock is held from the set edit until it is kept or undone,
// so a migration cannot copy a tentative tool into tool-sets.yaml.
func TestAddToSet_HoldsLockUntilKeptOrUndone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tool-sources.yaml")
	writeYAML(t, p, legacyFixture)
	edit, err := AddToSet(p, "jira:readonly", "new_tool")
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSplit(dir); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("migrate during a pending set edit: %v, want a refusal", err)
	}
	if err := edit.Undo(); err != nil {
		t.Fatal(err)
	}
	edit.Keep() // after Undo: harmless
	if err := MigrateSplit(dir); err != nil {
		t.Fatalf("after undo: %v", err)
	}
	reg, _ := Load(p)
	for _, tool := range reg.Sets["jira:readonly"].Tools {
		if tool == "new_tool" {
			t.Fatal("the undone tool reached tool-sets.yaml")
		}
	}
}
