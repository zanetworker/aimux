package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecentDirs_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")
	codexDir := filepath.Join(tmpDir, "codex-sessions")

	// Directories don't exist — should return empty, not error
	dirs := recentDirs(20, claudeDir, codexDir)
	if len(dirs) != 0 {
		t.Errorf("expected 0 dirs, got %d", len(dirs))
	}
}

func TestRecentDirs_Claude(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")

	// Create a mock Claude project directory with a session file
	projectKey := "-Users-me-projects-blog"
	projectDir := filepath.Join(claudeDir, projectKey)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(projectDir, "abc123.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"summary"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	codexDir := filepath.Join(tmpDir, "codex-sessions")
	dirs := recentDirs(20, claudeDir, codexDir)

	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(dirs))
	}
	if dirs[0].Path != projectKey {
		t.Errorf("Path = %q, want %q", dirs[0].Path, projectKey)
	}
	if dirs[0].Provider != "claude" {
		t.Errorf("Provider = %q, want %q", dirs[0].Provider, "claude")
	}
	if dirs[0].LastUsed.IsZero() {
		t.Error("LastUsed is zero")
	}
}

func TestRecentDirs_Codex(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")
	codexDir := filepath.Join(tmpDir, "codex-sessions")

	// Create a mock Codex session file with session_meta containing cwd
	dateDir := filepath.Join(codexDir, "2026", "02", "28")
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := map[string]string{
		"type": "session_meta",
		"cwd":  "/Users/me/projects/my-app",
	}
	metaJSON, _ := json.Marshal(meta)

	sessionFile := filepath.Join(dateDir, "session1.jsonl")
	if err := os.WriteFile(sessionFile, append(metaJSON, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := recentDirs(20, claudeDir, codexDir)

	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(dirs))
	}
	if dirs[0].Path != "/Users/me/projects/my-app" {
		t.Errorf("Path = %q, want %q", dirs[0].Path, "/Users/me/projects/my-app")
	}
	if dirs[0].Provider != "codex" {
		t.Errorf("Provider = %q, want %q", dirs[0].Provider, "codex")
	}
}

func TestRecentDirs_Dedup(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, "codex-sessions")

	// Create two Codex session files with the same cwd to test dedup.
	// (Claude uses dir-keys and Codex uses absolute paths, so cross-provider
	// dedup doesn't happen in practice. We test same-provider dedup here.)
	sharedCWD := "/Users/me/projects/shared"

	dateDir1 := filepath.Join(codexDir, "2026", "02", "27")
	dateDir2 := filepath.Join(codexDir, "2026", "02", "28")
	for _, d := range []string{dateDir1, dateDir2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	meta, _ := json.Marshal(map[string]string{"type": "session_meta", "cwd": sharedCWD})

	// Older file
	f1 := filepath.Join(dateDir1, "s1.jsonl")
	if err := os.WriteFile(f1, append(meta, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(f1, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Newer file, same cwd
	f2 := filepath.Join(dateDir2, "s2.jsonl")
	if err := os.WriteFile(f2, append(meta, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	claudeDir := filepath.Join(tmpDir, "claude-projects") // empty
	dirs := recentDirs(20, claudeDir, codexDir)

	if len(dirs) != 1 {
		t.Fatalf("expected 1 deduped dir, got %d: %+v", len(dirs), dirs)
	}
	if dirs[0].Path != sharedCWD {
		t.Errorf("Path = %q, want %q", dirs[0].Path, sharedCWD)
	}
	if dirs[0].Provider != "codex" {
		t.Errorf("Provider = %q, want %q", dirs[0].Provider, "codex")
	}
}

func TestRecentDirs_CrossProviderDedup(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")
	codexDir := filepath.Join(tmpDir, "codex-sessions")

	// Use a flat string (no slashes) as both the Claude dir-key and the
	// Codex cwd. This exercises the cross-provider dedup merge in recentDirs.
	sharedKey := "shared-project"

	// Claude entry
	projectDir := filepath.Join(claudeDir, sharedKey)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "s1.jsonl"), []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Codex entry with the same cwd value
	dateDir := filepath.Join(codexDir, "2026", "02", "28")
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]string{"type": "session_meta", "cwd": sharedKey})
	if err := os.WriteFile(filepath.Join(dateDir, "s2.jsonl"), append(meta, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := recentDirs(20, claudeDir, codexDir)

	if len(dirs) != 1 {
		t.Fatalf("expected 1 deduped dir, got %d: %+v", len(dirs), dirs)
	}
	if dirs[0].Provider != "both" {
		t.Errorf("Provider = %q, want %q", dirs[0].Provider, "both")
	}
}

func TestRecentDirs_SortedByRecency(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")
	codexDir := filepath.Join(tmpDir, "codex-sessions")

	// Create two Claude project dirs with different mod times
	olderKey := "-Users-me-older-project"
	newerKey := "-Users-me-newer-project"

	olderDir := filepath.Join(claudeDir, olderKey)
	newerDir := filepath.Join(claudeDir, newerKey)
	for _, d := range []string{olderDir, newerDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Write older file first
	olderFile := filepath.Join(olderDir, "s1.jsonl")
	if err := os.WriteFile(olderFile, []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Set modification time to 1 hour ago
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(olderFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Write newer file
	newerFile := filepath.Join(newerDir, "s2.jsonl")
	if err := os.WriteFile(newerFile, []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := recentDirs(20, claudeDir, codexDir)

	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(dirs))
	}
	if dirs[0].Path != newerKey {
		t.Errorf("first dir Path = %q, want %q (newest first)", dirs[0].Path, newerKey)
	}
	if dirs[1].Path != olderKey {
		t.Errorf("second dir Path = %q, want %q (oldest last)", dirs[1].Path, olderKey)
	}
}

func TestRecentDirs_MaxResults(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")
	codexDir := filepath.Join(tmpDir, "codex-sessions")

	// Create 5 Claude project dirs
	for i := 0; i < 5; i++ {
		key := filepath.Join(claudeDir, "-Users-me-project-"+string(rune('a'+i)))
		if err := os.MkdirAll(key, 0o755); err != nil {
			t.Fatal(err)
		}
		sessionFile := filepath.Join(key, "s.jsonl")
		if err := os.WriteFile(sessionFile, []byte(`{}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dirs := recentDirs(3, claudeDir, codexDir)
	if len(dirs) != 3 {
		t.Errorf("expected 3 dirs (capped), got %d", len(dirs))
	}
}

func TestScanClaudeDirs_SkipsEmptyDirs(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, "claude-projects")

	// Dir with no JSONL files
	emptyDir := filepath.Join(claudeDir, "-Users-me-empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Dir with a JSONL file
	withFile := filepath.Join(claudeDir, "-Users-me-active")
	if err := os.MkdirAll(withFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withFile, "s.jsonl"), []byte(`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := ScanClaudeDirs(claudeDir)
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir (skipping empty), got %d", len(dirs))
	}
	if dirs[0].Path != "-Users-me-active" {
		t.Errorf("Path = %q, want %q", dirs[0].Path, "-Users-me-active")
	}
}

func TestExtractCodexCWD(t *testing.T) {
	tmpDir := t.TempDir()

	meta := map[string]string{
		"type": "session_meta",
		"cwd":  "/home/user/project",
	}
	metaJSON, _ := json.Marshal(meta)
	content := append(metaJSON, '\n')
	content = append(content, []byte(`{"type":"message","text":"hello"}`+"\n")...)

	path := filepath.Join(tmpDir, "session.jsonl")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got := extractCodexCWD(path)
	if got != "/home/user/project" {
		t.Errorf("extractCodexCWD = %q, want %q", got, "/home/user/project")
	}
}

func TestExtractCodexCWD_NoCWD(t *testing.T) {
	tmpDir := t.TempDir()

	path := filepath.Join(tmpDir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := extractCodexCWD(path)
	if got != "" {
		t.Errorf("extractCodexCWD = %q, want empty string", got)
	}
}
