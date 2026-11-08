package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The media fixtures are consumed by both clients. This keeps them well
// formed: every card has a known kind, an http(s) address and a title, and
// together the files pin every kind.

type mediaBlock struct {
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	Href   string `json:"href"`
	Title  string `json:"title"`
	Blocks []mediaBlock
	Items  []struct {
		Blocks []mediaBlock `json:"blocks"`
	} `json:"items"`
}

func walkMedia(in []mediaBlock, f func(mediaBlock)) {
	for _, b := range in {
		if b.Type == "media" {
			f(b)
		}
		walkMedia(b.Blocks, f)
		for _, it := range b.Items {
			walkMedia(it.Blocks, f)
		}
	}
}

func TestMediaFixturesAreWellFormed(t *testing.T) {
	files, err := filepath.Glob("testdata/clients/media/*.json")
	if err != nil || len(files) < 2 {
		t.Fatalf("media fixtures: %v, %d files", err, len(files))
	}
	kinds := []string{"youtube", "vimeo", "video", "image", "audio"}
	seen := map[string]bool{}
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // a glob of the fixtures directory
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Name  string `json:"name"`
			Cases []struct {
				Name     string       `json:"name"`
				Markdown string       `json:"markdown"`
				Blocks   []mediaBlock `json:"blocks"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil || doc.Name == "" || len(doc.Cases) == 0 {
			t.Fatalf("%s: %v, name %q, %d cases", file, err, doc.Name, len(doc.Cases))
		}
		for _, c := range doc.Cases {
			where := filepath.Base(file) + ": " + c.Name
			if c.Name == "" || c.Markdown == "" || len(c.Blocks) == 0 {
				t.Errorf("%s: missing name, markdown or blocks", where)
			}
			walkMedia(c.Blocks, func(b mediaBlock) {
				lower := strings.ToLower(b.Href)
				if !slices.Contains(kinds, b.Kind) || b.Title == "" ||
					(!strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")) {
					t.Errorf("%s: bad media block %+v", where, b)
				}
				seen[b.Kind] = true
			})
		}
	}
	for _, k := range kinds {
		if !seen[k] {
			t.Errorf("no fixture pins the %s kind", k)
		}
	}
}
