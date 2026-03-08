package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Helper: create a minimal Claude JSONL session file ---

func writeSessionJSONL(t *testing.T, dir, sessionID string, lines []map[string]interface{}) string {
	t.Helper()
	filePath := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}
	defer f.Close()
	for _, line := range lines {
		data, _ := json.Marshal(line)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	return filePath
}

func minimalSession(t *testing.T, dir, sessionID, userPrompt string, ts time.Time) string {
	t.Helper()
	lines := []map[string]interface{}{
		{
			"type":      "human",
			"timestamp": ts.Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": userPrompt},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": ts.Add(30 * time.Second).Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "I'll help with that."},
				},
				"usage": map[string]interface{}{
					"input_tokens":  1500,
					"output_tokens": 300,
				},
			},
		},
	}
	return writeSessionJSONL(t, dir, sessionID, lines)
}

// --- Tests ---

func TestDecodeProjectDir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"-Users-foo-myproject", "/Users-foo-myproject"},
		{"-Users-azaalouk-go-src-github-com-zanetworker-aimux", "/Users-azaalouk-go-src-github-com-zanetworker-aimux"},
		{"somedir", "somedir"},
		{"-", "/"},
	}
	for _, tt := range tests {
		got := decodeProjectDir(tt.input)
		if got != tt.want {
			t.Errorf("decodeProjectDir(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMetaPath(t *testing.T) {
	got := MetaPath("/path/to/abc123.jsonl")
	want := "/path/to/abc123.meta.json"
	if got != want {
		t.Errorf("MetaPath() = %q, want %q", got, want)
	}
}

func TestSaveAndLoadMeta(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "test-session.jsonl")
	// Create a dummy session file so the path exists
	os.WriteFile(sessionFile, []byte("{}"), 0o644)

	meta := Meta{
		Annotation: "failed",
		Note:       "agent looped for 20 turns",
		Tags:       []string{"loop-on-error", "wrong-file"},
	}

	if err := SaveMeta(sessionFile, meta); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	loaded := LoadMeta(sessionFile)
	if loaded.Annotation != "failed" {
		t.Errorf("Annotation = %q, want %q", loaded.Annotation, "failed")
	}
	if loaded.Note != "agent looped for 20 turns" {
		t.Errorf("Note = %q, want %q", loaded.Note, "agent looped for 20 turns")
	}
	if len(loaded.Tags) != 2 || loaded.Tags[0] != "loop-on-error" {
		t.Errorf("Tags = %v, want [loop-on-error wrong-file]", loaded.Tags)
	}
	if loaded.UpdatedAt == "" {
		t.Error("UpdatedAt should be set")
	}
}

func TestLoadMeta_NoFile(t *testing.T) {
	meta := LoadMeta("/nonexistent/path.jsonl")
	if meta.Annotation != "" || meta.Note != "" || len(meta.Tags) != 0 {
		t.Errorf("expected empty Meta for nonexistent file, got %+v", meta)
	}
}

func TestSaveMeta_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	os.WriteFile(sessionFile, []byte("{}"), 0o644)

	// Write initial metadata
	if err := SaveMeta(sessionFile, Meta{Annotation: "achieved"}); err != nil {
		t.Fatalf("first SaveMeta: %v", err)
	}

	// Overwrite with new metadata
	if err := SaveMeta(sessionFile, Meta{Annotation: "failed", Tags: []string{"bug"}}); err != nil {
		t.Fatalf("second SaveMeta: %v", err)
	}

	loaded := LoadMeta(sessionFile)
	if loaded.Annotation != "failed" {
		t.Errorf("Annotation = %q, want %q after overwrite", loaded.Annotation, "failed")
	}
	if len(loaded.Tags) != 1 || loaded.Tags[0] != "bug" {
		t.Errorf("Tags = %v, want [bug] after overwrite", loaded.Tags)
	}
}

func TestCollectTags(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-project")
	os.MkdirAll(projDir, 0o755)

	// Create two meta files with overlapping tags
	s1 := filepath.Join(projDir, "session1.jsonl")
	os.WriteFile(s1, []byte("{}"), 0o644)
	SaveMeta(s1, Meta{Tags: []string{"loop-on-error", "wrong-file"}})

	s2 := filepath.Join(projDir, "session2.jsonl")
	os.WriteFile(s2, []byte("{}"), 0o644)
	SaveMeta(s2, Meta{Tags: []string{"wrong-file", "hallucinated-api"}})

	tags := CollectTags(dir)
	if len(tags) != 3 {
		t.Fatalf("expected 3 unique tags, got %d: %v", len(tags), tags)
	}
	// Tags should be sorted
	if tags[0] != "hallucinated-api" || tags[1] != "loop-on-error" || tags[2] != "wrong-file" {
		t.Errorf("tags = %v, want [hallucinated-api loop-on-error wrong-file]", tags)
	}
}

func TestCollectTags_NoFiles(t *testing.T) {
	dir := t.TempDir()
	tags := CollectTags(dir)
	if len(tags) != 0 {
		t.Errorf("expected no tags, got %v", tags)
	}
}

func TestExtractUserText_BlockArray(t *testing.T) {
	content, _ := json.Marshal([]map[string]interface{}{
		{"type": "text", "text": "fix the bug in main.go"},
	})
	got := extractUserText(content)
	if got != "fix the bug in main.go" {
		t.Errorf("extractUserText = %q, want %q", got, "fix the bug in main.go")
	}
}

func TestExtractUserText_Truncation(t *testing.T) {
	longText := strings.Repeat("x", 200)
	content, _ := json.Marshal([]map[string]interface{}{
		{"type": "text", "text": longText},
	})
	got := extractUserText(content)
	if len(got) > 120 {
		t.Errorf("expected truncated text <= 120 chars, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected truncated text to end with ...")
	}
}

func TestExtractUserText_Nil(t *testing.T) {
	got := extractUserText(nil)
	if got != "" {
		t.Errorf("expected empty for nil content, got %q", got)
	}
}

func TestDiscover_BasicSession(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-myproject")
	os.MkdirAll(projDir, 0o755)

	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	minimalSession(t, projDir, "abc-123", "fix the markdown rendering", ts)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.ID != "abc-123" {
		t.Errorf("ID = %q, want %q", s.ID, "abc-123")
	}
	if s.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", s.Provider, "claude")
	}
	if s.FirstPrompt != "fix the markdown rendering" {
		t.Errorf("FirstPrompt = %q, want %q", s.FirstPrompt, "fix the markdown rendering")
	}
	if s.Resumable != true {
		t.Error("expected Resumable = true for Claude")
	}
	if s.StartTime.IsZero() {
		t.Error("expected non-zero StartTime")
	}
	if s.TokensIn != 1500 {
		t.Errorf("TokensIn = %d, want 1500", s.TokensIn)
	}
	if s.TokensOut != 300 {
		t.Errorf("TokensOut = %d, want 300", s.TokensOut)
	}
}

func TestDiscover_MultipleSessions_SortedByLastActive(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-project")
	os.MkdirAll(projDir, 0o755)

	// Older session
	ts1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	minimalSession(t, projDir, "old-session", "old task", ts1)

	// Newer session
	ts2 := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	minimalSession(t, projDir, "new-session", "new task", ts2)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Most recent first
	if sessions[0].ID != "new-session" {
		t.Errorf("first session = %q, want %q (most recent)", sessions[0].ID, "new-session")
	}
	if sessions[1].ID != "old-session" {
		t.Errorf("second session = %q, want %q (older)", sessions[1].ID, "old-session")
	}
}

func TestEncodeProjectDir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/foo/myproject", "-Users-foo-myproject"},
		{"/Users/foo/my.project", "-Users-foo-my-project"},
		{"/Users/azaalouk/go/src/github.com/zanetworker/aimux", "-Users-azaalouk-go-src-github-com-zanetworker-aimux"},
	}
	for _, tt := range tests {
		got := encodeProjectDir(tt.input)
		if got != tt.want {
			t.Errorf("encodeProjectDir(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDiscover_DirFilter(t *testing.T) {
	dir := t.TempDir()

	// Two projects with Claude-style encoded dir names
	proj1 := filepath.Join(dir, "-Users-test-project1")
	proj2 := filepath.Join(dir, "-Users-test-project2")
	os.MkdirAll(proj1, 0o755)
	os.MkdirAll(proj2, 0o755)

	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	minimalSession(t, proj1, "s1", "task one", ts)
	minimalSession(t, proj2, "s2", "task two", ts)

	// Filter using the real path (as the app would pass it)
	sessions, err := Discover(DiscoverOpts{Dir: "/Users/test/project1"}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session with dir filter, got %d", len(sessions))
	}
	if sessions[0].ID != "s1" {
		t.Errorf("filtered session = %q, want %q", sessions[0].ID, "s1")
	}
}

func TestDiscover_Limit(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-project")
	os.MkdirAll(projDir, 0o755)

	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		minimalSession(t, projDir, "session-"+string(rune('a'+i)), "task", ts.Add(time.Duration(i)*time.Hour))
	}

	sessions, err := Discover(DiscoverOpts{Limit: 3}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions with limit, got %d", len(sessions))
	}
}

func TestDiscover_NonexistentDir(t *testing.T) {
	sessions, err := Discover(DiscoverOpts{}, "/nonexistent/path")
	if err != nil {
		t.Fatalf("expected no error for nonexistent dir, got %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestDiscover_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestDiscover_WithMetadata(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-project")
	os.MkdirAll(projDir, 0o755)

	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	sessionPath := minimalSession(t, projDir, "annotated", "some task", ts)

	// Add metadata
	SaveMeta(sessionPath, Meta{
		Annotation: "failed",
		Note:       "kept looping",
		Tags:       []string{"loop-on-error"},
	})

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.Annotation != "failed" {
		t.Errorf("Annotation = %q, want %q", s.Annotation, "failed")
	}
	if s.Note != "kept looping" {
		t.Errorf("Note = %q, want %q", s.Note, "kept looping")
	}
	if len(s.Tags) != 1 || s.Tags[0] != "loop-on-error" {
		t.Errorf("Tags = %v, want [loop-on-error]", s.Tags)
	}
}

func TestDiscover_UnsupportedProvider(t *testing.T) {
	dir := t.TempDir()
	sessions, err := Discover(DiscoverOpts{Provider: "gemini"}, dir)
	if err != nil {
		t.Fatalf("expected no error for unsupported provider, got %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions for unsupported provider, got %d", len(sessions))
	}
}

func TestDiscover_CostCalculation(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-project")
	os.MkdirAll(projDir, 0o755)

	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	lines := []map[string]interface{}{
		{
			"type":      "human",
			"timestamp": ts.Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "hello"},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": ts.Add(30 * time.Second).Format(time.RFC3339),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-5",
				"role":  "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Hi!"},
				},
				"usage": map[string]interface{}{
					"input_tokens":                100000,
					"output_tokens":               10000,
					"cache_creation_input_tokens":  50000,
					"cache_read_input_tokens":      200000,
				},
			},
		},
	}
	writeSessionJSONL(t, projDir, "cost-test", lines)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.TokensIn != 100000 {
		t.Errorf("TokensIn = %d, want 100000", s.TokensIn)
	}
	if s.TokensOut != 10000 {
		t.Errorf("TokensOut = %d, want 10000", s.TokensOut)
	}
	if s.CacheReadTokens != 200000 {
		t.Errorf("CacheReadTokens = %d, want 200000", s.CacheReadTokens)
	}
	if s.CacheWriteTokens != 50000 {
		t.Errorf("CacheWriteTokens = %d, want 50000", s.CacheWriteTokens)
	}

	// claude-sonnet-4-5: $3/M in, $15/M out, $0.30/M cache read, $3.75/M cache write
	// Cost = 100000/1M * 3 + 10000/1M * 15 + 50000/1M * 3.75 + 200000/1M * 0.30
	// Cost = 0.30 + 0.15 + 0.1875 + 0.06 = 0.6975
	if s.CostUSD < 0.69 || s.CostUSD > 0.70 {
		t.Errorf("CostUSD = %.4f, want ~0.6975", s.CostUSD)
	}
}

func TestTitleForSessionFile(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "test.jsonl")
	os.WriteFile(sessionFile, []byte("{}"), 0o644)

	// No meta file — should return ""
	if got := TitleForSessionFile(sessionFile); got != "" {
		t.Errorf("expected empty title, got %q", got)
	}

	// With meta file
	SaveMeta(sessionFile, Meta{Title: "Fix markdown rendering"})
	if got := TitleForSessionFile(sessionFile); got != "Fix markdown rendering" {
		t.Errorf("expected title, got %q", got)
	}

	// Empty path
	if got := TitleForSessionFile(""); got != "" {
		t.Errorf("expected empty for empty path, got %q", got)
	}
}

func TestScanSession_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(filePath, []byte(""), 0o644)

	s, err := scanSession("empty", filePath, "/test")
	if err != nil {
		t.Fatalf("scanSession: %v", err)
	}
	if s.ID != "empty" {
		t.Errorf("ID = %q, want %q", s.ID, "empty")
	}
	if s.TurnCount != 0 {
		t.Errorf("TurnCount = %d, want 0 for empty file", s.TurnCount)
	}
}
