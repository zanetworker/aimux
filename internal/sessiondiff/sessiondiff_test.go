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
