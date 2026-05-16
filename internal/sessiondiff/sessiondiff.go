package sessiondiff

import (
	"path/filepath"
	"strings"

	"github.com/zanetworker/aimux/internal/trace"
)

type FileDiff struct {
	Path      string     `json:"path"`
	ShortPath string     `json:"shortPath"`
	Status    string     `json:"status"` // "modified", "added"
	Added     int        `json:"added"`
	Removed   int        `json:"removed"`
	Hunks     []DiffHunk `json:"hunks"`
}

type DiffHunk struct {
	Lines []DiffLine `json:"lines"`
}

type DiffLine struct {
	Type   string `json:"type"` // "add", "del", "ctx", "collapse"
	Text   string `json:"text"`
	OldNum int    `json:"oldNum,omitempty"`
	NewNum int    `json:"newNum,omitempty"`
	Count  int    `json:"count,omitempty"` // for collapse: number of hidden lines
}

func Extract(turns []trace.Turn) []FileDiff {
	type fileEdit struct {
		path      string
		oldString string
		newString string
		content   string
		isWrite   bool
	}

	var edits []fileEdit
	for _, t := range turns {
		for _, a := range t.Actions {
			if a.Name == "Edit" && (a.OldString != "" || a.NewString != "") {
				path := a.FilePath
				if path == "" {
					path = a.Snippet
				}
				edits = append(edits, fileEdit{path: path, oldString: a.OldString, newString: a.NewString})
			}
			if a.Name == "Write" && a.Content != "" {
				path := a.FilePath
				if path == "" {
					path = a.Snippet
				}
				edits = append(edits, fileEdit{path: path, content: a.Content, isWrite: true})
			}
		}
	}

	byFile := make(map[string]*FileDiff)
	var order []string

	for _, e := range edits {
		fd, ok := byFile[e.path]
		if !ok {
			fd = &FileDiff{
				Path:      e.path,
				ShortPath: filepath.Base(e.path),
			}
			byFile[e.path] = fd
			order = append(order, e.path)
		}

		if e.isWrite {
			fd.Status = "added"
			lines := strings.Split(e.content, "\n")
			hunk := DiffHunk{}
			for i, l := range lines {
				hunk.Lines = append(hunk.Lines, DiffLine{Type: "add", Text: l, NewNum: i + 1})
				fd.Added++
			}
			fd.Hunks = append(fd.Hunks, hunk)
		} else {
			if fd.Status == "" {
				fd.Status = "modified"
			}
			oldLines := strings.Split(e.oldString, "\n")
			newLines := strings.Split(e.newString, "\n")
			hunk := DiffHunk{}
			for _, l := range oldLines {
				hunk.Lines = append(hunk.Lines, DiffLine{Type: "del", Text: l})
				fd.Removed++
			}
			for _, l := range newLines {
				hunk.Lines = append(hunk.Lines, DiffLine{Type: "add", Text: l})
				fd.Added++
			}
			fd.Hunks = append(fd.Hunks, hunk)
		}
	}

	result := make([]FileDiff, 0, len(order))
	for _, path := range order {
		result = append(result, *byFile[path])
	}
	return result
}
