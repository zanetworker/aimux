// Package wikitools gives a sandboxed agent read-only search and read access
// to a markdown wiki. Page content is untrusted, so Read is a security
// boundary: it never returns a file outside the wiki root.
package wikitools

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrOutsideRoot = errors.New("path is outside the wiki root")
	ErrNotMarkdown = errors.New("only .md files can be read")
	ErrTooLarge    = errors.New("file is too large")
)

// DefaultMaxBytes is the read size cap used when none is given.
const DefaultMaxBytes int64 = 512 * 1024

const (
	maxSearchFileBytes = 2 << 20
	maxSnippetRunes    = 200
)

// Hit is one matching file: the best matching line and its text.
type Hit struct {
	Path    string // relative to the wiki root, slash separated
	Line    int    // 1-based
	Snippet string
}

// Search finds *.md files containing the query's words (case-insensitive
// substring per word), ranked by matched-word count then path. Symlinks and
// hidden directories are not followed.
func Search(root, query string, limit int) ([]Hit, error) {
	return search(root, query, limit, DefaultMaxBytes)
}

// search is Search that also skips files larger than maxBytes (the Read cap),
// so it never returns snippets from a page Read would refuse.
func search(root, query string, limit int, maxBytes int64) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 || limit <= 0 {
		return nil, nil
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("wiki root: %w", err)
	}
	type scored struct {
		hit   Hit
		score int
	}
	var found []scored
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxBytes {
			return nil
		}
		score, line, snippet := scanFile(p, words)
		if score == 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		found = append(found, scored{Hit{filepath.ToSlash(rel), line, snippet}, score})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].hit.Path < found[j].hit.Path
	})
	if len(found) > limit {
		found = found[:limit]
	}
	hits := make([]Hit, len(found))
	for i, f := range found {
		hits[i] = f.hit
	}
	return hits, nil
}

// scanFile returns how many distinct words occur in the file and the line
// matching the most words (first on ties).
func scanFile(path string, words []string) (score, bestLine int, snippet string) {
	f, err := os.Open(path) //nolint:gosec // G304: path comes from walking the wiki root
	if err != nil {
		return 0, 0, ""
	}
	defer func() { _ = f.Close() }()
	seen := make([]bool, len(words))
	sc := bufio.NewScanner(io.LimitReader(f, maxSearchFileBytes))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	bestN := 0
	for n := 1; sc.Scan(); n++ {
		text := sc.Text()
		lower := strings.ToLower(text)
		lineN := 0
		for i, w := range words {
			if strings.Contains(lower, w) {
				seen[i] = true
				lineN++
			}
		}
		if lineN > bestN {
			bestN, bestLine = lineN, n
			snippet = truncate(strings.TrimSpace(text))
		}
	}
	for _, s := range seen {
		if s {
			score++
		}
	}
	return score, bestLine, snippet
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) <= maxSnippetRunes {
		return s
	}
	return string(r[:maxSnippetRunes]) + "…"
}

// Read returns a markdown file's contents. rel is relative to root; the
// target (after resolving symlinks) must stay inside root (also resolved).
// The open itself is confined with os.Root, so there is no check-then-open gap.
func Read(root, rel string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if rel == "" || filepath.IsAbs(rel) {
		return "", ErrOutsideRoot
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("wiki root: %w", err)
	}
	rootReal, err = filepath.Abs(rootReal)
	if err != nil {
		return "", fmt.Errorf("wiki root: %w", err)
	}
	// Lexical check first so ../x never reveals whether x exists.
	if !within(rootReal, filepath.Join(rootReal, rel)) {
		return "", ErrOutsideRoot
	}
	r, err := os.OpenRoot(rootReal)
	if err != nil {
		return "", fmt.Errorf("wiki root: %w", err)
	}
	defer func() { _ = r.Close() }()
	// Stat follows symlinks but refuses any that leave the root, before
	// existence is looked at, so nothing leaks about files outside.
	st, err := r.Stat(rel)
	if err != nil {
		return "", mapRootErr(err)
	}
	if !strings.EqualFold(filepath.Ext(rel), ".md") {
		return "", ErrNotMarkdown
	}
	if real, err := filepath.EvalSymlinks(filepath.Join(rootReal, rel)); err == nil && !strings.EqualFold(filepath.Ext(real), ".md") {
		return "", ErrNotMarkdown
	}
	// Checked before opening: opening a FIFO would block forever.
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", rel)
	}
	if st.Size() > maxBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, st.Size(), maxBytes)
	}
	f, err := r.Open(rel)
	if err != nil {
		return "", mapRootErr(err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > maxBytes {
		return "", fmt.Errorf("%w: limit %d", ErrTooLarge, maxBytes)
	}
	return string(b), nil
}

// mapRootErr turns os.Root's escape refusal into ErrOutsideRoot.
func mapRootErr(err error) error {
	if strings.Contains(err.Error(), "path escapes from parent") {
		return ErrOutsideRoot
	}
	return err
}

// within reports whether path is root itself or below it. It compares path
// elements, so /w/wiki-private is not inside /w/wiki.
func within(root, path string) bool {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
