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

// ReadTail reads the lines in the last limit bytes of one writer's file, dir/name.jsonl. It is for a writer that
// only needs its recent lines at start and must not take longer as its file grows. A missing file is no lines.
func ReadTail(dir, name string, limit int64) ([]Line, ReadReport, error) {
	var report ReadReport
	file, err := os.Open(filepath.Join(dir, name+".jsonl")) // #nosec G304 -- a file in the owner's ledger folder
	if errors.Is(err, os.ErrNotExist) {
		return nil, report, nil
	}
	if err != nil {
		return nil, report, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, report, err
	}
	report.Files = 1
	start := max(info.Size()-limit, 0)
	return readTailLines(file, start, info.Size(), &report)
}

// readTailLines reads whole lines from offset start, dropping the first when start is inside the file.
func readTailLines(file *os.File, start, size int64, report *ReadReport) ([]Line, ReadReport, error) {
	reader := bufio.NewReader(io.NewSectionReader(file, start, size-start))
	if start > 0 {
		if _, err := reader.ReadBytes('\n'); err != nil {
			return nil, *report, nil
		}
	}
	lines, err := readFile(reader, nil, report)
	return lines, *report, err
}
