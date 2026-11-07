package history

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/zanetworker/aimux/internal/jsonl"
)

// ShownTitleForFile is ShownTitle for one session file, for surfaces that
// hold a live agent's file path rather than a Session. The .meta.json title
// is read fresh on every call; the transcript is read only past the bytes
// already seen, so a growing live session costs only its new lines.
func ShownTitleForFile(path string) string {
	if path == "" {
		return ""
	}
	return ShownTitleForFileMeta(path, LoadMeta(path))
}

// ShownTitleForFileMeta is ShownTitleForFile with the session's .meta.json
// already read by the caller, so it is read once.
func ShownTitleForFileMeta(path string, meta Meta) string {
	if path == "" {
		return ""
	}
	s := transcriptTitles(path)
	s.Title, s.TitleBy = meta.Title, meta.TitleBy
	return ShownTitle(s)
}

// WarmTitles reads the new lines of these transcripts now, off the render
// path (the TUI's agent refresh), so ShownTitleForFile in a view only stats
// the file and reads its .meta.json.
func WarmTitles(paths []string) {
	for _, p := range paths {
		if p != "" {
			transcriptTitles(p)
		}
	}
}

// titleFilesMax bounds the parse cache; the oldest file is dropped first.
const titleFilesMax = 512

// titleFile is what has been read of one transcript: the complete lines up
// to offset, parsed as scanSession parses them.
type titleFile struct {
	offset int64
	s      Session
}

var titleFiles = struct {
	sync.Mutex
	m     map[string]*titleFile
	order []string // insertion order, oldest first
}{m: map[string]*titleFile{}}

// transcriptTitles returns what a transcript says about its title (latest
// ai-title and /rename, first prompt), reading only what was appended since
// the last call. A file that shrank was rewritten and is read again from the
// start; a file rewritten at an equal or larger size keeps the old parse of
// its first bytes (Claude Code only appends to transcripts).
func transcriptTitles(path string) Session {
	titleFiles.Lock()
	defer titleFiles.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return Session{} // a gone file's entry ages out under the cap
	}
	tf := titleFiles.m[path]
	if tf == nil || info.Size() < tf.offset {
		if tf == nil {
			titleFiles.order = append(titleFiles.order, path)
		}
		tf = &titleFile{}
		titleFiles.m[path] = tf
		for len(titleFiles.m) > titleFilesMax && len(titleFiles.order) > 0 {
			oldest := titleFiles.order[0]
			titleFiles.order = titleFiles.order[1:]
			if oldest != path {
				delete(titleFiles.m, oldest)
			}
		}
	}
	if info.Size() > tf.offset {
		tf.readFrom(path)
	}
	return Session{AITitle: tf.s.AITitle, CustomTitle: tf.s.CustomTitle, FirstPrompt: tf.s.FirstPrompt}
}

// readFrom parses the complete lines after offset, one at a time; a last
// line still being written is left for the next call.
func (tf *titleFile) readFrom(path string) {
	f, err := os.Open(path) // #nosec G304 -- a session file path from discovery
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(tf.offset, io.SeekStart); err != nil {
		return
	}
	sc := jsonl.NewScanner(f)
	sc.Split(completeLines)
	for sc.Scan() {
		tf.offset += int64(len(sc.Bytes())) + 1
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		parseSessionLine(json.RawMessage(line), &tf.s, tf.s.FirstPrompt == "")
	}
}

// completeLines splits on newlines and, unlike bufio.ScanLines, never
// returns a last line without one: it may still be being written.
func completeLines(data []byte, _ bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	return 0, nil, nil
}

// FirstPromptReader finds a transcript's first prompt exactly as Discover
// does, fed one line at a time by a reader that walks the file anyway (the
// search index), so its titles use history's first-message form.
type FirstPromptReader struct{ s Session }

// Add parses one transcript line until the first prompt is found.
func (r *FirstPromptReader) Add(line []byte) {
	if r.s.FirstPrompt == "" {
		parseSessionLine(json.RawMessage(bytes.TrimSpace(line)), &r.s, true)
	}
}

// FirstPrompt is the first prompt found so far.
func (r *FirstPromptReader) FirstPrompt() string { return r.s.FirstPrompt }
