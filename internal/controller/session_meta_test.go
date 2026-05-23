package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/history"
)

// tempSessionFile creates a minimal .jsonl file in t.TempDir() and returns its path.
func tempSessionFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("create temp session file: %v", err)
	}
	return path
}

func TestToggleStar(t *testing.T) {
	sf := tempSessionFile(t)

	// First toggle: unstarred -> starred
	starred, err := ToggleStar(sf)
	if err != nil {
		t.Fatalf("first ToggleStar: %v", err)
	}
	if !starred {
		t.Error("expected starred=true after first toggle")
	}

	// Verify persisted
	meta := history.LoadMeta(sf)
	if !meta.Starred {
		t.Error("persisted meta should be starred")
	}

	// Second toggle: starred -> unstarred
	starred, err = ToggleStar(sf)
	if err != nil {
		t.Fatalf("second ToggleStar: %v", err)
	}
	if starred {
		t.Error("expected starred=false after second toggle")
	}

	meta = history.LoadMeta(sf)
	if meta.Starred {
		t.Error("persisted meta should be unstarred after second toggle")
	}
}

func TestToggleStar_InvalidPath(t *testing.T) {
	// Non-existent directory should fail on SaveMeta
	_, err := ToggleStar("/nonexistent/dir/session.jsonl")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestSetAnnotation(t *testing.T) {
	sf := tempSessionFile(t)

	if err := SetAnnotation(sf, "achieved"); err != nil {
		t.Fatalf("SetAnnotation: %v", err)
	}

	meta := history.LoadMeta(sf)
	if meta.Annotation != "achieved" {
		t.Errorf("expected annotation %q, got %q", "achieved", meta.Annotation)
	}
}

func TestSetAnnotation_Overwrite(t *testing.T) {
	sf := tempSessionFile(t)

	if err := SetAnnotation(sf, "partial"); err != nil {
		t.Fatalf("first SetAnnotation: %v", err)
	}
	if err := SetAnnotation(sf, "achieved"); err != nil {
		t.Fatalf("second SetAnnotation: %v", err)
	}

	meta := history.LoadMeta(sf)
	if meta.Annotation != "achieved" {
		t.Errorf("expected annotation %q after overwrite, got %q", "achieved", meta.Annotation)
	}
}

func TestSetTags(t *testing.T) {
	sf := tempSessionFile(t)

	tags := []string{"regression", "p1"}
	if err := SetTags(sf, tags); err != nil {
		t.Fatalf("SetTags: %v", err)
	}

	meta := history.LoadMeta(sf)
	if len(meta.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(meta.Tags))
	}
	if meta.Tags[0] != "regression" || meta.Tags[1] != "p1" {
		t.Errorf("expected tags [regression p1], got %v", meta.Tags)
	}
}

func TestSetTags_Empty(t *testing.T) {
	sf := tempSessionFile(t)

	// Set tags then clear them
	if err := SetTags(sf, []string{"a", "b"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	if err := SetTags(sf, nil); err != nil {
		t.Fatalf("SetTags (clear): %v", err)
	}

	meta := history.LoadMeta(sf)
	if len(meta.Tags) != 0 {
		t.Errorf("expected empty tags after clear, got %v", meta.Tags)
	}
}

func TestSetNote(t *testing.T) {
	sf := tempSessionFile(t)

	if err := SetNote(sf, "Fixed the flaky CI test"); err != nil {
		t.Fatalf("SetNote: %v", err)
	}

	meta := history.LoadMeta(sf)
	if meta.Note != "Fixed the flaky CI test" {
		t.Errorf("expected note %q, got %q", "Fixed the flaky CI test", meta.Note)
	}
}

func TestSetNote_PreservesOtherFields(t *testing.T) {
	sf := tempSessionFile(t)

	// Set starred and annotation first
	if _, err := ToggleStar(sf); err != nil {
		t.Fatalf("ToggleStar: %v", err)
	}
	if err := SetAnnotation(sf, "achieved"); err != nil {
		t.Fatalf("SetAnnotation: %v", err)
	}

	// Now set note -- should not clobber starred or annotation
	if err := SetNote(sf, "my note"); err != nil {
		t.Fatalf("SetNote: %v", err)
	}

	meta := history.LoadMeta(sf)
	if !meta.Starred {
		t.Error("SetNote clobbered starred field")
	}
	if meta.Annotation != "achieved" {
		t.Errorf("SetNote clobbered annotation: got %q", meta.Annotation)
	}
	if meta.Note != "my note" {
		t.Errorf("expected note %q, got %q", "my note", meta.Note)
	}
}
