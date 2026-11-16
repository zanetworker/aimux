package sandboxlaunch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type dlBackend struct {
	recBackend
	sandbox, remote, local string
}

func (d *dlBackend) Download(_ context.Context, sandbox, remote, local string) error {
	d.sandbox, d.remote, d.local = sandbox, remote, local
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return err
	}
	return os.WriteFile(local, []byte("{}\n"), 0o644) //nolint:gosec // G306: a sloppy backend; Archive still ends at 0600 //; Archive still ends at 0600
}

const sid = "c7c471b7-31d8-43a4-a787-d8f8ccc504f4"

func TestArchive_RejectsNonUUID(t *testing.T) {
	d := &dlBackend{}
	dir := t.TempDir()
	for _, bad := range []string{"", "../../etc/passwd", "c7c471b7-31d8-43a4-a787", sid + "/x", "not-a-uuid"} {
		if err := Archive(context.Background(), d, "aimux-work-readonly", bad, dir); err == nil {
			t.Errorf("session id %q accepted", bad)
		}
	}
	if d.remote != "" {
		t.Fatalf("downloaded %s for a bad id", d.remote)
	}
}

func TestArchive_Writes0600UnderProviderDir(t *testing.T) {
	d := &dlBackend{}
	dir := t.TempDir()
	if err := Archive(context.Background(), d, "aimux-work-readonly", sid, dir); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	want := filepath.Join(dir, "openshell-aimux-work-readonly", sid+".jsonl")
	if d.sandbox != "aimux-work-readonly" || d.remote != "/sandbox/.claude/projects/-sandbox/"+sid+".jsonl" || d.local != want {
		t.Fatalf("download %s %s -> %s", d.sandbox, d.remote, d.local)
	}
	st, err := os.Stat(want)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v %v, want 0600", st, err)
	}
	if err := Archive(context.Background(), d, "../evil", sid, dir); err == nil {
		t.Fatal("a sandbox name with a path was accepted")
	}
}
