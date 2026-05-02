package trace

import (
	"bufio"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Tailer watches a JSONL file and calls onLine for each new complete line
// appended after the tailer starts. Uses fsnotify for immediate detection
// with a 1s poll fallback.
type Tailer struct {
	path    string
	onLine  func(string)
	offset  int64
	watcher *fsnotify.Watcher
	stop    chan struct{}
	done    sync.WaitGroup
}

// NewTailer starts watching from the current end of file.
// Existing content is skipped. Only new appended lines trigger onLine.
func NewTailer(path string, onLine func(string)) (*Tailer, error) {
	// 1. Stat the file to get initial size (this becomes our starting offset)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	offset := info.Size()

	// 2. Create fsnotify watcher on the file
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(path); err != nil {
		watcher.Close()
		return nil, err
	}

	t := &Tailer{
		path:    path,
		onLine:  onLine,
		offset:  offset,
		watcher: watcher,
		stop:    make(chan struct{}),
	}

	// 3. Start background goroutine
	t.done.Add(1)
	go t.run()

	return t, nil
}

// Stop shuts down the tailer and waits for the goroutine to exit.
func (t *Tailer) Stop() {
	close(t.stop)
	t.watcher.Close()
	t.done.Wait()
}

// run is the background goroutine that watches for file changes.
func (t *Tailer) run() {
	defer t.done.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-t.stop:
			return
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				t.readNewLines()
			}
		case <-ticker.C:
			// Fallback poll in case fsnotify misses events
			t.readNewLines()
		case err, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
			// Log error but continue watching
			_ = err
		}
	}
}

// readNewLines reads new bytes from offset, parses lines, and calls onLine.
func (t *Tailer) readNewLines() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()

	// Seek to current offset
	if _, err := f.Seek(t.offset, 0); err != nil {
		return
	}

	// Scan lines with 256KB buffer
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			t.onLine(line)
		}
	}

	// Update offset to current position
	newOffset, err := f.Seek(0, 1) // SEEK_CUR
	if err == nil {
		t.offset = newOffset
	}
}
