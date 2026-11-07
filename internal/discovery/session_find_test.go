package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

const findTestID = "1f29de94-74c3-4089-94f7-df066c9fc5f5"

func writeSessionAt(t *testing.T, projects, dirKey string) string {
	t.Helper()
	dir := filepath.Join(projects, dirKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, findTestID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectDirKey(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/me/go/src/github.com/x/aimux": "-Users-me-go-src-github-com-x-aimux",
		"/tmp/my_proj.v2":                    "-tmp-my-proj-v2",
		"/":                                  "-",
		"":                                   "",
	} {
		if got := ProjectDirKey(in); got != want {
			t.Errorf("ProjectDirKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindSessionFileAt_PrefersTheCwdsProjectDir(t *testing.T) {
	projects := t.TempDir()
	writeSessionAt(t, projects, "-A-other") // sorts first: the glob's pick
	want := writeSessionAt(t, projects, ProjectDirKey("/work/repo.x"))
	if got := FindSessionFileAt(findTestID, "/work/repo.x", projects); got != want {
		t.Errorf("FindSessionFileAt = %q, want the cwd's project file %q", got, want)
	}
}

func TestFindSessionFileAt_FallsBackToSearch(t *testing.T) {
	projects := t.TempDir()
	want := writeSessionAt(t, projects, "-started-elsewhere")
	if got := FindSessionFileAt(findTestID, "/now/somewhere/else", projects); got != want {
		t.Errorf("FindSessionFileAt = %q, want %q", got, want)
	}
	if got := FindSessionFileAt(findTestID, "", projects); got != want {
		t.Errorf("no cwd: FindSessionFileAt = %q, want %q", got, want)
	}
	if got := FindSessionFileAt("missing-id", "/now/somewhere/else", projects); got != "" {
		t.Errorf("missing session: got %q, want empty", got)
	}
}

func TestFindSessionFileAt_RejectsUnsafeIDs(t *testing.T) {
	projects := t.TempDir()
	writeSessionAt(t, projects, ProjectDirKey("/w"))
	for _, id := range []string{"../" + findTestID, "*", findTestID + "/x", ""} {
		if got := FindSessionFileAt(id, "/w", projects); got != "" {
			t.Errorf("id %q found %q", id, got)
		}
	}
}

func TestFindSessionFile_RememberedPathRechecked(t *testing.T) {
	projects := t.TempDir()
	first := writeSessionAt(t, projects, "-p1")
	if got := FindSessionFile(findTestID, projects); got != first {
		t.Fatalf("got %q, want %q", got, first)
	}
	// The remembered file moved: the next lookup searches again.
	if err := os.RemoveAll(filepath.Dir(first)); err != nil {
		t.Fatal(err)
	}
	moved := writeSessionAt(t, projects, "-p2")
	if got := FindSessionFile(findTestID, projects); got != moved {
		t.Errorf("after the move got %q, want %q", got, moved)
	}
	// Gone everywhere: nothing.
	if err := os.RemoveAll(filepath.Dir(moved)); err != nil {
		t.Fatal(err)
	}
	if got := FindSessionFile(findTestID, projects); got != "" {
		t.Errorf("after removal got %q, want empty", got)
	}
}

func TestFindSessionFile_PerProjectsDir(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	inA := writeSessionAt(t, a, "-p")
	inB := writeSessionAt(t, b, "-p")
	if got := FindSessionFile(findTestID, a); got != inA {
		t.Errorf("dir a: got %q", got)
	}
	if got := FindSessionFile(findTestID, b); got != inB {
		t.Errorf("dir b: got %q, want its own file %q", got, inB)
	}
}
