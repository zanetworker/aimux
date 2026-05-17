package sessiondiff

import (
	"testing"

	"github.com/zanetworker/aimux/internal/trace"
)

func TestExtract_EditAndWrite(t *testing.T) {
	turns := []trace.Turn{
		{
			Actions: []trace.ToolSpan{
				{Name: "Edit", FilePath: "auth.go", OldString: "old line", NewString: "new line"},
				{Name: "Write", FilePath: "auth_test.go", Content: "package auth\n\nfunc TestX() {}"},
			},
		},
	}

	diffs := Extract(turns)
	if len(diffs) != 2 {
		t.Fatalf("expected 2 files, got %d", len(diffs))
	}

	if diffs[0].Path != "auth.go" || diffs[0].Status != "modified" {
		t.Errorf("file 0: path=%q status=%q", diffs[0].Path, diffs[0].Status)
	}
	if diffs[0].Added != 1 || diffs[0].Removed != 1 {
		t.Errorf("file 0: added=%d removed=%d", diffs[0].Added, diffs[0].Removed)
	}

	if diffs[1].Path != "auth_test.go" || diffs[1].Status != "added" {
		t.Errorf("file 1: path=%q status=%q", diffs[1].Path, diffs[1].Status)
	}
	if diffs[1].Added != 3 {
		t.Errorf("file 1: added=%d, want 3", diffs[1].Added)
	}
}

func TestExtract_MultipleEditsToSameFile(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{{Name: "Edit", FilePath: "main.go", OldString: "a", NewString: "b"}}},
		{Actions: []trace.ToolSpan{{Name: "Edit", FilePath: "main.go", OldString: "c", NewString: "d"}}},
	}

	diffs := Extract(turns)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(diffs))
	}
	if len(diffs[0].Hunks) != 2 {
		t.Errorf("expected 2 hunks, got %d", len(diffs[0].Hunks))
	}
}

func TestExtract_Empty(t *testing.T) {
	diffs := Extract(nil)
	if len(diffs) != 0 {
		t.Errorf("expected 0 files, got %d", len(diffs))
	}
}

func TestExtract_FallbackToSnippet(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{{Name: "Edit", Snippet: "config.go", OldString: "x", NewString: "y"}}},
	}
	diffs := Extract(turns)
	if len(diffs) != 1 || diffs[0].Path != "config.go" {
		t.Errorf("expected path from snippet, got %v", diffs)
	}
}

func TestExtract_ShortPath(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{{Name: "Edit", FilePath: "cmd/aimux/cmd/spawn.go", OldString: "a", NewString: "b"}}},
	}
	diffs := Extract(turns)
	if diffs[0].ShortPath != "spawn.go" {
		t.Errorf("shortPath=%q, want %q", diffs[0].ShortPath, "spawn.go")
	}
}

func TestExtract_IgnoresNonEditWrite(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{
			{Name: "Bash", Snippet: "go test"},
			{Name: "Read", FilePath: "main.go"},
			{Name: "Edit", FilePath: "main.go", OldString: "a", NewString: "b"},
		}},
	}
	diffs := Extract(turns)
	if len(diffs) != 1 {
		t.Errorf("expected 1 file (only Edit), got %d", len(diffs))
	}
}

func TestExtract_WriteStatus(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{{Name: "Write", FilePath: "new.go", Content: "package main"}}},
	}
	diffs := Extract(turns)
	if diffs[0].Status != "added" {
		t.Errorf("Write should be 'added', got %q", diffs[0].Status)
	}
	if diffs[0].Removed != 0 {
		t.Errorf("Write should have 0 removed, got %d", diffs[0].Removed)
	}
}

func TestExtract_PreservesOrder(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{
			{Name: "Edit", FilePath: "b.go", OldString: "x", NewString: "y"},
			{Name: "Edit", FilePath: "a.go", OldString: "x", NewString: "y"},
			{Name: "Edit", FilePath: "c.go", OldString: "x", NewString: "y"},
		}},
	}
	diffs := Extract(turns)
	if diffs[0].Path != "b.go" || diffs[1].Path != "a.go" || diffs[2].Path != "c.go" {
		t.Errorf("expected order b,a,c; got %s,%s,%s", diffs[0].Path, diffs[1].Path, diffs[2].Path)
	}
}

func TestExtract_HunkLines(t *testing.T) {
	turns := []trace.Turn{
		{Actions: []trace.ToolSpan{{Name: "Edit", FilePath: "x.go", OldString: "line1\nline2", NewString: "line1\nline2\nline3"}}},
	}
	diffs := Extract(turns)
	hunk := diffs[0].Hunks[0]
	delCount, addCount := 0, 0
	for _, l := range hunk.Lines {
		if l.Type == "del" {
			delCount++
		}
		if l.Type == "add" {
			addCount++
		}
	}
	if delCount != 2 {
		t.Errorf("expected 2 del lines, got %d", delCount)
	}
	if addCount != 3 {
		t.Errorf("expected 3 add lines, got %d", addCount)
	}
}
