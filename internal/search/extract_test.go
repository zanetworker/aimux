package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSession(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	humanFirst  = `{"type":"user","entrypoint":"cli","cwd":"/Users/me/repo","message":{"role":"user","content":"why does openshell need service accounts"}}`
	assistantTx = `{"type":"assistant","message":{"content":[{"type":"text","text":"Because tokens must be short-lived."}]}}`
	toolUseBash = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"kubectl get sa -n openshell"}}]}}`
	toolUseEdit = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/docs/token-exchange.md","old_string":"a","new_string":"b"}}]}}`
	toolResult  = `{"type":"user","message":{"content":[{"type":"tool_result","content":"serviceaccount/default created"}]}}`
	humanImage  = `{"type":"user","message":{"content":[{"type":"text","text":"can I add the proposals to Ying's deck"},{"type":"image","source":{"data":"AAAA"}}]}}`
	aiTitle     = `{"type":"ai-title","aiTitle":"Openshell prerequisites"}`
	customTitle = `{"type":"custom-title","customTitle":"agent-ops"}`
)

func TestExtractFile_ChunksPerExchangeWithToolContext(t *testing.T) {
	path := writeSession(t, humanFirst, assistantTx, toolUseBash, toolUseEdit, toolResult, humanImage, aiTitle)

	doc, err := ExtractFile(path, DefaultExtractOpts())
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	if doc.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("SessionID = %q", doc.SessionID)
	}
	if doc.CWD != "/Users/me/repo" {
		t.Errorf("CWD = %q, want /Users/me/repo", doc.CWD)
	}
	if doc.Automated {
		t.Error("interactive cli session marked automated")
	}
	if len(doc.Chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (one per human message): %+v", len(doc.Chunks), doc.Chunks)
	}
	first := doc.Chunks[0].Text
	for _, want := range []string{"service accounts", "short-lived", "kubectl get sa", "/repo/docs/token-exchange.md", "serviceaccount/default created"} {
		if !strings.Contains(first, want) {
			t.Errorf("chunk 0 missing %q:\n%s", want, first)
		}
	}
	if !strings.Contains(doc.Chunks[1].Text, "Ying's deck") {
		t.Errorf("image message text not indexed: %q", doc.Chunks[1].Text)
	}
	if strings.Contains(doc.Chunks[1].Text, "AAAA") {
		t.Error("image data leaked into chunk text")
	}
	if doc.Title != "Openshell prerequisites" {
		t.Errorf("Title = %q, want ai-title", doc.Title)
	}
	if doc.FirstPrompt != "why does openshell need service accounts" {
		t.Errorf("FirstPrompt = %q", doc.FirstPrompt)
	}
}

func TestExtractFile_CustomTitleBeatsAITitle(t *testing.T) {
	path := writeSession(t, humanFirst, aiTitle, customTitle)
	doc, err := ExtractFile(path, DefaultExtractOpts())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "agent-ops" {
		t.Errorf("Title = %q, want custom title agent-ops", doc.Title)
	}
}

func TestExtractFile_TitleFallsBackToFirstPrompt(t *testing.T) {
	path := writeSession(t, humanFirst, assistantTx)
	doc, _ := ExtractFile(path, DefaultExtractOpts())
	if doc.Title != "why does openshell need service accounts" {
		t.Errorf("Title = %q, want first prompt", doc.Title)
	}
}

func TestExtractFile_DetectsAutomatedSessions(t *testing.T) {
	cases := map[string]string{
		"sdk entrypoint": `{"type":"user","entrypoint":"sdk-cli","cwd":"/Users/me/repo","message":{"content":"summarize this"}}`,
		"temp dir cwd":   `{"type":"user","entrypoint":"cli","cwd":"/private/var/folders/y2/T/tmpabc","message":{"content":"hello"}}`,
		"known prefix":   `{"type":"user","entrypoint":"cli","cwd":"/Users/me/repo","message":{"content":"YOU ARE A SESSION ANALYZER. Analyze..."}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			doc, err := ExtractFile(writeSession(t, line), DefaultExtractOpts())
			if err != nil {
				t.Fatal(err)
			}
			if !doc.Automated {
				t.Errorf("%s: want Automated=true", name)
			}
		})
	}
}

func TestExtractFile_SkipsMetaAndMalformedLines(t *testing.T) {
	meta := `{"type":"user","isMeta":true,"message":{"content":"<local-command-caveat>ignore</local-command-caveat>"}}`
	cmd := `{"type":"user","message":{"content":"<command-name>/clear</command-name>"}}`
	path := writeSession(t, meta, "{not json", cmd, humanFirst)
	doc, err := ExtractFile(path, DefaultExtractOpts())
	if err != nil {
		t.Fatalf("malformed line should be skipped, got %v", err)
	}
	if len(doc.Chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(doc.Chunks))
	}
	if strings.Contains(doc.Chunks[0].Text, "caveat") || strings.Contains(doc.Chunks[0].Text, "/clear") {
		t.Errorf("meta/command text indexed: %q", doc.Chunks[0].Text)
	}
}

func TestExtractFile_EmptyFile(t *testing.T) {
	doc, err := ExtractFile(writeSession(t, ""), DefaultExtractOpts())
	if err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if len(doc.Chunks) != 0 || doc.Automated {
		t.Errorf("empty file: chunks=%d automated=%v", len(doc.Chunks), doc.Automated)
	}
}

func TestExtractFile_MissingFile(t *testing.T) {
	if _, err := ExtractFile(filepath.Join(t.TempDir(), "nope.jsonl"), DefaultExtractOpts()); err == nil {
		t.Error("want error for missing file")
	}
}

func TestExtractFile_TruncatesAtLimits(t *testing.T) {
	longOut := strings.Repeat("x", 5000)
	result := `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + longOut + `"}]}}`
	opts := DefaultExtractOpts()
	opts.MaxToolOutputChars = 100
	opts.MaxChunkChars = 300

	doc, err := ExtractFile(writeSession(t, humanFirst, result), opts)
	if err != nil {
		t.Fatal(err)
	}
	got := doc.Chunks[0].Text
	if n := strings.Count(got, "x"); n > 100 {
		t.Errorf("tool output not trimmed: %d x's", n)
	}
	if len(got) > 300 {
		t.Errorf("chunk length %d exceeds MaxChunkChars 300", len(got))
	}
	// exactly at the limit is kept whole
	opts.MaxChunkChars = len("why does openshell need service accounts")
	doc, _ = ExtractFile(writeSession(t, humanFirst), opts)
	if doc.Chunks[0].Text != "why does openshell need service accounts" {
		t.Errorf("text at exact limit altered: %q", doc.Chunks[0].Text)
	}
}

func TestExtractFile_ProseExcludesToolNoise(t *testing.T) {
	doc, err := ExtractFile(writeSession(t, humanFirst, assistantTx, toolUseBash, toolUseEdit, toolResult), DefaultExtractOpts())
	if err != nil {
		t.Fatal(err)
	}
	prose := doc.Chunks[0].Prose
	for _, want := range []string{"service accounts", "short-lived"} {
		if !strings.Contains(prose, want) {
			t.Errorf("prose missing %q: %q", want, prose)
		}
	}
	for _, noise := range []string{"kubectl", "token-exchange.md", "serviceaccount/default created"} {
		if strings.Contains(prose, noise) {
			t.Errorf("prose contains tool noise %q: %q", noise, prose)
		}
	}
	// the searchable text still has everything
	if !strings.Contains(doc.Chunks[0].Text, "kubectl") {
		t.Error("Text lost tool context")
	}
}

func TestExtractFile_LongExchangeOverflowStaysSearchable(t *testing.T) {
	long := strings.Repeat("filler ", 1000) + "needle-at-the-end"
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"}]}}`
	opts := DefaultExtractOpts()
	doc, err := ExtractFile(writeSession(t, humanFirst, line), opts)
	if err != nil {
		t.Fatal(err)
	}
	c := doc.Chunks[0]
	if len(c.Text) > opts.MaxChunkChars {
		t.Errorf("main text is %d chars, want <= %d", len(c.Text), opts.MaxChunkChars)
	}
	if strings.Contains(c.Text, "needle-at-the-end") || !strings.Contains(c.More, "needle-at-the-end") {
		t.Error("text past the main budget must go to More, not be dropped")
	}
	if len(c.Text)+len(c.More) > opts.MaxTextChars {
		t.Errorf("text+more = %d chars, want <= %d", len(c.Text)+len(c.More), opts.MaxTextChars)
	}
	if len(c.Prose) > opts.MaxChunkChars/2 {
		t.Errorf("embedding prose is %d chars; it must stay within the embedding budget", len(c.Prose))
	}
}
