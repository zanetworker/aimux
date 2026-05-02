package diff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseDiffStat(t *testing.T) {
	input := ` internal/history/history.go      | 23 ++++++++++++-----------
 internal/history/history_test.go | 15 +++++++++++++++
 internal/tui/app.go              |  8 ++++----
 3 files changed, 31 insertions(+), 15 deletions(-)`

	files, total := ParseDiffStat(input)

	// Verify file count
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d", len(files))
	}

	// Verify first file
	if files[0].Path != "internal/history/history.go" {
		t.Errorf("expected first file to be 'internal/history/history.go', got '%s'", files[0].Path)
	}

	// Verify total stats
	if total.FileCount != 3 {
		t.Errorf("expected 3 files changed, got %d", total.FileCount)
	}
	if total.Insertions != 31 {
		t.Errorf("expected 31 insertions, got %d", total.Insertions)
	}
	if total.Deletions != 15 {
		t.Errorf("expected 15 deletions, got %d", total.Deletions)
	}
}

func TestGetDiffStatInGitRepo(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")

	// Create and commit a file
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "test.txt")
	runGitCmd(t, dir, "commit", "-m", "initial commit")

	// Make an uncommitted change
	if err := os.WriteFile(testFile, []byte("initial content\nmodified line\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get diff stat
	stat, err := GetDiffStat(dir)
	if err != nil {
		t.Fatalf("GetDiffStat failed: %v", err)
	}

	if stat == "" {
		t.Error("expected non-empty diff stat, got empty string")
	}

	// Verify it contains the file name
	if !contains(stat, "test.txt") {
		t.Errorf("expected diff stat to contain 'test.txt', got: %s", stat)
	}
}

func TestGetDiffStatNoChanges(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")

	// Create and commit a file
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "test.txt")
	runGitCmd(t, dir, "commit", "-m", "initial commit")

	// No uncommitted changes
	stat, err := GetDiffStat(dir)
	if err != nil {
		t.Fatalf("GetDiffStat failed: %v", err)
	}

	if stat != "" {
		t.Errorf("expected empty diff stat, got: %s", stat)
	}
}

func TestGetFullDiff(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")

	// Create and commit a file
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "test.txt")
	runGitCmd(t, dir, "commit", "-m", "initial commit")

	// Make an uncommitted change
	if err := os.WriteFile(testFile, []byte("initial content\nmodified line\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get full diff
	diff, err := GetFullDiff(dir)
	if err != nil {
		t.Fatalf("GetFullDiff failed: %v", err)
	}

	if diff == "" {
		t.Error("expected non-empty diff, got empty string")
	}

	// Verify it contains diff markers
	if !contains(diff, "+modified line") {
		t.Errorf("expected diff to contain '+modified line', got: %s", diff)
	}
}

// Helper functions

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, out)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestFormatCompact(t *testing.T) {
	input := ` internal/history/history.go      | 23 ++++++++++++-----------
 internal/history/history_test.go | 15 +++++++++++++++
 internal/tui/app.go              |  8 ++++----
 3 files changed, 31 insertions(+), 15 deletions(-)`

	result := FormatCompact(input)

	if result == "" {
		t.Error("expected non-empty result, got empty string")
	}

	// Verify header line
	if !contains(result, "Uncommitted: 3 files (+31 / -15)") {
		t.Errorf("expected header 'Uncommitted: 3 files (+31 / -15)', got: %s", result)
	}

	// Verify file paths
	if !contains(result, "internal/history/history.go") {
		t.Errorf("expected result to contain 'internal/history/history.go', got: %s", result)
	}
	if !contains(result, "internal/history/history_test.go") {
		t.Errorf("expected result to contain 'internal/history/history_test.go', got: %s", result)
	}
	if !contains(result, "internal/tui/app.go") {
		t.Errorf("expected result to contain 'internal/tui/app.go', got: %s", result)
	}
}

func TestFormatCompactEmpty(t *testing.T) {
	result := FormatCompact("")

	if result != "" {
		t.Errorf("expected empty result, got: %s", result)
	}
}
