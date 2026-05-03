# aimux v2.0 Observability Release -- Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make aimux the fastest, most responsive AI agent dashboard with live trace streaming, cross-session search, cost-per-turn, diff summaries, and smart notifications.

**Architecture:** Core packages (cache, diff, trace/tailer, discovery/watcher) stay UI-agnostic -- no bubbletea/lipgloss imports. TUI wiring happens in `tui/app.go` and `tui/views/`. Performance fixes target `terminal/view.go` (VT double-render) and `terminal/tmux.go` (poll interval). One new dependency: `fsnotify/fsnotify`.

**Tech Stack:** Go 1.24+, Bubble Tea (TUI), charmbracelet/x/vt (VT emulator), fsnotify (file watchers), creack/pty (PTY), ripgrep (search)

**Spec:** `docs/superpowers/specs/2026-05-02-observability-release-design.md`

---

## Task 1: Startup Cache (core package)

**Files:**
- Create: `internal/cache/cache.go`
- Create: `internal/cache/cache_test.go`

- [ ] **Step 1: Write the failing test for Save**

```go
// internal/cache/cache_test.go
package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-seen.json")

	agents := []agent.Agent{
		{
			PID:          12345,
			Name:         "my-api",
			ProviderName: "claude",
			WorkingDir:   "/tmp/my-api",
			Model:        "claude-opus-4-6",
			EstCostUSD:   0.42,
		},
		{
			PID:          67890,
			Name:         "tests",
			ProviderName: "codex",
			WorkingDir:   "/tmp/tests",
			Model:        "o4-mini",
			EstCostUSD:   0.18,
		},
	}

	err := Save(path, agents)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(loaded))
	}
	if loaded[0].Name != "my-api" {
		t.Errorf("expected name my-api, got %s", loaded[0].Name)
	}
	if loaded[1].ProviderName != "codex" {
		t.Errorf("expected provider codex, got %s", loaded[1].ProviderName)
	}
}

func TestLoadMissingFile(t *testing.T) {
	agents, err := Load("/nonexistent/path/cache.json")
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected empty slice, got %d agents", len(agents))
	}
}

func TestLoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-seen.json")
	os.WriteFile(path, []byte("not json{{{"), 0644)

	agents, err := Load(path)
	if err != nil {
		t.Fatalf("Load should not error on corrupt file: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected empty slice on corrupt, got %d", len(agents))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/cache/ -v -run TestSave`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Write minimal implementation**

```go
// internal/cache/cache.go
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

type entry struct {
	PID          int       `json:"pid"`
	Name         string    `json:"name"`
	ProviderName string    `json:"provider"`
	WorkingDir   string    `json:"cwd"`
	Model        string    `json:"model"`
	Status       string    `json:"status"`
	EstCostUSD   float64   `json:"cost"`
	GitBranch    string    `json:"branch,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
}

func toEntry(a agent.Agent) entry {
	return entry{
		PID:          a.PID,
		Name:         a.Name,
		ProviderName: a.ProviderName,
		WorkingDir:   a.WorkingDir,
		Model:        a.Model,
		Status:       a.Status.String(),
		EstCostUSD:   a.EstCostUSD,
		GitBranch:    a.GitBranch,
		LastSeen:     time.Now(),
	}
}

func toAgent(e entry) agent.Agent {
	return agent.Agent{
		PID:          e.PID,
		Name:         e.Name,
		ProviderName: e.ProviderName,
		WorkingDir:   e.WorkingDir,
		Model:        e.Model,
		EstCostUSD:   e.EstCostUSD,
		GitBranch:    e.GitBranch,
		LastActivity: e.LastSeen,
	}
}

// DefaultPath returns ~/.aimux/cache/last-seen.json.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aimux", "cache", "last-seen.json")
}

// Save writes the agent list to disk as JSON.
func Save(path string, agents []agent.Agent) error {
	entries := make([]entry, len(agents))
	for i, a := range agents {
		entries[i] = toEntry(a)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Load reads cached agents from disk. Returns empty slice on missing or corrupt file.
func Load(path string) ([]agent.Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, nil
	}
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil
	}
	agents := make([]agent.Agent, len(entries))
	for i, e := range entries {
		agents[i] = toAgent(e)
	}
	return agents, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/cache/ -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/cache/
git commit -m "feat(cache): add startup cache for instant first paint"
```

---

## Task 2: Wire Startup Cache into TUI

**Files:**
- Modify: `internal/tui/app.go` (NewApp, Init, Update)

- [ ] **Step 1: Add cache import and load in NewApp**

In `internal/tui/app.go`, add to imports:

```go
"github.com/zanetworker/aimux/internal/cache"
```

In `NewApp()`, after the existing setup, add cache loading before returning:

```go
// Load cached agents for instant first paint
cachedAgents, _ := cache.Load(cache.DefaultPath())
```

Set `instances` field on the App struct:

```go
app.instances = cachedAgents
```

Add a `staleAgents` field to the App struct to track which PIDs came from cache:

```go
staleAgents map[int]bool // PIDs loaded from cache (dim until confirmed by discovery)
```

Initialize it in NewApp:

```go
staleAgents := make(map[int]bool)
for _, a := range cachedAgents {
    staleAgents[a.PID] = true
}
```

- [ ] **Step 2: Save cache on each discovery refresh**

In `Update()`, in the `instancesMsg` handler (where `a.instances = msg`), add:

```go
// Persist for next startup
go cache.Save(cache.DefaultPath(), a.instances)

// Clear stale markers: all discovered agents are confirmed alive
a.staleAgents = make(map[int]bool)
```

- [ ] **Step 3: Pass stale flag to agents view for dim rendering**

In `internal/tui/views/agents.go`, add a method to accept stale PIDs and dim those rows. The exact rendering change: stale agent rows use `lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))` (dim grey) instead of the normal row style.

- [ ] **Step 4: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all packages PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/views/agents.go
git commit -m "feat(tui): render cached agents on startup for instant first paint"
```

---

## Task 3: VT Emulator Double-Render Fix

**Files:**
- Modify: `internal/terminal/view.go`
- Modify: `internal/terminal/view_test.go` (if exists, otherwise create)

This is the highest-risk change. The current `Write()` method calls `tv.term.Render()` twice per write to detect content changes for scroll history. This is the main cause of split-pane sluggishness.

- [ ] **Step 1: Write test for dirty-tracking Write**

```go
// internal/terminal/view_test.go
package terminal

import (
	"testing"
)

func TestWriteDoesNotCallRender(t *testing.T) {
	tv := NewTermView(80, 24)

	// Write some data
	tv.Write([]byte("hello world\n"))

	// The dirty flag should be set
	tv.mu.Lock()
	dirty := tv.dirty
	tv.mu.Unlock()

	if !dirty {
		t.Error("expected dirty=true after Write")
	}
}

func TestSnapshotHistory(t *testing.T) {
	tv := NewTermView(80, 24)

	tv.Write([]byte("line one\n"))
	tv.SnapshotHistory()

	tv.mu.Lock()
	histLen := len(tv.history)
	tv.mu.Unlock()

	if histLen == 0 {
		t.Error("expected history to have entries after SnapshotHistory")
	}
}

func TestRenderCaching(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("hello\n"))

	r1 := tv.Render()
	r2 := tv.Render()

	if r1 != r2 {
		t.Error("consecutive Render calls with no Write should return same string")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/terminal/ -v -run TestWriteDoesNotCallRender`
Expected: FAIL (dirty field does not exist)

- [ ] **Step 3: Implement dirty-tracking Write**

Replace the `Write` method in `internal/terminal/view.go`:

```go
// Add fields to TermView struct:
dirty       bool      // set on Write, cleared on Render
cachedView  string    // last Render() output
cacheScroll int       // scrollBack value when cachedView was built
bytesWritten int      // bytes written since last history snapshot

// New Write method - no Render() calls:
func (tv *TermView) Write(data []byte) {
	tv.term.Write(data)

	tv.mu.Lock()
	tv.dirty = true
	tv.bytesWritten += len(data)
	tv.mu.Unlock()
}

// New SnapshotHistory captures current screen into scrollback.
// Called on a debounce timer (100ms) from the TUI layer, not on every Write.
func (tv *TermView) SnapshotHistory() {
	tv.mu.Lock()
	defer tv.mu.Unlock()

	if tv.bytesWritten == 0 {
		return
	}

	rendered := tv.term.Render()
	for _, line := range strings.Split(rendered, "\n") {
		tv.history = append(tv.history, line)
	}
	if len(tv.history) > 10000 {
		tv.history = tv.history[len(tv.history)-10000:]
	}
	tv.bytesWritten = 0
}

// Updated Render with caching:
func (tv *TermView) Render() string {
	tv.mu.Lock()
	scrollBack := tv.scrollBack
	dirty := tv.dirty
	tv.mu.Unlock()

	if !dirty && scrollBack == tv.cacheScroll && tv.cachedView != "" {
		return tv.cachedView
	}

	var result string
	if scrollBack == 0 {
		result = tv.term.Render()
	} else {
		tv.mu.Lock()
		histLen := len(tv.history)
		height := tv.height
		end := histLen
		start := end - scrollBack
		if start < 0 {
			start = 0
		}
		var b strings.Builder
		b.Grow(height * (tv.width + 1))
		for i := start; i < end && i-start < height; i++ {
			if i > start {
				b.WriteByte('\n')
			}
			b.WriteString(tv.history[i])
		}
		result = b.String()
		tv.mu.Unlock()
	}

	tv.mu.Lock()
	tv.dirty = false
	tv.cachedView = result
	tv.cacheScroll = scrollBack
	tv.mu.Unlock()

	return result
}
```

- [ ] **Step 4: Add debounce timer in session view**

In `internal/tui/app.go` (or the session view), add a 100ms ticker that calls `SnapshotHistory()` on the active TermView. This replaces the per-write history capture:

```go
// In the split-view readPTY loop or as a separate tea.Cmd:
func (a App) historySnapshotTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return historySnapshotMsg{}
	})
}
```

Handle `historySnapshotMsg` in Update by calling `tv.SnapshotHistory()` on the active TermView.

- [ ] **Step 5: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/terminal/ -v`
Expected: PASS

- [ ] **Step 6: Run full test suite for regressions**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/terminal/view.go internal/terminal/view_test.go internal/tui/app.go
git commit -m "perf(terminal): remove double-render from Write, add dirty tracking and render caching"
```

---

## Task 4: Tmux Adaptive Poll Interval

**Files:**
- Modify: `internal/terminal/tmux.go`

- [ ] **Step 1: Write test for adaptive interval**

```go
// Add to internal/terminal/tmux_test.go (or create it)
package terminal

import "testing"

func TestPollIntervalAdaptive(t *testing.T) {
	fast := pollInterval(true)
	slow := pollInterval(false)

	if fast >= slow {
		t.Errorf("active interval (%v) should be less than idle (%v)", fast, slow)
	}
	if fast.Milliseconds() > 150 {
		t.Errorf("active interval too slow: %v", fast)
	}
	if slow.Milliseconds() < 400 {
		t.Errorf("idle interval too fast: %v", slow)
	}
}
```

- [ ] **Step 2: Implement adaptive polling**

In `internal/terminal/tmux.go`, add:

```go
import "hash/fnv"

func pollInterval(active bool) time.Duration {
	if active {
		return 100 * time.Millisecond
	}
	return 500 * time.Millisecond
}
```

Update the `poll` method:

```go
func (ts *TmuxSession) poll(ctx context.Context) {
	interval := 100 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var prevHash uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			out, err := exec.Command("tmux", "capture-pane", "-e", "-p", "-t", ts.sessionName).Output()
			if err != nil {
				continue
			}

			// Hash-based change detection instead of full string compare
			h := fnv.New64a()
			h.Write(out)
			hash := h.Sum64()

			ts.mu.Lock()
			if ts.closed {
				ts.mu.Unlock()
				return
			}

			if hash != prevHash {
				prevHash = hash
				ts.prev = out
				ts.rendered = string(out)
				ts.pending = append(ts.pending, '.')
				select {
				case ts.signal <- struct{}{}:
				default:
				}
				// Content changed = active, use fast interval
				ticker.Reset(pollInterval(true))
			} else {
				// No change = idle, use slow interval
				ticker.Reset(pollInterval(false))
			}
			ts.mu.Unlock()
		}
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/terminal/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/terminal/tmux.go internal/terminal/tmux_test.go
git commit -m "perf(terminal): adaptive tmux poll interval with hash-based change detection"
```

---

## Task 5: JSONL File Tailer (core package)

**Files:**
- Create: `internal/trace/tailer.go`
- Create: `internal/trace/tailer_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/trace/tailer_test.go
package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTailerDetectsNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	// Create file with initial content
	os.WriteFile(path, []byte(`{"type":"initial"}`+"\n"), 0644)

	lines := make(chan string, 10)
	tailer, err := NewTailer(path, func(line string) {
		lines <- line
	})
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer tailer.Stop()

	// Append a new line
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"type":"appended"}` + "\n")
	f.Close()

	select {
	case line := <-lines:
		if line != `{"type":"appended"}` {
			t.Errorf("unexpected line: %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for appended line")
	}
}

func TestTailerSkipsExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	os.WriteFile(path, []byte(`{"old":"data"}`+"\n"), 0644)

	lines := make(chan string, 10)
	tailer, err := NewTailer(path, func(line string) {
		lines <- line
	})
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer tailer.Stop()

	// No lines should arrive for existing content
	select {
	case line := <-lines:
		t.Errorf("should not receive existing content, got: %s", line)
	case <-time.After(500 * time.Millisecond):
		// expected: no lines
	}
}

func TestTailerStopClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	os.WriteFile(path, []byte(""), 0644)

	tailer, err := NewTailer(path, func(line string) {})
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}

	tailer.Stop()
	// Should not panic or hang
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/trace/ -v -run TestTailer`
Expected: FAIL (NewTailer not defined)

- [ ] **Step 3: Write implementation**

```go
// internal/trace/tailer.go
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
// appended after the tailer starts. It uses fsnotify for immediate detection
// with a 1s poll fallback.
type Tailer struct {
	path    string
	onLine  func(string)
	offset  int64
	watcher *fsnotify.Watcher
	stop    chan struct{}
	done    sync.WaitGroup
}

// NewTailer creates a tailer that starts watching from the current end of file.
func NewTailer(path string, onLine func(string)) (*Tailer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	t := &Tailer{
		path:   path,
		onLine: onLine,
		offset: info.Size(),
		watcher: watcher,
		stop:   make(chan struct{}),
	}

	if err := watcher.Add(path); err != nil {
		watcher.Close()
		return nil, err
	}

	t.done.Add(1)
	go t.run()

	return t, nil
}

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
			if event.Has(fsnotify.Write) {
				t.readNewLines()
			}
		case <-t.watcher.Errors:
			// ignore watcher errors, poll fallback handles it
		case <-ticker.C:
			t.readNewLines()
		}
	}
}

func (t *Tailer) readNewLines() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= t.offset {
		return
	}

	if _, err := f.Seek(t.offset, 0); err != nil {
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			t.onLine(line)
		}
	}

	newOffset, _ := f.Seek(0, 1)
	t.offset = newOffset
}

// Stop shuts down the tailer and waits for the goroutine to exit.
func (t *Tailer) Stop() {
	close(t.stop)
	t.watcher.Close()
	t.done.Wait()
}
```

- [ ] **Step 4: Add fsnotify dependency**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go get github.com/fsnotify/fsnotify`

- [ ] **Step 5: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/trace/ -v -run TestTailer`
Expected: PASS (all 3 tests)

- [ ] **Step 6: Commit**

```bash
git add internal/trace/tailer.go internal/trace/tailer_test.go go.mod go.sum
git commit -m "feat(trace): add JSONL file tailer for live trace streaming"
```

---

## Task 6: Git Diff Summary (core package)

**Files:**
- Create: `internal/diff/summary.go`
- Create: `internal/diff/summary_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/diff/summary_test.go
package diff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseDiffStat(t *testing.T) {
	output := ` internal/tui/app.go      | 45 ++++++++++++-----
 internal/tui/views/p.go  | 38 +++++++++++---
 go.mod                   |  8 ----
 3 files changed, 72 insertions(+), 11 deletions(-)`

	files, total := ParseDiffStat(output)

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
	if files[0].Path != "internal/tui/app.go" {
		t.Errorf("expected first file internal/tui/app.go, got %s", files[0].Path)
	}
	if total.Insertions != 72 {
		t.Errorf("expected 72 insertions, got %d", total.Insertions)
	}
	if total.Deletions != 11 {
		t.Errorf("expected 11 deletions, got %d", total.Deletions)
	}
}

func TestGetDiffStatInGitRepo(t *testing.T) {
	// Create a temp git repo with a change
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Run()
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")

	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	// Make a change
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	stat, err := GetDiffStat(dir)
	if err != nil {
		t.Fatalf("GetDiffStat: %v", err)
	}
	if stat == "" {
		t.Fatal("expected non-empty diff stat")
	}
}

func TestGetDiffStatNoChanges(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Run()
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("package f\n"), 0644)
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	stat, err := GetDiffStat(dir)
	if err != nil {
		t.Fatalf("GetDiffStat: %v", err)
	}
	if stat != "" {
		t.Errorf("expected empty stat, got: %s", stat)
	}
}

func TestGetFullDiff(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Run()
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)
	run("git", "add", ".")
	run("git", "commit", "-m", "init")
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	diff, err := GetFullDiff(dir)
	if err != nil {
		t.Fatalf("GetFullDiff: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/diff/ -v`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Write implementation**

```go
// internal/diff/summary.go
package diff

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// FileStat represents one file's change summary.
type FileStat struct {
	Path       string
	Status     string // "M", "A", "D"
	Insertions int
	Deletions  int
}

// TotalStat is the aggregate across all files.
type TotalStat struct {
	FileCount  int
	Insertions int
	Deletions  int
}

// GetDiffStat runs git diff --stat in the given directory. Returns empty string if no changes.
func GetDiffStat(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "--stat", "--no-color")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff --stat: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GetFullDiff runs git diff --no-color in the given directory.
func GetFullDiff(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-color")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

var statLineRe = regexp.MustCompile(`^\s*(.+?)\s*\|\s*(\d+)`)
var totalLineRe = regexp.MustCompile(`(\d+)\s+files?\s+changed(?:,\s*(\d+)\s+insertions?)?(?:,\s*(\d+)\s+deletions?)?`)

// ParseDiffStat parses git diff --stat output into structured data.
func ParseDiffStat(output string) ([]FileStat, TotalStat) {
	var files []FileStat
	var total TotalStat

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)

		if m := totalLineRe.FindStringSubmatch(line); m != nil {
			total.FileCount, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				total.Insertions, _ = strconv.Atoi(m[2])
			}
			if m[3] != "" {
				total.Deletions, _ = strconv.Atoi(m[3])
			}
			continue
		}

		if m := statLineRe.FindStringSubmatch(line); m != nil {
			path := strings.TrimSpace(m[0:][0])
			path = m[1]
			files = append(files, FileStat{
				Path: strings.TrimSpace(path),
			})
		}
	}

	return files, total
}

// FormatCompact returns a compact multi-line summary for the preview pane.
func FormatCompact(stat string) string {
	if stat == "" {
		return ""
	}
	files, total := ParseDiffStat(stat)
	if total.FileCount == 0 && len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Files changed: %d (+%d / -%d)\n",
		total.FileCount, total.Insertions, total.Deletions))
	for _, f := range files {
		b.WriteString(fmt.Sprintf("  M %s\n", f.Path))
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/diff/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/diff/
git commit -m "feat(diff): add git diff summary and full diff for agent preview"
```

---

## Task 7: Wire Diff Summary into Preview Pane

**Files:**
- Modify: `internal/tui/views/preview.go`
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Add diff fields to PreviewPane**

In `internal/tui/views/preview.go`, add fields:

```go
diffStat     string // cached git diff --stat output
diffFull     string // cached git diff output (for expanded view)
diffExpanded bool   // true when showing full diff instead of trace
diffScroll   int    // scroll position within full diff view
```

- [ ] **Step 2: Fetch diff stat on agent selection**

In `SetAgent`, after setting the agent, fire a goroutine to get the diff stat:

```go
go func(cwd string) {
    stat, _ := diff.GetDiffStat(cwd)
    // Store result - will be picked up on next render
    p.mu.Lock()
    p.diffStat = stat
    p.mu.Unlock()
}(a.WorkingDir)
```

Add a `sync.Mutex` to PreviewPane for thread-safe diff access.

- [ ] **Step 3: Render diff stat in View()**

In the `View()` method of PreviewPane, insert the compact diff summary between the header info block and the trace. Use `diff.FormatCompact(p.diffStat)`. Only show if non-empty.

- [ ] **Step 4: Handle `d` key for expanded diff**

In `internal/tui/app.go`, in the agents view key handler, add:

```go
case "d":
    if a.previewPane != nil {
        a.previewPane.ToggleDiff()
    }
```

Add `ToggleDiff()` to PreviewPane that toggles `diffExpanded`, fetches full diff if not cached, and switches rendering between trace and full diff.

When expanded, render the full diff with `j/k` scrolling. `d` or `Esc` returns to trace view.

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tui/views/preview.go internal/tui/app.go
git commit -m "feat(preview): show git diff summary with expandable full diff view"
```

---

## Task 8: Attention Counter and Terminal Bell

**Files:**
- Modify: `internal/tui/views/header.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add attention counter to header**

In `internal/tui/views/header.go`, add an `attentionCount` field to `HeaderView`:

```go
attentionCount int
```

Add setter:

```go
func (h *HeaderView) SetAttentionCount(n int) {
    h.attentionCount = n
}
```

In `renderInfoBoxes()`, after the provider box, add an attention box when count > 0:

```go
if h.attentionCount > 0 {
    attentionStyle := lipgloss.NewStyle().
        Foreground(lipgloss.Color("#F59E0B")).Bold(true)
    attentionBox := boxStyle.Render(
        labelStyle.Render("Attention") + "\n" +
            attentionStyle.Render(fmt.Sprintf("⚠ %d need action", h.attentionCount)),
    )
    boxes = lipgloss.JoinHorizontal(lipgloss.Top, boxes, " ", attentionBox)
}
```

- [ ] **Step 2: Calculate attention count in app.go**

In the `instancesMsg` handler in `Update()`, calculate:

```go
attention := 0
for _, inst := range a.instances {
    if inst.Status == agent.StatusWaitingPermission {
        attention++
    }
    // Done agents count for 60 seconds (track via doneTimestamps map)
}
a.headerView.SetAttentionCount(attention)
```

Add a `doneTimestamps map[int]time.Time` field to App to track when agents transitioned to done.

- [ ] **Step 3: Add terminal bell**

In the `instancesMsg` handler, after detecting status transitions (using existing `prevStatuses`), send terminal bell:

```go
for _, inst := range a.instances {
    prev, existed := a.prevStatuses[inst.PID]
    if !existed {
        continue
    }
    shouldBell := false
    switch {
    case prev == agent.StatusActive && inst.Status == agent.StatusWaitingPermission:
        shouldBell = true
    case prev == agent.StatusActive && inst.Status == agent.StatusIdle:
        shouldBell = true
    case inst.Status == agent.StatusError && prev != agent.StatusError:
        shouldBell = true
    }
    if shouldBell && !a.silenced && a.cfg.Notifications.Bell {
        fmt.Print("\a")
    }
}
```

- [ ] **Step 4: Add Bell config field**

In `internal/config/config.go`, add to `NotificationsConfig`:

```go
Bell bool `yaml:"bell"` // terminal bell on attention events (default: true)
```

In `Default()`, set `Bell: true`.

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tui/views/header.go internal/tui/app.go internal/config/config.go
git commit -m "feat(notifications): add attention counter in header and terminal bell"
```

---

## Task 9: Wire Live Trace Streaming into Split View

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/views/logs.go`

- [ ] **Step 1: Start tailer when entering split view**

In `app.go`, when entering split mode (the code that creates `splitTrace`), start a tailer on the agent's session file:

```go
type newTraceLineMsg struct {
    line string
}

// After creating splitTrace, if agent has a SessionFile:
if agent.SessionFile != "" {
    tailer, err := trace.NewTailer(agent.SessionFile, func(line string) {
        // Send as tea.Msg via program.Send
        a.program.Send(newTraceLineMsg{line: line})
    })
    if err == nil {
        a.activeTailer = tailer
    }
}
```

Add `activeTailer *trace.Tailer` and `program *tea.Program` fields to App.

- [ ] **Step 2: Handle newTraceLineMsg in Update**

```go
case newTraceLineMsg:
    if a.splitTrace != nil {
        // Re-parse the full trace (simple approach) or append
        // For now, re-parse is safest since turns span multiple lines
        a.refreshSplitTrace()
    }
```

`refreshSplitTrace()` re-reads and re-parses the session file, updating `splitTrace`. The tailer just triggers the refresh.

- [ ] **Step 3: Auto-scroll trace on new content**

In `internal/tui/views/logs.go`, add a method:

```go
func (l *LogsView) SnapToBottom() {
    l.scrollOffset = max(0, len(l.turns) - l.visibleTurns())
}
```

Call `SnapToBottom()` on the splitTrace after refresh, unless the user has scrolled up (track with a `userScrolled bool`).

- [ ] **Step 4: Stop tailer when leaving split view**

In the code that exits split mode:

```go
if a.activeTailer != nil {
    a.activeTailer.Stop()
    a.activeTailer = nil
}
```

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/views/logs.go
git commit -m "feat(trace): live trace streaming in split view via JSONL tailer"
```

---

## Task 10: Cross-Session Trace Search

**Files:**
- Modify: `internal/tui/views/agents.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/history/search.go`

- [ ] **Step 1: Add SearchAgentContent to history package**

In `internal/history/search.go`, add a function that searches a specific JSONL file:

```go
// SearchFile counts matches of query in a single JSONL file.
// Returns the count and a snippet from the first match.
func SearchFile(filePath, query string) (count int, snippet string) {
	if filePath == "" || query == "" {
		return 0, ""
	}
	f, err := os.Open(filePath)
	if err != nil {
		return 0, ""
	}
	defer f.Close()

	needle := strings.ToLower(query)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), needle) {
			count++
			if snippet == "" {
				snippet = cleanSnippet(line, needle)
			}
		}
	}
	return count, snippet
}
```

- [ ] **Step 2: Add test for SearchFile**

```go
// Add to internal/history/search_test.go
func TestSearchFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	content := `{"role":"user","content":"fix auth.go"}
{"role":"assistant","content":"looking at auth.go now"}
{"role":"tool","name":"Read","input":"auth.go"}
{"role":"assistant","content":"done with the fix"}
`
	os.WriteFile(path, []byte(content), 0644)

	count, snippet := SearchFile(path, "auth.go")
	if count != 3 {
		t.Errorf("expected 3 matches, got %d", count)
	}
	if snippet == "" {
		t.Error("expected non-empty snippet")
	}
}
```

- [ ] **Step 3: Add MATCH column to agents view**

In `internal/tui/views/agents.go`, add a `matchCounts map[int]int` field (PID -> match count). When non-nil, render a MATCH column. When a match count is 0, hide that row.

Add setter:

```go
func (v *AgentsView) SetMatchCounts(counts map[int]int) {
    v.matchCounts = counts
}

func (v *AgentsView) ClearMatchCounts() {
    v.matchCounts = nil
}
```

- [ ] **Step 4: Wire filter mode to search in app.go**

In `app.go`, modify the filter mode handler. When filter text is entered, debounce 200ms, then launch goroutines per agent:

```go
// On filter text change (debounced):
go func(agents []agent.Agent, query string) {
    counts := make(map[int]int)
    var mu sync.Mutex
    var wg sync.WaitGroup

    for _, ag := range agents {
        wg.Add(1)
        go func(a agent.Agent) {
            defer wg.Done()
            count, _ := history.SearchFile(a.SessionFile, query)
            mu.Lock()
            counts[a.PID] = count
            mu.Unlock()
        }(ag)
    }
    wg.Wait()
    a.program.Send(searchResultsMsg(counts))
}(a.instances, filterText)
```

Handle `searchResultsMsg` by calling `a.agentsView.SetMatchCounts(counts)`.

On filter clear (Esc), call `a.agentsView.ClearMatchCounts()`.

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/history/search.go internal/history/search_test.go internal/tui/views/agents.go internal/tui/app.go
git commit -m "feat(search): cross-session trace search with MATCH column in agents table"
```

---

## Task 11: Cost-per-Turn Toggle

**Files:**
- Modify: `internal/tui/views/logs.go`
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Add showCostPerTurn flag to LogsView**

In `internal/tui/views/logs.go`, add:

```go
showCostPerTurn bool
```

Add toggle method:

```go
func (l *LogsView) ToggleCostPerTurn() {
    l.showCostPerTurn = !l.showCostPerTurn
}
```

- [ ] **Step 2: Render cost inline after assistant turns**

In the turn rendering logic within LogsView, after rendering an assistant turn's content, when `showCostPerTurn` is true, append:

```go
if l.showCostPerTurn && turn.CostUSD > 0 {
    costLine := fmt.Sprintf("              [%s | %s tokens | $%.2f]",
        turn.Model,
        formatTokens(turn.TokensIn + turn.TokensOut),
        turn.CostUSD,
    )
    // Render in dim style
}
```

Add helper:

```go
func formatTokens(n int64) string {
    if n >= 1000 {
        return fmt.Sprintf("%.1fK", float64(n)/1000)
    }
    return fmt.Sprintf("%d", n)
}
```

- [ ] **Step 3: Wire `$` key in app.go**

In the key handler for the logs/trace view:

```go
case "$":
    if a.splitTrace != nil {
        a.splitTrace.ToggleCostPerTurn()
    }
    if a.currentView == viewLogs && a.logsView != nil {
        a.logsView.ToggleCostPerTurn()
    }
```

- [ ] **Step 4: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/views/logs.go internal/tui/app.go
git commit -m "feat(trace): toggle cost-per-turn display with $ key"
```

---

## Task 12: Discovery Watcher (fsnotify)

**Files:**
- Create: `internal/discovery/watcher.go`
- Create: `internal/discovery/watcher_test.go`
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/discovery/watcher_test.go
package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherDetectsNewFile(t *testing.T) {
	dir := t.TempDir()

	events := make(chan struct{}, 10)
	w, err := NewWatcher([]string{dir}, func() {
		events <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	// Create a new file
	os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}"), 0644)

	select {
	case <-events:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for file creation event")
	}
}

func TestWatcherSkipsMissingDirs(t *testing.T) {
	// Should not error on non-existent directories
	w, err := NewWatcher([]string{"/nonexistent/path"}, func() {})
	if err != nil {
		t.Fatalf("NewWatcher should not error on missing dirs: %v", err)
	}
	w.Stop()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/discovery/ -v -run TestWatcher`
Expected: FAIL (NewWatcher not defined)

- [ ] **Step 3: Write implementation**

```go
// internal/discovery/watcher.go
package discovery

import (
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors session directories for new files and triggers a callback.
// It debounces events to avoid excessive re-discovery on rapid file creation.
type Watcher struct {
	watcher  *fsnotify.Watcher
	onChange func()
	stop     chan struct{}
	done     sync.WaitGroup
}

// NewWatcher creates a watcher on the given directories. Directories that don't
// exist are silently skipped. The onChange callback is debounced to fire at most
// once per 500ms.
func NewWatcher(dirs []string, onChange func()) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err == nil {
			_ = fsw.Add(dir)
		}
	}

	w := &Watcher{
		watcher:  fsw,
		onChange: onChange,
		stop:    make(chan struct{}),
	}

	w.done.Add(1)
	go w.run()

	return w, nil
}

func (w *Watcher) run() {
	defer w.done.Done()

	var debounce *time.Timer

	for {
		select {
		case <-w.stop:
			if debounce != nil {
				debounce.Stop()
			}
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(500*time.Millisecond, w.onChange)
			}
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

// Stop shuts down the watcher.
func (w *Watcher) Stop() {
	close(w.stop)
	w.watcher.Close()
	w.done.Wait()
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/discovery/ -v -run TestWatcher`
Expected: PASS

- [ ] **Step 5: Wire into app.go**

In `NewApp()` or `Init()`, create the watcher:

```go
home, _ := os.UserHomeDir()
watchDirs := []string{
    filepath.Join(home, ".claude", "projects"),
    filepath.Join(home, ".codex"),
    filepath.Join(home, ".gemini"),
}
discoveryWatcher, _ := discovery.NewWatcher(watchDirs, func() {
    a.program.Send(discoveryTriggerMsg{})
})
```

Handle `discoveryTriggerMsg` by running `a.discoverInstances` as a `tea.Cmd`.

Add `discoveryWatcher *discovery.Watcher` field to App. Stop it in cleanup.

- [ ] **Step 6: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/discovery/watcher.go internal/discovery/watcher_test.go internal/tui/app.go
git commit -m "feat(discovery): fsnotify watcher for instant new-agent detection"
```

---

## Task 13: Notification Config (per-event)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Verify current NotificationsConfig**

The config already has `NotificationsConfig` with `Enabled`, `OnWaiting`, `OnError`, `OnIdle`, `Sound`. We need to add:
- `OnDone bool` (agent finished)
- `Bell bool` (terminal bell)
- `Desktop bool` (macOS notification center)

- [ ] **Step 2: Add new fields**

In `internal/config/config.go`:

```go
type NotificationsConfig struct {
	Enabled   bool `yaml:"enabled"`    // master switch (default: true)
	OnWaiting bool `yaml:"on_waiting"` // agent needs permission (default: true)
	OnDone    bool `yaml:"on_done"`    // agent finished (default: true)
	OnError   bool `yaml:"on_error"`   // agent crashed (default: true)
	OnIdle    bool `yaml:"on_idle"`    // agent finished turn (default: false)
	Bell      bool `yaml:"bell"`       // terminal bell (default: true)
	Desktop   bool `yaml:"desktop"`    // macOS notification center (default: true)
	Sound     bool `yaml:"sound"`      // play macOS sound (default: false)
}
```

- [ ] **Step 3: Update Default() to set new defaults**

In the `Default()` function, ensure:

```go
Notifications: NotificationsConfig{
    Enabled:   true,
    OnWaiting: true,
    OnDone:    true,
    OnError:   true,
    OnIdle:    false,
    Bell:      true,
    Desktop:   true,
    Sound:     false,
},
```

- [ ] **Step 4: Add test for new fields**

```go
func TestDefaultNotificationConfig(t *testing.T) {
    cfg := Default()
    if !cfg.Notifications.Bell {
        t.Error("expected Bell default true")
    }
    if !cfg.Notifications.Desktop {
        t.Error("expected Desktop default true")
    }
    if !cfg.Notifications.OnDone {
        t.Error("expected OnDone default true")
    }
}
```

- [ ] **Step 5: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add per-event notification settings (bell, desktop, on_done)"
```

---

## Task 14: OpenShell Stub Provider

**Files:**
- Create: `internal/provider/openshell.go`

- [ ] **Step 1: Write the stub**

```go
// internal/provider/openshell.go
package provider

import (
	"os/exec"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/trace"
)

// OpenShell discovers agents running in NVIDIA OpenShell sandboxes.
// This is an architecture placeholder. Functional implementation will
// follow once OpenShell's API stabilizes.
//
// Integration plan: discover sandboxes via `openshell sandbox list`,
// connect via `openshell sandbox connect`, parse traces from sandbox
// filesystem mounts or OTEL forwarding.
type OpenShell struct{}

var _ Provider = (*OpenShell)(nil)

func (o *OpenShell) Name() string                          { return "openshell" }
func (o *OpenShell) Discover() ([]agent.Agent, error)      { return nil, nil }
func (o *OpenShell) ResumeCommand(a agent.Agent) *exec.Cmd { return nil }
func (o *OpenShell) CanEmbed() bool                        { return false }
func (o *OpenShell) FindSessionFile(a agent.Agent) string  { return "" }
func (o *OpenShell) RecentDirs(max int) []RecentDir        { return nil }
func (o *OpenShell) SpawnCommand(dir, model, mode string) *exec.Cmd { return nil }
func (o *OpenShell) SpawnArgs() SpawnArgs                  { return SpawnArgs{} }
func (o *OpenShell) ParseTrace(filePath string) ([]trace.Turn, error) { return nil, nil }
func (o *OpenShell) OTELEnv(endpoint string) string        { return "" }
func (o *OpenShell) OTELServiceName() string               { return "openshell" }
func (o *OpenShell) SubagentAttrKeys() subagent.AttrKeys   { return subagent.AttrKeys{} }
func (o *OpenShell) Kill(a agent.Agent) error              { return nil }
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./internal/provider/`
Expected: success (no errors)

- [ ] **Step 3: Commit**

```bash
git add internal/provider/openshell.go
git commit -m "feat(provider): add OpenShell stub provider (architecture placeholder)"
```

---

## Task 15: Split View Loading Placeholder

**Files:**
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Show placeholder on split view entry**

In the code path that enters split mode (handling Enter key on an agent), before starting the PTY/tmux session, set a `splitLoading bool` flag to true:

```go
a.splitLoading = true
```

In the `View()` method, when `splitLoading` is true, render a centered "Loading session..." text in the session pane instead of the empty/frozen view.

- [ ] **Step 2: Clear placeholder when session connects**

When the PTY or tmux session produces its first output (in the `PTYOutputMsg` handler), clear the loading state:

```go
a.splitLoading = false
```

- [ ] **Step 3: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat(tui): show loading placeholder when entering split view"
```

---

## Task 16: README Rewrite

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Rewrite the README**

New structure:
1. Logo + tagline: "Tame the agent sprawl."
2. One-liner: "Every tool tells you which agents are running. aimux tells you what they actually did -- and whether it was good."
3. Demo GIF (keep existing `assets/demo.gif`)
4. Three differentiators with screenshots:
   - **Trace**: turn-by-turn view of prompts, responses, tool calls
   - **Annotate**: GOOD/BAD/WASTE labels, free-text notes
   - **Export**: OTEL to MLflow/Jaeger, JSONL to disk
5. Table-stakes features: discovery, split view, cost tracking, diff summary, live streaming, cross-session search, notifications
6. Install section (keep existing brew + source instructions)
7. Keybindings reference table
8. Configuration section

- [ ] **Step 2: Review diff**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && git diff README.md | head -100`

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: rewrite README with observability-first positioning"
```

---

## Task 17: Final Integration Test

- [ ] **Step 1: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all packages PASS

- [ ] **Step 2: Build binary**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build -o aimux ./cmd/aimux`
Expected: successful build, no errors

- [ ] **Step 3: Run go vet**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go vet ./...`
Expected: no issues

- [ ] **Step 4: Manual smoke test**

Run `./aimux` and verify:
- Agents appear instantly from cache (dim until confirmed)
- Split view opens with loading placeholder
- `d` toggles diff view in preview
- `$` toggles cost-per-turn in trace
- `/` searches across agent traces
- Header shows attention counter
- Terminal bell fires on agent state changes

---

## Task Order and Dependencies

```
Task 1  (cache core)           -- independent
Task 2  (cache wiring)         -- depends on Task 1
Task 3  (VT double-render)     -- independent
Task 4  (tmux poll)            -- independent
Task 5  (JSONL tailer)         -- independent
Task 6  (diff core)            -- independent
Task 7  (diff wiring)          -- depends on Task 6
Task 8  (attention + bell)     -- independent
Task 9  (live trace wiring)    -- depends on Task 5
Task 10 (cross-session search) -- independent
Task 11 (cost-per-turn)        -- independent
Task 12 (discovery watcher)    -- independent
Task 13 (notification config)  -- independent
Task 14 (OpenShell stub)       -- independent
Task 15 (split loading)        -- independent
Task 16 (README)               -- after all features
Task 17 (integration test)     -- after all tasks
```

**Parallelizable groups:**
- Group A: Tasks 1+2 (cache)
- Group B: Tasks 3, 4 (performance)
- Group C: Tasks 5+9 (tailer + wiring)
- Group D: Tasks 6+7 (diff)
- Group E: Tasks 8, 10, 11, 12, 13, 14, 15 (all independent)
- Group F: Tasks 16, 17 (final)
