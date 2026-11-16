package wikitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// newWiki builds <tmp>/wiki plus <tmp>/outside.md and <tmp>/wiki-private/x.md.
func newWiki(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "wiki")
	w := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w(filepath.Join(root, "a.md"), "Alpha beta\nsecond line about gamma\n")
	w(filepath.Join(root, "sub", "b c.md"), "spaced name: alpha and gamma and [[OpenShell]]\n")
	w(filepath.Join(root, "ü.md"), "umlaut file with alpha\n")
	w(filepath.Join(root, "notes.txt"), "alpha in text file\n")
	w(filepath.Join(root, "big.md"), strings.Repeat("x", 600*1024))
	w(filepath.Join(base, "outside.md"), "secret alpha\n")
	w(filepath.Join(base, "wiki-private", "x.md"), "private alpha\n")
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "escape.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "wiki-private"), filepath.Join(root, "sibling")); err != nil {
		t.Fatal(err)
	}
	return root
}

func paths(hits []Hit) []string {
	var p []string
	for _, h := range hits {
		p = append(p, h.Path)
	}
	return p
}

func TestSearchFindsWords(t *testing.T) {
	root := newWiki(t)
	hits, err := Search(root, "ALPHA gamma", 10)
	if err != nil {
		t.Fatal(err)
	}
	// "sub/b c.md" matches both words, a.md matches both too (alpha, gamma), ü.md only one.
	got := paths(hits)
	want := []string{"a.md", "sub/b c.md", "ü.md"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if hits[0].Line < 1 || hits[0].Snippet == "" {
		t.Errorf("hit missing line/snippet: %+v", hits[0])
	}
}

func TestSearchHandlesWikiLinkQuery(t *testing.T) {
	root := newWiki(t)
	hits, err := Search(root, "[[OpenShell]]", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "sub/b c.md" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSearchSkipsNonMarkdownAndEscapes(t *testing.T) {
	root := newWiki(t)
	hits, err := Search(root, "alpha", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if strings.HasSuffix(h.Path, ".txt") || strings.Contains(h.Path, "private") || h.Path == "escape.md" || strings.HasPrefix(h.Path, "sibling") {
			t.Errorf("unexpected hit %s", h.Path)
		}
	}
}

func TestSearchLimit(t *testing.T) {
	root := newWiki(t)
	hits, err := Search(root, "alpha", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("len = %d, want 2", len(hits))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	if hits, err := Search(newWiki(t), "  ", 10); err != nil || len(hits) != 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
}

func TestReadOK(t *testing.T) {
	root := newWiki(t)
	for rel, want := range map[string]string{"sub/b c.md": "spaced name", "ü.md": "umlaut"} {
		got, err := Read(root, rel, 512*1024)
		if err != nil || !strings.Contains(got, want) {
			t.Errorf("Read(%q) = %q, %v", rel, got, err)
		}
	}
}

func TestReadRejects(t *testing.T) {
	root := newWiki(t)
	tests := []struct {
		rel  string
		want error
	}{
		{"../outside.md", ErrOutsideRoot},
		{"../wiki-private/x.md", ErrOutsideRoot},
		{"/etc/hosts", ErrOutsideRoot},
		{"escape.md", ErrOutsideRoot},
		{"sibling/x.md", ErrOutsideRoot},
		{"notes.txt", ErrNotMarkdown},
		{"big.md", ErrTooLarge},
	}
	for _, tc := range tests {
		_, err := Read(root, tc.rel, 512*1024)
		if !errors.Is(err, tc.want) {
			t.Errorf("Read(%q) err = %v, want %v", tc.rel, err, tc.want)
		}
	}
}

func TestReadMissingIsNotOutsideRoot(t *testing.T) {
	_, err := Read(newWiki(t), "nope.md", 1024)
	if err == nil || errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v", err)
	}
}

func TestBackendListsTwoTools(t *testing.T) {
	b := NewBackend(newWiki(t), 0)
	raws, err := b.ListToolsRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range raws {
		var tool struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		if err := json.Unmarshal(r, &tool); err != nil {
			t.Fatal(err)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s schema = %v", tool.Name, tool.InputSchema)
		}
		names = append(names, tool.Name)
	}
	if fmt.Sprint(names) != "[wiki_search wiki_read]" {
		t.Fatalf("names = %v", names)
	}
}

func call(t *testing.T, b *Backend, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := b.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("go error: %v", err)
	}
	return res
}

func text(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestBackendCallErrorsAreToolErrors(t *testing.T) {
	b := NewBackend(newWiki(t), 0)
	for _, rel := range []string{"../outside.md", "notes.txt", "big.md", "nope.md"} {
		res := call(t, b, "wiki_read", map[string]any{"path": rel})
		if !res.IsError || text(res) == "" {
			t.Errorf("%s: IsError=%v text=%q", rel, res.IsError, text(res))
		}
	}
	if res := call(t, b, "wiki_read", map[string]any{}); !res.IsError {
		t.Error("missing path should be a tool error")
	}
	if res := call(t, b, "wiki_search", map[string]any{}); !res.IsError {
		t.Error("missing query should be a tool error")
	}
	if res := call(t, b, "bogus", nil); !res.IsError {
		t.Error("unknown tool should be a tool error")
	}
}

func TestBackendCallsWork(t *testing.T) {
	b := NewBackend(newWiki(t), 0)
	res := call(t, b, "wiki_read", map[string]any{"path": "ü.md"})
	if res.IsError || !strings.Contains(text(res), "umlaut") {
		t.Fatalf("read: %v %q", res.IsError, text(res))
	}
	res = call(t, b, "wiki_search", map[string]any{"query": "gamma", "limit": float64(1)})
	if res.IsError || strings.Count(text(res), ".md") != 1 {
		t.Fatalf("search: %v %q", res.IsError, text(res))
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchThroughSymlinkedRoot(t *testing.T) {
	root := newWiki(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(link, "gamma", 10)
	if err != nil || len(hits) != 2 || hits[0].Path != "a.md" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if _, err := Read(link, "a.md", 1024); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkToMissingOutsideIsOutsideRoot(t *testing.T) {
	root := newWiki(t)
	if err := os.Symlink("/nonexistent/zzz.md", filepath.Join(root, "ghost.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "ghost.md", 1024); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v", err)
	}
}

func TestFIFONamedMarkdown(t *testing.T) {
	root := newWiki(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := Read(root, "pipe.md", 1024); err == nil {
			t.Error("Read of FIFO should fail")
		}
		hits, err := Search(root, "alpha", 50)
		if err != nil {
			t.Error(err)
		}
		for _, h := range hits {
			if h.Path == "pipe.md" {
				t.Error("Search returned FIFO")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on FIFO")
	}
}

func TestSearchSkipsFilesOverCap(t *testing.T) {
	root := newWiki(t)
	if err := os.WriteFile(filepath.Join(root, "huge.md"), []byte("alpha "+strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewBackend(root, 1024)
	res := call(t, b, "wiki_search", map[string]any{"query": "alpha"})
	if strings.Contains(text(res), "huge.md") {
		t.Fatalf("over-cap file returned: %q", text(res))
	}
	if !strings.Contains(text(res), "a.md") {
		t.Fatalf("small file missing: %q", text(res))
	}
}

func TestBackendErrorsDoNotLeakRoot(t *testing.T) {
	root := newWiki(t)
	b := NewBackend(root, 0)
	for _, rel := range []string{"../outside.md", "escape.md", "nope.md", "notes.txt", "big.md"} {
		if res := call(t, b, "wiki_read", map[string]any{"path": rel}); strings.Contains(text(res), filepath.Dir(root)) {
			t.Errorf("%s leaks root: %q", rel, text(res))
		}
	}
	gone := NewBackend(filepath.Join(root, "missing"), 0)
	res := call(t, gone, "wiki_search", map[string]any{"query": "x"})
	if !res.IsError || strings.Contains(text(res), root) {
		t.Errorf("search error: %v %q", res.IsError, text(res))
	}
}
