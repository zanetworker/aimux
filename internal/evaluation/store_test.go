package evaluation

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewStoreCreatesCorrectPath(t *testing.T) {
	store := NewStore("sess-abc-123")

	if store.sessionID != "sess-abc-123" {
		t.Errorf("sessionID = %q, want %q", store.sessionID, "sess-abc-123")
	}

	// The directory should end with .aimux/evaluations
	if !filepath.IsAbs(store.dir) {
		t.Errorf("dir should be absolute, got %q", store.dir)
	}
	if filepath.Base(store.dir) != "evaluations" {
		t.Errorf("dir should end with 'evaluations', got %q", store.dir)
	}
	parent := filepath.Base(filepath.Dir(store.dir))
	if parent != ".aimux" {
		t.Errorf("parent dir should be '.aimux', got %q", parent)
	}
}

func TestStorePath(t *testing.T) {
	store := &Store{sessionID: "test-session", dir: "/tmp/evals"}
	got := store.path()
	want := "/tmp/evals/test-session.jsonl"
	if got != want {
		t.Errorf("path() = %q, want %q", got, want)
	}
}

func TestSaveCreatesDirectoriesAndWritesJSONL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "evaluations")
	store := &Store{sessionID: "save-test", dir: dir}

	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	a := Annotation{Turn: 1, Label: "good", Note: "looks correct", Timestamp: ts}

	if err := store.Save(a); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("Save() did not create the directory")
	}

	// Verify the JSONL file contents
	data, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("failed to read JSONL file: %v", err)
	}

	var got Annotation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to unmarshal JSONL line: %v", err)
	}
	if got.Turn != 1 {
		t.Errorf("Turn = %d, want 1", got.Turn)
	}
	if got.Label != "good" {
		t.Errorf("Label = %q, want %q", got.Label, "good")
	}
	if got.Note != "looks correct" {
		t.Errorf("Note = %q, want %q", got.Note, "looks correct")
	}
}

func TestSaveAppendsMultipleAnnotations(t *testing.T) {
	store := &Store{sessionID: "append-test", dir: t.TempDir()}

	ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})
	store.Save(Annotation{Turn: 2, Label: "bad", Timestamp: ts.Add(time.Minute)})
	store.Save(Annotation{Turn: 3, Label: "wasteful", Timestamp: ts.Add(2 * time.Minute)})

	f, err := os.Open(store.path())
	if err != nil {
		t.Fatalf("failed to open JSONL file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
	}
	if lineCount != 3 {
		t.Errorf("expected 3 JSONL lines, got %d", lineCount)
	}
}

func TestLoadReturnsAnnotations(t *testing.T) {
	store := &Store{sessionID: "load-test", dir: t.TempDir()}

	ts := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)
	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})
	store.Save(Annotation{Turn: 2, Label: "bad", Note: "wrong tool", Timestamp: ts.Add(time.Second)})

	annotations, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(annotations) != 2 {
		t.Fatalf("Load() returned %d annotations, want 2", len(annotations))
	}
	if annotations[0].Label != "good" {
		t.Errorf("annotations[0].Label = %q, want %q", annotations[0].Label, "good")
	}
	if annotations[1].Label != "bad" {
		t.Errorf("annotations[1].Label = %q, want %q", annotations[1].Label, "bad")
	}
	if annotations[1].Note != "wrong tool" {
		t.Errorf("annotations[1].Note = %q, want %q", annotations[1].Note, "wrong tool")
	}
}

func TestLoadReturnsEmptySliceForMissingFile(t *testing.T) {
	store := &Store{sessionID: "nonexistent", dir: t.TempDir()}

	annotations, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if annotations != nil {
		t.Errorf("Load() returned %v, want nil for missing file", annotations)
	}
}

func TestLoadSkipsMalformedLines(t *testing.T) {
	store := &Store{sessionID: "malformed-test", dir: t.TempDir()}

	// Write a mix of valid and invalid lines
	content := `{"turn":1,"label":"good","timestamp":"2026-01-01T00:00:00Z"}
not valid json
{"turn":2,"label":"bad","timestamp":"2026-01-01T00:01:00Z"}
`
	os.MkdirAll(store.dir, 0o755)
	os.WriteFile(store.path(), []byte(content), 0o644)

	annotations, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(annotations) != 2 {
		t.Errorf("Load() returned %d annotations, want 2 (skipping malformed)", len(annotations))
	}
}

func TestGetForTurnReturnsLatestAnnotation(t *testing.T) {
	store := &Store{sessionID: "getforturn-test", dir: t.TempDir()}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Save two annotations for turn 1 (latest should win)
	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})
	store.Save(Annotation{Turn: 1, Label: "bad", Timestamp: ts.Add(time.Minute)})
	store.Save(Annotation{Turn: 2, Label: "wasteful", Timestamp: ts.Add(2 * time.Minute)})

	got := store.GetForTurn(1)
	if got == nil {
		t.Fatal("GetForTurn(1) returned nil")
	}
	if got.Label != "bad" {
		t.Errorf("GetForTurn(1).Label = %q, want %q (latest)", got.Label, "bad")
	}
}

func TestGetForTurnReturnsNilForMissingTurn(t *testing.T) {
	store := &Store{sessionID: "getforturn-missing", dir: t.TempDir()}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})

	got := store.GetForTurn(99)
	if got != nil {
		t.Errorf("GetForTurn(99) = %v, want nil", got)
	}
}

func TestGetForTurnReturnsNilForEmptyStore(t *testing.T) {
	store := &Store{sessionID: "empty-store", dir: t.TempDir()}

	got := store.GetForTurn(1)
	if got != nil {
		t.Errorf("GetForTurn(1) on empty store = %v, want nil", got)
	}
}

func TestRemoveDeletesAnnotationsForTurn(t *testing.T) {
	store := &Store{sessionID: "remove-test", dir: t.TempDir()}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})
	store.Save(Annotation{Turn: 2, Label: "bad", Timestamp: ts.Add(time.Second)})
	store.Save(Annotation{Turn: 3, Label: "wasteful", Timestamp: ts.Add(2 * time.Second)})

	if err := store.Remove(2); err != nil {
		t.Fatalf("Remove(2) error: %v", err)
	}

	annotations, err := store.Load()
	if err != nil {
		t.Fatalf("Load() after Remove error: %v", err)
	}
	if len(annotations) != 2 {
		t.Fatalf("Load() returned %d annotations after Remove, want 2", len(annotations))
	}
	for _, a := range annotations {
		if a.Turn == 2 {
			t.Error("Remove(2) did not remove turn 2 annotation")
		}
	}
}

func TestRemoveDeletesFileWhenEmpty(t *testing.T) {
	store := &Store{sessionID: "remove-empty", dir: t.TempDir()}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})

	if err := store.Remove(1); err != nil {
		t.Fatalf("Remove(1) error: %v", err)
	}

	if _, err := os.Stat(store.path()); !os.IsNotExist(err) {
		t.Error("Remove() should delete file when no annotations remain")
	}
}

func TestRemoveNoOpForMissingTurn(t *testing.T) {
	store := &Store{sessionID: "remove-noop", dir: t.TempDir()}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.Save(Annotation{Turn: 1, Label: "good", Timestamp: ts})

	if err := store.Remove(99); err != nil {
		t.Fatalf("Remove(99) error: %v", err)
	}

	annotations, _ := store.Load()
	if len(annotations) != 1 {
		t.Errorf("Remove(99) changed annotation count: got %d, want 1", len(annotations))
	}
}

func TestExportPathReturnsCorrectPath(t *testing.T) {
	path := ExportPath("sess-xyz")

	if !filepath.IsAbs(path) {
		t.Errorf("ExportPath should return absolute path, got %q", path)
	}
	if filepath.Base(path) != "sess-xyz-export.jsonl" {
		t.Errorf("filename = %q, want %q", filepath.Base(path), "sess-xyz-export.jsonl")
	}
	parent := filepath.Base(filepath.Dir(path))
	if parent != "exports" {
		t.Errorf("parent dir = %q, want %q", parent, "exports")
	}
}

func TestWriteExportCreatesDirectoriesAndWritesJSONL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "exports")
	path := filepath.Join(dir, "test-export.jsonl")

	turns := []ExportTurn{
		{
			Turn:       1,
			Timestamp:  "2026-01-01T10:00:00Z",
			Input:      "fix the bug",
			Output:     "Done.",
			Actions:    []ExportAction{{Tool: "Read", Input: "main.go", Success: true}},
			TokensIn:   100,
			TokensOut:  50,
			CostUSD:    0.01,
			DurationMs: 5000,
			Label:      "good",
		},
		{
			Turn:       2,
			Timestamp:  "2026-01-01T10:01:00Z",
			Input:      "run the tests",
			Output:     "All passed.",
			Actions:    []ExportAction{{Tool: "Bash", Input: "go test", Success: true}},
			TokensIn:   200,
			TokensOut:  100,
			CostUSD:    0.02,
			DurationMs: 3000,
		},
	}

	if err := WriteExport(path, turns, nil); err != nil {
		t.Fatalf("WriteExport() error: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("WriteExport() did not create the directory")
	}

	// Read back and verify
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read export file: %v", err)
	}

	lines := splitNonEmpty(string(data))
	if len(lines) != 2 {
		t.Fatalf("export file has %d lines, want 2", len(lines))
	}

	var turn1 ExportTurn
	if err := json.Unmarshal([]byte(lines[0]), &turn1); err != nil {
		t.Fatalf("failed to unmarshal line 1: %v", err)
	}
	if turn1.Turn != 1 {
		t.Errorf("turn1.Turn = %d, want 1", turn1.Turn)
	}
	if turn1.Input != "fix the bug" {
		t.Errorf("turn1.Input = %q, want %q", turn1.Input, "fix the bug")
	}
	if turn1.Label != "good" {
		t.Errorf("turn1.Label = %q, want %q", turn1.Label, "good")
	}
	if len(turn1.Actions) != 1 {
		t.Errorf("turn1.Actions has %d entries, want 1", len(turn1.Actions))
	}
}

func TestWriteExportEmptyTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-export.jsonl")

	if err := WriteExport(path, nil, nil); err != nil {
		t.Fatalf("WriteExport(nil) error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read export file: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file for nil turns, got %d bytes", len(data))
	}
}

// splitNonEmpty splits a string by newlines and returns non-empty lines.
func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
