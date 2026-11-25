package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrNewerSchema is a ledger file whose last line has a schema this build does not know. Its writer must not
// append to it: a reader of that schema could not make sense of the mix.
var ErrNewerSchema = errors.New("the ledger file was written by a newer aimux")

// tailScan is how much of the end of a file OpenWriter reads to find the last sequence number.
const tailScan = 1 << 20

// Writer appends numbered lines to one ledger file. One process owns one file. A nil *Writer writes nothing, so
// a service without a ledger still serves.
type Writer struct {
	name string

	mu   sync.Mutex
	file *os.File
	// out is where lines go: the file. A test replaces it to cut a write short, as a full disk does.
	out    io.Writer
	seq    uint64
	failed uint64
	// torn: the file ends in half a line, so the next line starts on a line of its own.
	torn bool
}

// WriterStats is what a writer reports in its service's health answer.
type WriterStats struct {
	Seq    uint64 // the last number handed out
	Failed uint64 // lines that could not be written
}

// OpenWriter appends to dir/name.jsonl, creating dir (0700) and the file (0600). name is also the writer's name
// on every line. Numbering continues from the last complete line on disk.
func OpenWriter(dir, name string) (*Writer, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("writer name %q is not a plain file name", name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("make the ledger folder: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a folder needs its search bit
		return nil, fmt.Errorf("make the ledger folder owner-only: %w", err)
	}
	path := filepath.Join(dir, name+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600) // #nosec G304 -- under the owner's ledger folder
	if err != nil {
		return nil, fmt.Errorf("open the ledger file: %w", err)
	}
	// One process owns one file: the lock goes when the file is closed or the process ends.
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- a file descriptor
		_ = file.Close()
		return nil, fmt.Errorf("%s is open in another process (one writer per ledger file): %w", path, err)
	}
	// The mode is set again: a file made earlier by something else may be readable by others.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("make the ledger file owner-only: %w", err)
	}
	seq, torn, err := lastSeq(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Writer{name: name, file: file, out: file, seq: seq, torn: torn}, nil
}

// lastSeq reads the end of the file: the highest sequence number among its last lines, and whether the file ends
// without a newline. When the last megabyte holds no line it can read, it reads the whole file, so numbering
// never starts again from 1 on a file that has lines.
func lastSeq(file *os.File) (seq uint64, torn bool, err error) {
	info, err := file.Stat()
	if err != nil {
		return 0, false, err
	}
	start := max(info.Size()-tailScan, 0)
	for {
		tail := make([]byte, info.Size()-start)
		if _, err := file.ReadAt(tail, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, false, err
		}
		torn = len(tail) > 0 && tail[len(tail)-1] != '\n'
		lines := bytes.Split(tail, []byte("\n"))
		if start > 0 {
			lines = lines[1:] // the first piece may begin mid-line
		}
		found := false
		for _, raw := range lines {
			// The schema is read on its own: a newer one may have changed the type of any other field.
			var version struct {
				Schema string `json:"schema"`
			}
			if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &version) != nil {
				continue // half a line; a last piece that is whole JSON without its newline still counts
			}
			if version.Schema != Schema {
				return 0, false, ErrNewerSchema
			}
			var numbered struct {
				Seq uint64 `json:"seq"`
			}
			if json.Unmarshal(raw, &numbered) == nil {
				found = true
				seq = max(seq, numbered.Seq)
			}
		}
		if found || start == 0 {
			return seq, torn, nil
		}
		start = 0
	}
}

// Write stamps the writer's name, the next sequence number, the schema and, when it is zero, the time, and
// appends the line. It returns nothing: the action the line describes has already happened, so a write that
// fails is counted in Stats and its number is not used again, which a reader sees as a gap.
func (w *Writer) Write(l Line) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	l.Writer, l.Seq, l.Schema, l.SemConv = w.name, w.seq, Schema, SemConv
	if l.At.IsZero() {
		l.At = time.Now()
	}
	l.At = l.At.UTC()
	data, err := json.Marshal(l)
	if err != nil {
		w.failed++
		return
	}
	// The line and its newline go in one write, so two lines never interleave and a crash leaves at most one
	// half line.
	out := make([]byte, 0, len(data)+2)
	if w.torn {
		out = append(out, '\n')
	}
	out = append(append(out, data...), '\n')
	n, err := w.out.Write(out)
	if err != nil {
		w.failed++
		// Part of the line may be in the file. The next one must not be glued onto it.
		if n > 0 {
			w.torn = out[n-1] != '\n'
		}
		return
	}
	w.torn = false
}

// Stats reports the last number handed out and how many lines were not written.
func (w *Writer) Stats() WriterStats {
	if w == nil {
		return WriterStats{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return WriterStats{Seq: w.seq, Failed: w.failed}
}

// Close closes the file. A Write after Close is counted as failed.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
