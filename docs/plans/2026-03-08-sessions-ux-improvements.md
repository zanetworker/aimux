# Sessions UX Improvements — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Improve sessions view with failure-mode visibility, bulk cleanup, preview titles, and scrollable terminal embed.

**Architecture:** Four independent features touching sessions view, history package, preview pane, and terminal embed. Each can be implemented and tested separately.

**Tech Stack:** Go, Bubble Tea (TUI), charmbracelet/x/vt (terminal emulator), lipgloss (styling)

## Task 1: Failure-Mode Indicator

**Files:**
- Modify: `internal/tui/views/sessions.go` (renderSessionRow, sort cycle)
- Modify: `internal/tui/views/sessions_test.go`

**Step 1: Write failing test for the `!` indicator**

```go
func TestSessionsView_FailureModeIndicator(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessions())
	v.SetSize(100, 40)

	output := v.View()
	// ghi-789 has tags ["loop-on-error"], should show ! indicator
	// Find the line with "OTEL" (ghi-789's prompt)
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "OTEL") {
			if !strings.Contains(line, "!") {
				t.Error("expected ! indicator for tagged session ghi-789")
			}
		}
		// def-456 has no tags, should NOT show !
		if strings.Contains(line, "table support") {
			// Count ! occurrences — should not have the indicator
			if strings.Contains(line, " ! ") {
				t.Error("unexpected ! indicator for untagged session def-456")
			}
		}
	}
}
```

Run: `go test ./internal/tui/views/ -run TestSessionsView_FailureModeIndicator -v`
Expected: FAIL

**Step 2: Implement the indicator in renderSessionRow**

In `internal/tui/views/sessions.go`, in `renderSessionRow`, change the marker logic:

```go
// Current:
marker := "  "
if selected {
    marker = " \u25b8"
}

// New:
marker := "  "
if selected {
    marker = " \u25b8"
}
failIndicator := " "
if len(s.Tags) > 0 {
    failIndicator = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true).Render("!")
}
```

Then change `b.WriteString(marker + " ")` to `b.WriteString(marker + " " + failIndicator + " ")`.

Update `columnWidths` to account for the extra 2 chars (indicator + space) in the fixed width calculation:
```go
// In columnWidths, change:
fixed := 3 + c.age + c.turns + c.cost + 8
// To:
fixed := 5 + c.age + c.turns + c.cost + 8  // +2 for fail indicator
```

**Step 3: Add SortByFailureMode to sort cycle**

In `sessions.go`, add to the constants and cycle:

```go
const (
    SortByAge   SortField = iota
    SortByCost
    SortByTurns
    SortByTitle
    SortByFailureMode  // new
)

var sortFieldNames = map[SortField]string{
    SortByAge:         "AGE",
    SortByCost:        "COST",
    SortByTurns:       "TURNS",
    SortByTitle:       "TITLE",
    SortByFailureMode: "FAIL",
}

var sortFieldOrder = []SortField{SortByAge, SortByCost, SortByTurns, SortByTitle, SortByFailureMode}
```

Add the case to `cycleSortField` default direction:
```go
case SortByFailureMode:
    v.sortAsc = false // tagged first
```

Add the case to `compareSessions`:
```go
case SortByFailureMode:
    aHas := len(a.Tags) > 0
    bHas := len(b.Tags) > 0
    if aHas != bHas {
        return !aHas // tagged sorts before untagged in ascending
    }
    aTag, bTag := "", ""
    if len(a.Tags) > 0 { aTag = strings.ToLower(a.Tags[0]) }
    if len(b.Tags) > 0 { bTag = strings.ToLower(b.Tags[0]) }
    return aTag < bTag
```

Add "FAIL" to the column header `colHeader` calls (same pattern as others).

**Step 4: Write test for failure-mode sort**

```go
func TestSessionsView_SortByFailureMode(t *testing.T) {
    v := NewSessionsView()
    v.SetSessions(testSessions())
    v.SetSize(100, 40)

    // Cycle to SortByFailureMode: Age -> Cost -> Turns -> Title -> FailureMode
    for i := 0; i < 4; i++ {
        v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
    }
    if v.sortField != SortByFailureMode {
        t.Errorf("sortField = %d, want SortByFailureMode", v.sortField)
    }

    visible := v.visibleSessions()
    // ghi-789 has tags, should be first (descending = tagged first)
    if visible[0].ID != "ghi-789" {
        t.Errorf("failure-mode sort: first = %q, want ghi-789 (tagged)", visible[0].ID)
    }
}
```

Run: `go test ./internal/tui/views/ -run TestSessionsView_FailureMode -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All pass

**Step 6: Commit**

```bash
git add internal/tui/views/sessions.go internal/tui/views/sessions_test.go
git commit -m "feat: add failure-mode indicator and sort in sessions view"
```

## Task 2: Titles in Preview Pane

**Files:**
- Modify: `internal/tui/views/preview.go` (renderHeader)
- Modify: `internal/history/history.go` (add TitleForSessionFile)
- Test: `internal/tui/views/preview_test.go` (create if missing, or add test)

**Step 1: Add TitleForSessionFile helper to history package**

In `internal/history/history.go`, add:

```go
// TitleForSessionFile returns the LLM-generated title from the sidecar
// .meta.json file, or "" if none exists.
func TitleForSessionFile(sessionFilePath string) string {
    if sessionFilePath == "" {
        return ""
    }
    meta := LoadMeta(sessionFilePath)
    return meta.Title
}
```

**Step 2: Write failing test**

```go
func TestTitleForSessionFile(t *testing.T) {
    dir := t.TempDir()
    sessionFile := filepath.Join(dir, "test.jsonl")
    os.WriteFile(sessionFile, []byte("{}"), 0o644)

    // No meta file — should return ""
    if got := TitleForSessionFile(sessionFile); got != "" {
        t.Errorf("expected empty title, got %q", got)
    }

    // With meta file
    SaveMeta(sessionFile, Meta{Title: "Fix markdown rendering"})
    if got := TitleForSessionFile(sessionFile); got != "Fix markdown rendering" {
        t.Errorf("expected title, got %q", got)
    }
}
```

Run: `go test ./internal/history/ -run TestTitleForSessionFile -v`
Expected: PASS (implementation is straightforward)

**Step 3: Add title line to preview header**

In `internal/tui/views/preview.go`, in `renderHeader()`, after the `nameLine` construction (around line 166), add:

```go
// Session title (from LLM-generated meta)
var titleLine string
if a.SessionFile != "" {
    title := history.TitleForSessionFile(a.SessionFile)
    if title == "" {
        // Fall back to first prompt from trace
        if p.logsView != nil {
            turns := p.logsView.Turns()
            for _, t := range turns {
                if t.Role == "user" && t.Text != "" {
                    title = t.Text
                    if len(title) > 60 {
                        title = title[:57] + "..."
                    }
                    break
                }
            }
        }
    }
    if title != "" {
        titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Italic(true)
        titleLine = titleStyle.Render(truncatePreview(title, maxW))
    }
}
```

Then add it to the result assembly after `nameLine`:
```go
result := nameLine + "\n"
if titleLine != "" {
    result += titleLine + "\n"
}
result += infoLine + "\n"
```

Add the import for history package at the top of preview.go:
```go
"github.com/zanetworker/aimux/internal/history"
```

**Step 4: Run full test suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All pass

**Step 5: Commit**

```bash
git add internal/tui/views/preview.go internal/history/history.go internal/history/history_test.go
git commit -m "feat: show session titles in agent preview pane"
```

## Task 3: Bulk Cleanup

**Files:**
- Modify: `internal/history/history.go` (FindDuplicates, FindEmpty)
- Create: `internal/history/cleanup_test.go`
- Modify: `internal/tui/views/sessions.go` (cleanup mode)
- Modify: `internal/tui/views/sessions_test.go`
- Modify: `internal/tui/app.go` (wire SessionBulkDeleteMsg, hint bar)

**Step 1: Write failing tests for FindDuplicates and FindEmpty**

Create `internal/history/cleanup_test.go`:

```go
package history

import (
    "testing"
    "time"
)

func TestFindEmpty(t *testing.T) {
    sessions := []Session{
        {ID: "a", TurnCount: 16, CostUSD: 0.42},
        {ID: "b", TurnCount: 1, CostUSD: 0},
        {ID: "c", TurnCount: 2, CostUSD: 0},
        {ID: "d", TurnCount: 3, CostUSD: 0.10},
    }
    empty := FindEmpty(sessions)
    if len(empty) != 2 {
        t.Fatalf("expected 2 empty, got %d", len(empty))
    }
    ids := map[string]bool{}
    for _, s := range empty {
        ids[s.ID] = true
    }
    if !ids["b"] || !ids["c"] {
        t.Errorf("expected b and c as empty, got %v", empty)
    }
}

func TestFindDuplicates(t *testing.T) {
    now := time.Now()
    sessions := []Session{
        {ID: "a", Project: "/proj", FirstPrompt: "fix the bug", TurnCount: 20, LastActive: now},
        {ID: "b", Project: "/proj", FirstPrompt: "fix the bug", TurnCount: 5, LastActive: now.Add(-time.Hour)},
        {ID: "c", Project: "/proj", FirstPrompt: "fix the bug", TurnCount: 2, LastActive: now.Add(-2 * time.Hour)},
        {ID: "d", Project: "/proj", FirstPrompt: "different task", TurnCount: 10, LastActive: now},
    }
    dupes := FindDuplicates(sessions)
    if len(dupes) != 2 {
        t.Fatalf("expected 2 duplicates, got %d", len(dupes))
    }
    // "a" should be kept (most turns), "b" and "c" are duplicates
    ids := map[string]bool{}
    for _, s := range dupes {
        ids[s.ID] = true
    }
    if ids["a"] {
        t.Error("session 'a' should be the keeper, not a duplicate")
    }
    if !ids["b"] || !ids["c"] {
        t.Errorf("expected b and c as duplicates, got %v", dupes)
    }
}

func TestFindDuplicates_DifferentProjects(t *testing.T) {
    sessions := []Session{
        {ID: "a", Project: "/proj1", FirstPrompt: "fix the bug", TurnCount: 10},
        {ID: "b", Project: "/proj2", FirstPrompt: "fix the bug", TurnCount: 5},
    }
    dupes := FindDuplicates(sessions)
    if len(dupes) != 0 {
        t.Errorf("expected 0 duplicates across projects, got %d", len(dupes))
    }
}
```

Run: `go test ./internal/history/ -run "TestFind" -v`
Expected: FAIL (functions don't exist)

**Step 2: Implement FindEmpty and FindDuplicates**

In `internal/history/history.go`, add:

```go
// FindEmpty returns sessions with very low activity (<=2 turns and $0 cost).
func FindEmpty(sessions []Session) []Session {
    var result []Session
    for _, s := range sessions {
        if s.TurnCount <= 2 && s.CostUSD == 0 {
            result = append(result, s)
        }
    }
    return result
}

// FindDuplicates returns duplicate session candidates. Sessions with the same
// FirstPrompt (or Title) within the same project are grouped; the one with the
// most turns is kept, the rest are returned as duplicates.
func FindDuplicates(sessions []Session) []Session {
    type groupKey struct {
        project string
        prompt  string
    }

    groups := make(map[groupKey][]Session)
    for _, s := range sessions {
        prompt := s.Title
        if prompt == "" {
            prompt = s.FirstPrompt
        }
        if prompt == "" || prompt == "(no prompt)" {
            continue
        }
        key := groupKey{project: s.Project, prompt: prompt}
        groups[key] = append(groups[key], s)
    }

    var dupes []Session
    for _, group := range groups {
        if len(group) < 2 {
            continue
        }
        // Find the keeper (most turns)
        bestIdx := 0
        for i, s := range group {
            if s.TurnCount > group[bestIdx].TurnCount {
                bestIdx = i
            }
        }
        for i, s := range group {
            if i != bestIdx {
                dupes = append(dupes, s)
            }
        }
    }
    return dupes
}
```

Run: `go test ./internal/history/ -run "TestFind" -v`
Expected: PASS

**Step 3: Add cleanup mode state to SessionsView**

In `internal/tui/views/sessions.go`, add fields to the struct:

```go
// Cleanup mode
cleanupMode     bool
cleanupItems    []cleanupItem
cleanupCursor   int
```

Add the cleanupItem type and SessionBulkDeleteMsg:

```go
type cleanupItem struct {
    session  history.Session
    reason   string // "duplicate" or "empty"
    selected bool
}

// SessionBulkDeleteMsg is emitted when the user confirms bulk deletion.
type SessionBulkDeleteMsg struct {
    Sessions []history.Session
}
```

**Step 4: Add `D` keybinding and cleanup mode handlers**

In the `Update` switch, add before the existing cases:

```go
if v.cleanupMode {
    return v.handleCleanupKey(msg)
}
```

Add the `D` case:
```go
case "D":
    v.enterCleanupMode()
```

Implement `enterCleanupMode`:
```go
func (v *SessionsView) enterCleanupMode() {
    visible := v.visibleSessions()
    dupes := history.FindDuplicates(visible)
    empty := history.FindEmpty(visible)

    dupeIDs := make(map[string]bool)
    for _, s := range dupes {
        dupeIDs[s.ID] = true
    }

    var items []cleanupItem
    for _, s := range dupes {
        items = append(items, cleanupItem{session: s, reason: "duplicate", selected: true})
    }
    for _, s := range empty {
        if !dupeIDs[s.ID] {
            items = append(items, cleanupItem{session: s, reason: "empty", selected: true})
        }
    }

    if len(items) == 0 {
        return // nothing to clean
    }
    v.cleanupMode = true
    v.cleanupItems = items
    v.cleanupCursor = 0
}
```

Implement `handleCleanupKey`:
```go
func (v *SessionsView) handleCleanupKey(msg tea.KeyMsg) tea.Cmd {
    switch msg.String() {
    case "j", "down":
        if v.cleanupCursor < len(v.cleanupItems)-1 {
            v.cleanupCursor++
        }
    case "k", "up":
        if v.cleanupCursor > 0 {
            v.cleanupCursor--
        }
    case " ":
        v.cleanupItems[v.cleanupCursor].selected = !v.cleanupItems[v.cleanupCursor].selected
    case "a":
        allSelected := true
        for _, item := range v.cleanupItems {
            if !item.selected {
                allSelected = false
                break
            }
        }
        for i := range v.cleanupItems {
            v.cleanupItems[i].selected = !allSelected
        }
    case "enter":
        var toDelete []history.Session
        for _, item := range v.cleanupItems {
            if item.selected {
                toDelete = append(toDelete, item.session)
            }
        }
        v.cleanupMode = false
        v.cleanupItems = nil
        if len(toDelete) == 0 {
            return nil
        }
        // Remove from local sessions list
        deleteIDs := make(map[string]bool)
        for _, s := range toDelete {
            deleteIDs[s.ID] = true
        }
        var kept []history.Session
        for _, s := range v.sessions {
            if !deleteIDs[s.ID] {
                kept = append(kept, s)
            }
        }
        v.sessions = kept
        v.cursor = 0
        v.previewLogs = nil
        sessions := toDelete
        return func() tea.Msg {
            return SessionBulkDeleteMsg{Sessions: sessions}
        }
    case "esc":
        v.cleanupMode = false
        v.cleanupItems = nil
    }
    return nil
}
```

Update `HasActiveInput` to include cleanup mode:
```go
func (v *SessionsView) HasActiveInput() bool {
    return v.filterMode || v.tagMode || v.noteMode || v.deleteMode || v.cleanupMode
}
```

**Step 5: Render cleanup mode in View**

In the `View()` method, after the filter indicator block and before the session list, add a cleanup-mode branch:

```go
if v.cleanupMode {
    b.WriteString(v.renderCleanupView(w))
    return b.String()
}
```

Implement `renderCleanupView`:
```go
func (v *SessionsView) renderCleanupView(w int) string {
    var b strings.Builder

    dupeCount, emptyCount, selectedCount := 0, 0, 0
    for _, item := range v.cleanupItems {
        if item.reason == "duplicate" { dupeCount++ }
        if item.reason == "empty" { emptyCount++ }
        if item.selected { selectedCount++ }
    }

    warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
    headerStr := fmt.Sprintf("  Cleanup: %d duplicates, %d empty -- %d selected", dupeCount, emptyCount, selectedCount)
    b.WriteString(warnStyle.Render(headerStr) + "\n")
    b.WriteString(sessDimStyle.Render("  space:toggle  a:all  enter:delete  esc:cancel") + "\n\n")

    listHeight := v.height - 8
    if listHeight < 3 { listHeight = 3 }

    start := 0
    if v.cleanupCursor >= listHeight {
        start = v.cleanupCursor - listHeight + 1
    }
    end := start + listHeight
    if end > len(v.cleanupItems) { end = len(v.cleanupItems) }

    for i := start; i < end; i++ {
        item := v.cleanupItems[i]
        selected := i == v.cleanupCursor

        check := "[ ]"
        if item.selected {
            check = "[x]"
        }

        reason := sessDimStyle.Render(fmt.Sprintf("(%s)", item.reason))
        prompt := item.session.Title
        if prompt == "" { prompt = item.session.FirstPrompt }
        if prompt == "" { prompt = "(no prompt)" }
        if len(prompt) > 50 { prompt = prompt[:47] + "..." }

        turnStr := sessTurnStyle.Render(fmt.Sprintf("%dt", item.session.TurnCount))
        age := sessAgeStyle.Render(formatAge(item.session.LastActive))

        line := fmt.Sprintf("  %s %s  %s  %s  %s  %s", check, reason, age, truncate(prompt, 50), turnStr, sessCostStyle.Render(fmt.Sprintf("$%.2f", item.session.CostUSD)))
        if selected {
            line = sessSelectedStyle.Render(padRight(line, w))
        }
        b.WriteString(line + "\n")
    }

    return b.String()
}
```

**Step 6: Wire SessionBulkDeleteMsg in app.go**

In `internal/tui/app.go`, add a case after `SessionDeleteMsg`:

```go
case views.SessionBulkDeleteMsg:
    deleted := 0
    for _, s := range msg.Sessions {
        if err := os.Remove(s.FilePath); err == nil {
            metaPath := history.MetaPath(s.FilePath)
            os.Remove(metaPath)
            deleted++
        }
    }
    a.statusHint = fmt.Sprintf("Deleted %d sessions", deleted)
```

Update hint bars (both locations) to add `D:cleanup`.

**Step 7: Write cleanup mode tests**

```go
func TestSessionsView_CleanupMode(t *testing.T) {
    v := NewSessionsView()
    sessions := []history.Session{
        {ID: "a", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 20, CostUSD: 1.0, LastActive: time.Now()},
        {ID: "b", Project: "/proj", FirstPrompt: "fix bug", TurnCount: 2, CostUSD: 0.1, LastActive: time.Now()},
        {ID: "c", Project: "/proj", FirstPrompt: "other", TurnCount: 1, CostUSD: 0, LastActive: time.Now()},
    }
    v.SetSessions(sessions)
    v.SetSize(100, 40)

    // Enter cleanup mode
    v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
    if !v.cleanupMode {
        t.Fatal("expected cleanup mode")
    }
    // Should have 2 items: "b" (duplicate) and "c" (empty)
    if len(v.cleanupItems) != 2 {
        t.Fatalf("expected 2 cleanup items, got %d", len(v.cleanupItems))
    }

    // Toggle first item off
    v.handleCleanupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
    if v.cleanupItems[0].selected {
        t.Error("expected first item deselected after space")
    }

    // Cancel
    v.handleCleanupKey(tea.KeyMsg{Type: tea.KeyEscape})
    if v.cleanupMode {
        t.Error("expected cleanup mode exited after esc")
    }
}
```

**Step 8: Run full test suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All pass

**Step 9: Commit**

```bash
git add internal/history/history.go internal/history/cleanup_test.go internal/tui/views/sessions.go internal/tui/views/sessions_test.go internal/tui/app.go
git commit -m "feat: bulk cleanup mode for duplicate and empty sessions"
```

## Task 4: Scrollable Terminal Embed

**Files:**
- Modify: `internal/terminal/view.go` (scroll-back buffer)
- Modify: `internal/tui/views/session.go` (scroll state, keybindings)
- Modify: `internal/tui/app.go` (intercept PgUp/PgDn before forwarding to PTY)
- Test: `internal/terminal/view_test.go`

**Step 1: Add scroll-back buffer to TermView**

In `internal/terminal/view.go`, add fields:

```go
type TermView struct {
    term       *vt.SafeEmulator
    mu         sync.Mutex
    width      int
    height     int
    history    []string // scroll-back: completed screen lines
    scrollBack int      // 0 = live bottom, >0 = lines scrolled up
}
```

Modify `Write` to capture lines that scroll off-screen. The VT emulator doesn't expose a scroll-back buffer, so we capture the full screen before each write and detect scroll:

```go
func (tv *TermView) Write(data []byte) {
    tv.mu.Lock()
    // Capture current top line before write (if it will scroll off)
    preRender := tv.term.Render()
    tv.mu.Unlock()

    tv.term.Write(data)

    tv.mu.Lock()
    defer tv.mu.Unlock()

    // Compare pre/post to detect scrolled lines
    preLines := strings.Split(preRender, "\n")
    postRender := tv.term.Render()
    postLines := strings.Split(postRender, "\n")

    // If first line changed, the old first line scrolled off
    if len(preLines) > 0 && len(postLines) > 0 && preLines[0] != postLines[0] {
        // Add non-empty pre lines that are no longer visible
        for _, line := range preLines {
            trimmed := strings.TrimRight(line, " ")
            if trimmed != "" {
                tv.history = append(tv.history, line)
            }
        }
    }

    // Auto-snap to bottom on new output
    tv.scrollBack = 0
}
```

Add scroll methods:

```go
// ScrollUp moves the viewport up by n lines.
func (tv *TermView) ScrollUp(n int) {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    tv.scrollBack += n
    if tv.scrollBack > len(tv.history) {
        tv.scrollBack = len(tv.history)
    }
}

// ScrollDown moves the viewport down by n lines. Snaps to live at 0.
func (tv *TermView) ScrollDown(n int) {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    tv.scrollBack -= n
    if tv.scrollBack < 0 {
        tv.scrollBack = 0
    }
}

// IsScrolled returns true if viewing history (not live bottom).
func (tv *TermView) IsScrolled() bool {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    return tv.scrollBack > 0
}

// SnapToBottom resets scroll to live view.
func (tv *TermView) SnapToBottom() {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    tv.scrollBack = 0
}
```

Modify `Render` to show history when scrolled:

```go
func (tv *TermView) Render() string {
    tv.mu.Lock()
    scrollBack := tv.scrollBack
    history := tv.history
    tv.mu.Unlock()

    if scrollBack == 0 || len(history) == 0 {
        return tv.term.Render()
    }

    // Show history lines instead of live terminal
    end := len(history)
    start := end - scrollBack
    if start < 0 {
        start = 0
    }

    // Take height lines from history starting at 'start'
    height := tv.Height()
    visible := history[start:]
    if len(visible) > height {
        visible = visible[:height]
    }
    return strings.Join(visible, "\n")
}
```

Add `"strings"` to imports.

**Step 2: Add scroll keybindings in SessionView**

In `internal/tui/views/session.go`, modify `View()` to show scroll indicator:

```go
// After termContent assignment, add:
if sv.termView != nil && sv.termView.IsScrolled() {
    scrollIndicator := lipgloss.NewStyle().
        Foreground(lipgloss.Color("#F59E0B")).Bold(true).
        Render("-- scrolled (PgDn to resume) --")
    // Replace last line of termContent with indicator
    lines := strings.Split(termContent, "\n")
    if len(lines) > 0 {
        lines[len(lines)-1] = scrollIndicator
        termContent = strings.Join(lines, "\n")
    }
}
```

**Step 3: Intercept PgUp/PgDn in app.go before forwarding to PTY**

In `internal/tui/app.go`, find where keystrokes are forwarded to the session view. Before `sv.SendKey(keyStr)`, add:

```go
// Intercept scroll keys
switch keyStr {
case "pgup":
    if sv.termView != nil {
        sv.termView.ScrollUp(sv.height / 2)
        return a, nil
    }
case "pgdown":
    if sv.termView != nil {
        sv.termView.ScrollDown(sv.height / 2)
        return a, nil
    }
}
```

Note: `termView` is private in `SessionView`. Add a public accessor:
```go
// TermView returns the underlying terminal view, or nil for DirectRenderer backends.
func (sv *SessionView) TermView() *terminal.TermView {
    return sv.termView
}
```

Update session header hint to include PgUp/PgDn:
```go
right := sessionHintStyle.Render(" PgUp/PgDn:scroll  Tab:trace  Ctrl+f:split  Esc:exit ")
```

**Step 4: Write test for scroll methods**

In `internal/terminal/view_test.go` (create or add):

```go
func TestTermView_ScrollUpDown(t *testing.T) {
    tv := NewTermView(80, 24)

    if tv.IsScrolled() {
        t.Error("should not be scrolled initially")
    }

    // Simulate some history
    tv.mu.Lock()
    for i := 0; i < 50; i++ {
        tv.history = append(tv.history, fmt.Sprintf("line %d", i))
    }
    tv.mu.Unlock()

    tv.ScrollUp(10)
    if !tv.IsScrolled() {
        t.Error("should be scrolled after ScrollUp")
    }

    tv.ScrollDown(10)
    if tv.IsScrolled() {
        t.Error("should not be scrolled after ScrollDown back to 0")
    }

    // Scroll past history
    tv.ScrollUp(100)
    tv.mu.Lock()
    if tv.scrollBack != 50 {
        t.Errorf("scrollBack = %d, want 50 (clamped to history length)", tv.scrollBack)
    }
    tv.mu.Unlock()

    tv.SnapToBottom()
    if tv.IsScrolled() {
        t.Error("should not be scrolled after SnapToBottom")
    }
}
```

**Step 5: Run full test suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All pass

**Step 6: Commit**

```bash
git add internal/terminal/view.go internal/terminal/view_test.go internal/tui/views/session.go internal/tui/app.go
git commit -m "feat: scrollable terminal embed with PgUp/PgDn"
```
