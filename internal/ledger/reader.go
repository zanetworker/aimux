package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// ReadReport says what ReadDir could not use. A total over a folder with skipped or torn lines is incomplete.
type ReadReport struct {
	Files   int
	Skipped int // lines of a schema this build does not know
	Torn    int // lines that are not complete JSON
}

// ReadDir reads every *.jsonl file in dir, in file-name order. It never writes. A missing dir is no lines and no
// error: nothing has been recorded yet.
func ReadDir(dir string) ([]Line, ReadReport, error) {
	var report ReadReport
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, report, nil
	}
	if err != nil {
		return nil, report, err
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".jsonl" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	slices.Sort(paths)
	var lines []Line
	for _, path := range paths {
		file, err := os.Open(path) // #nosec G304 -- a file in the owner's ledger folder
		if errors.Is(err, os.ErrNotExist) {
			continue // rotated away between the listing and the read
		}
		if err != nil {
			return nil, report, err
		}
		report.Files++
		lines, err = readFile(file, lines, &report)
		_ = file.Close()
		if err != nil {
			return nil, report, err
		}
	}
	return lines, report, nil
}

func readFile(file io.Reader, lines []Line, report *ReadReport) ([]Line, error) {
	reader := bufio.NewReader(file)
	for {
		raw, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var head struct {
				Schema string `json:"schema"`
			}
			var line Line
			switch {
			case json.Unmarshal(raw, &head) != nil:
				report.Torn++
			case head.Schema != Schema:
				report.Skipped++
			case json.Unmarshal(raw, &line) != nil:
				report.Torn++
			default:
				lines = append(lines, line)
			}
		}
		if errors.Is(err, io.EOF) {
			return lines, nil
		}
		if err != nil {
			return lines, err
		}
	}
}
