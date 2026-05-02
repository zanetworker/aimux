package discovery

import (
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors session directories for new files and triggers a callback.
// Events are debounced to avoid excessive re-discovery on rapid file creation.
type Watcher struct {
	watcher  *fsnotify.Watcher
	onChange func()
	stop     chan struct{}
	done     sync.WaitGroup
}

// NewWatcher creates a watcher on the given directories. Directories that
// don't exist are silently skipped. The onChange callback is debounced to
// fire at most once per 500ms.
func NewWatcher(dirs []string, onChange func()) (*Watcher, error) {
	// Create fsnotify.Watcher
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		watcher:  fw,
		onChange: onChange,
		stop:     make(chan struct{}),
	}

	// Add each dir that exists (skip missing ones silently)
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err == nil {
			if err := fw.Add(dir); err != nil {
				// Silently skip directories we can't watch
				continue
			}
		}
	}

	// Start background goroutine
	w.done.Add(1)
	go w.run()

	return w, nil
}

// run is the background event loop.
func (w *Watcher) run() {
	defer w.done.Done()

	var debounceTimer *time.Timer
	var debounceTimerMu sync.Mutex

	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Only care about Create and Write events
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			// Reset the debounce timer
			debounceTimerMu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				w.onChange()
			})
			debounceTimerMu.Unlock()

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// Silently ignore errors - we don't want watcher failures
			// to bring down the entire app
			_ = err

		case <-w.stop:
			debounceTimerMu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimerMu.Unlock()
			return
		}
	}
}

// Stop shuts down the watcher.
func (w *Watcher) Stop() {
	close(w.stop)
	w.watcher.Close()
	w.done.Wait()
}
