package badge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluate_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	content := `{"name": "my-app", "version": "1.0.0"}`
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0600)

	rules := []Rule{
		{Path: "package.json", JSONPath: "name", Label: "pkg"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != "my-app" {
		t.Errorf("expected 'my-app', got %q", badges[0].Value)
	}
	if badges[0].Label != "pkg" {
		t.Errorf("expected label 'pkg', got %q", badges[0].Label)
	}
}

func TestEvaluate_PlainTextFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".python-version"), []byte("3.11\n"), 0600)

	rules := []Rule{
		{Path: ".python-version", Label: "py"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != "3.11" {
		t.Errorf("expected '3.11', got %q", badges[0].Value)
	}
}

func TestEvaluate_MissingFile(t *testing.T) {
	dir := t.TempDir()
	rules := []Rule{
		{Path: "nonexistent.json", JSONPath: "name", Label: "x"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 0 {
		t.Errorf("expected 0 badges for missing file, got %d", len(badges))
	}
}

func TestEvaluate_NestedJSONPath(t *testing.T) {
	dir := t.TempDir()
	content := `{"engines": {"node": ">=18"}}`
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0600)

	rules := []Rule{
		{Path: "package.json", JSONPath: "engines.node", Label: "node"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != ">=18" {
		t.Errorf("expected '>=18', got %q", badges[0].Value)
	}
}

func TestEvaluate_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid json"), 0600)

	rules := []Rule{
		{Path: "bad.json", JSONPath: "name", Label: "x"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 0 {
		t.Errorf("expected 0 badges for invalid JSON, got %d", len(badges))
	}
}

func TestEvaluate_EmptyJSONPathReadsFirstLine(t *testing.T) {
	dir := t.TempDir()
	content := `{"name": "test"}`
	_ = os.WriteFile(filepath.Join(dir, "data.json"), []byte(content), 0600)

	rules := []Rule{
		{Path: "data.json", Label: "raw"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != `{"name": "test"}` {
		t.Errorf("expected full first line, got %q", badges[0].Value)
	}
}

func TestEvaluate_BadJSONPath(t *testing.T) {
	dir := t.TempDir()
	content := `{"name": "test"}`
	_ = os.WriteFile(filepath.Join(dir, "pkg.json"), []byte(content), 0600)

	rules := []Rule{
		{Path: "pkg.json", JSONPath: "nonexistent.deep.path", Label: "x"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 0 {
		t.Errorf("expected 0 badges for bad JSON path, got %d", len(badges))
	}
}

func TestEvaluate_WithColor(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".node-version"), []byte("20.10.0"), 0600)

	rules := []Rule{
		{Path: ".node-version", Label: "node", Color: "#3C873A"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Color != "#3C873A" {
		t.Errorf("expected color '#3C873A', got %q", badges[0].Color)
	}
}
