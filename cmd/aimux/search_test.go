package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// docBlockingEmbedder answers the query at once but holds document
// embeddings until release closes, like a slow API catching up on live sessions.
type docBlockingEmbedder struct {
	query   string
	release chan struct{}
}

func (e *docBlockingEmbedder) Model() string { return "test-model" }

func (e *docBlockingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	for _, t := range texts {
		if t != e.query {
			<-e.release
			break
		}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func TestSessionContentSearch_DoesNotWaitForEmbedding(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "-Users-me-research")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","entrypoint":"cli","cwd":"/Users/me/research","message":{"content":"zanzibar walkthrough"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "5a5a5a5a.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &docBlockingEmbedder{query: "zanzibar", release: make(chan struct{})}
	svc := newInteractiveSearch()
	svc.DBPath, svc.ProjectsDir, svc.ArchiveDirs, svc.Embedder = filepath.Join(t.TempDir(), "s.db"), projects, nil, e
	orig := interactiveSearch
	interactiveSearch = svc
	t.Cleanup(func() { close(e.release); svc.Wait(); interactiveSearch = orig })

	done := make(chan int, 1)
	go func() {
		ms, err := sessionContentSearch("zanzibar", "")
		if err != nil {
			t.Error(err)
		}
		done <- len(ms)
	}()
	select {
	case n := <-done:
		if n != 1 {
			t.Errorf("got %d matches, want 1", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TUI search waited for document embeddings")
	}
}
