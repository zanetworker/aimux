# Sessions Fuzzy Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add fuzzy search and interactive session picking to `aimux sessions`, with a `--danger` flag for `--dangerously-skip-permissions` resume.

**Architecture:** Three layers: (1) `FilterByPrompt` in `history/search.go` for metadata matching, (2) new `sessions/picker.go` package for fzf/bubbletea interactive picking, (3) updated CLI wiring in `main.go` for flag parsing and search+pick+resume orchestration.

**Tech Stack:** Go, fzf (external, optional), charmbracelet/bubbletea (existing dependency), history package (existing).

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/history/search.go` | Modify | Add `FilterByPrompt()` |
| `internal/history/search_test.go` | Modify | Tests for `FilterByPrompt()` |
| `internal/sessions/picker.go` | Create | fzf and bubbletea session pickers |
| `internal/sessions/picker_test.go` | Create | Tests for formatting, parsing, picker logic |
| `cmd/aimux/main.go` | Modify | Wire `--danger`, query arg, picker, resume changes |

---

### Task 1: Add `FilterByPrompt` to history/search.go

**Files:**
- Modify: `internal/history/search.go`
- Modify: `internal/history/search_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/history/search_test.go`:

```go
func TestFilterByPrompt_MatchesTitle(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "Add clipboard support", FirstPrompt: "do something"},
		{ID: "b", Title: "Fix auth bug", FirstPrompt: "help me"},
	}
	result := FilterByPrompt(sessions, "clipboard")
	if len(result) != 1 {
		t.Fatalf("got %d, want 1", len(result))
	}
	if result[0].ID != "a" {
		t.Errorf("got ID %q, want %q", result[0].ID, "a")
	}
}

func TestFilterByPrompt_MatchesFirstPrompt(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "", FirstPrompt: "implement fuzzy search"},
		{ID: "b", Title: "", FirstPrompt: "fix the tests"},
	}
	result := FilterByPrompt(sessions, "fuzzy")
	if len(result) != 1 {
		t.Fatalf("got %d, want 1", len(result))
	}
	if result[0].ID != "a" {
		t.Errorf("got ID %q, want %q", result[0].ID, "a")
	}
}

func TestFilterByPrompt_CaseInsensitive(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "UPPERCASE TITLE", FirstPrompt: ""},
	}
	result := FilterByPrompt(sessions, "uppercase")
	if len(result) != 1 {
		t.Fatalf("got %d, want 1", len(result))
	}
}

func TestFilterByPrompt_EmptyQuery(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "something"},
	}
	result := FilterByPrompt(sessions, "")
	if len(result) != 1 {
		t.Fatalf("empty query should return all sessions, got %d", len(result))
	}
}

func TestFilterByPrompt_NoMatch(t *testing.T) {
	sessions := []Session{
		{ID: "a", Title: "auth fix", FirstPrompt: "fix auth"},
	}
	result := FilterByPrompt(sessions, "clipboard")
	if len(result) != 0 {
		t.Errorf("got %d, want 0", len(result))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/history/ -run TestFilterByPrompt -v`
Expected: FAIL with "undefined: FilterByPrompt"

- [ ] **Step 3: Implement `FilterByPrompt`**

Add to `internal/history/search.go`:

```go
// FilterByPrompt returns sessions whose Title or FirstPrompt contains the
// query string (case-insensitive). An empty query returns all sessions.
func FilterByPrompt(sessions []Session, query string) []Session {
	if query == "" {
		result := make([]Session, len(sessions))
		copy(result, sessions)
		return result
	}
	needle := strings.ToLower(query)
	var result []Session
	for _, s := range sessions {
		if strings.Contains(strings.ToLower(s.Title), needle) ||
			strings.Contains(strings.ToLower(s.FirstPrompt), needle) {
			result = append(result, s)
		}
	}
	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/history/ -run TestFilterByPrompt -v`
Expected: all 5 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/history/search.go internal/history/search_test.go
git commit -m "feat(history): add FilterByPrompt for session metadata search"
```

---

### Task 2: Create sessions/picker.go with fzf backend

**Files:**
- Create: `internal/sessions/picker.go`
- Create: `internal/sessions/picker_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/sessions/picker_test.go`:

```go
package sessions

import (
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

func TestFormatLine_BasicSession(t *testing.T) {
	s := history.Session{
		ID:          "abc-123-def",
		Project:     "/Users/me/myproject",
		LastActive:  time.Now().Add(-2 * time.Hour),
		TurnCount:   15,
		CostUSD:     1.23,
		Title:       "Add fuzzy search",
	}
	line := FormatLine(s)
	if !strings.Contains(line, "abc-123-def") {
		t.Errorf("line missing ID: %q", line)
	}
	if !strings.Contains(line, "myproject") {
		t.Errorf("line missing project: %q", line)
	}
	if !strings.Contains(line, "Add fuzzy search") {
		t.Errorf("line missing title: %q", line)
	}
}

func TestFormatLine_FallsBackToFirstPrompt(t *testing.T) {
	s := history.Session{
		ID:          "xyz-789",
		Project:     "/Users/me/proj",
		LastActive:  time.Now(),
		TurnCount:   5,
		CostUSD:     0.50,
		Title:       "",
		FirstPrompt: "fix the auth bug",
	}
	line := FormatLine(s)
	if !strings.Contains(line, "fix the auth bug") {
		t.Errorf("line missing first prompt: %q", line)
	}
}

func TestParseSelectedLine_ExtractsID(t *testing.T) {
	line := "abc-123-def  myproject  2h ago   15T  $  1.23  Add fuzzy search"
	id := ParseSelectedID(line)
	if id != "abc-123-def" {
		t.Errorf("got %q, want %q", id, "abc-123-def")
	}
}

func TestParseSelectedLine_EmptyLine(t *testing.T) {
	id := ParseSelectedID("")
	if id != "" {
		t.Errorf("got %q, want empty", id)
	}
}

func TestParseSelectedLine_WhitespaceOnly(t *testing.T) {
	id := ParseSelectedID("   ")
	if id != "" {
		t.Errorf("got %q, want empty", id)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/sessions/ -v`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Implement the sessions package with fzf picker**

Create `internal/sessions/picker.go`:

```go
// Package sessions provides interactive session picking via fzf or bubbletea.
package sessions

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

// ErrCancelled is returned when the user cancels the picker.
var ErrCancelled = fmt.Errorf("selection cancelled")

// PickSession presents an interactive picker for the given sessions.
// Uses fzf if available, falls back to a bubbletea list.
// Returns the selected session or ErrCancelled.
func PickSession(sessions []history.Session) (history.Session, error) {
	if len(sessions) == 0 {
		return history.Session{}, fmt.Errorf("no sessions to pick from")
	}
	if hasFzf() {
		return fzfPick(sessions)
	}
	return bubbleteaPick(sessions)
}

// FormatLine formats a session as a single display line for the picker.
func FormatLine(s history.Session) string {
	proj := shortProject(s.Project)
	age := shortAge(s.LastActive)
	prompt := s.Title
	if prompt == "" {
		prompt = s.FirstPrompt
	}
	if len(prompt) > 60 {
		prompt = prompt[:57] + "..."
	}
	if prompt == "" {
		prompt = "-"
	}
	return fmt.Sprintf("%-38s  %-14s  %-7s  %4dT  $%6.2f  %s",
		s.ID, truncStr(proj, 14), age, s.TurnCount, s.CostUSD, prompt)
}

// ParseSelectedID extracts the session ID from a picker output line.
// The ID is the first whitespace-delimited field.
func ParseSelectedID(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func hasFzf() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

func fzfPick(sessions []history.Session) (history.Session, error) {
	var input bytes.Buffer
	for _, s := range sessions {
		input.WriteString(FormatLine(s))
		input.WriteString("\n")
	}

	fzfBin, _ := exec.LookPath("fzf")
	cmd := exec.Command(fzfBin, // #nosec G204
		"--ansi",
		"--no-multi",
		"--header", "ID                                      PROJECT         AGE      TURNS    COST  PROMPT",
		"--prompt", "session> ",
	)
	cmd.Stdin = &input
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return history.Session{}, ErrCancelled
		}
		return history.Session{}, ErrCancelled
	}

	selectedID := ParseSelectedID(string(out))
	if selectedID == "" {
		return history.Session{}, ErrCancelled
	}

	for _, s := range sessions {
		if s.ID == selectedID {
			return s, nil
		}
	}
	return history.Session{}, fmt.Errorf("session %q not found", selectedID)
}

func shortProject(path string) string {
	if path == "" {
		return "(unknown)"
	}
	return filepath.Base(path)
}

func shortAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/sessions/ -v`
Expected: all 5 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/sessions/picker.go internal/sessions/picker_test.go
git commit -m "feat(sessions): add fzf-based interactive session picker"
```

---

### Task 3: Add Bubble Tea fallback picker

**Files:**
- Modify: `internal/sessions/picker.go`
- Modify: `internal/sessions/picker_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/sessions/picker_test.go`:

```go
func TestPickerModel_FilterReducesList(t *testing.T) {
	sessions := []history.Session{
		{ID: "aaa", Title: "Add clipboard"},
		{ID: "bbb", Title: "Fix auth"},
		{ID: "ccc", Title: "Add search"},
	}
	m := newPickerModel(sessions)

	// Simulate typing "add"
	m.filter = "add"
	filtered := m.filteredSessions()
	if len(filtered) != 2 {
		t.Fatalf("got %d filtered, want 2", len(filtered))
	}
}

func TestPickerModel_FilterCaseInsensitive(t *testing.T) {
	sessions := []history.Session{
		{ID: "aaa", Title: "UPPERCASE"},
	}
	m := newPickerModel(sessions)
	m.filter = "upper"
	filtered := m.filteredSessions()
	if len(filtered) != 1 {
		t.Fatalf("got %d filtered, want 1", len(filtered))
	}
}

func TestPickerModel_EmptyFilterShowsAll(t *testing.T) {
	sessions := []history.Session{
		{ID: "aaa", Title: "one"},
		{ID: "bbb", Title: "two"},
	}
	m := newPickerModel(sessions)
	m.filter = ""
	filtered := m.filteredSessions()
	if len(filtered) != 2 {
		t.Fatalf("got %d filtered, want 2", len(filtered))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/sessions/ -run TestPickerModel -v`
Expected: FAIL with "undefined: newPickerModel"

- [ ] **Step 3: Implement the Bubble Tea fallback picker**

Add to `internal/sessions/picker.go`:

```go
import (
	// add to existing imports:
	tea "github.com/charmbracelet/bubbletea"
)

type pickerModel struct {
	sessions []history.Session
	filter   string
	cursor   int
	selected history.Session
	done     bool
	cancelled bool
	width    int
	height   int
}

func newPickerModel(sessions []history.Session) pickerModel {
	return pickerModel{
		sessions: sessions,
		width:    120,
		height:   24,
	}
}

func (m pickerModel) filteredSessions() []history.Session {
	if m.filter == "" {
		return m.sessions
	}
	needle := strings.ToLower(m.filter)
	var result []history.Session
	for _, s := range m.sessions {
		if strings.Contains(strings.ToLower(s.Title), needle) ||
			strings.Contains(strings.ToLower(s.FirstPrompt), needle) ||
			strings.Contains(strings.ToLower(s.Project), needle) ||
			strings.Contains(strings.ToLower(s.ID), needle) {
			result = append(result, s)
		}
	}
	return result
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			m.done = true
			return m, tea.Quit
		case "enter":
			filtered := m.filteredSessions()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				m.selected = filtered[m.cursor]
				m.done = true
				return m, tea.Quit
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			filtered := m.filteredSessions()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.cursor = 0
			}
		default:
			if len(msg.String()) == 1 {
				m.filter += msg.String()
				m.cursor = 0
			}
		}
	}
	return m, nil
}

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(" session> %s\n", m.filter))
	b.WriteString(" ──────────────────────────────────────────────────────────────────────\n")

	filtered := m.filteredSessions()
	if len(filtered) == 0 {
		b.WriteString(" No matching sessions.\n")
		return b.String()
	}

	maxVisible := m.height - 4
	if maxVisible < 5 {
		maxVisible = 5
	}
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
	}

	for i := start; i < end; i++ {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		b.WriteString(prefix)
		b.WriteString(FormatLine(filtered[i]))
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("\n %d/%d sessions  |  type to filter  |  enter to resume  |  esc to cancel",
		len(filtered), len(m.sessions)))
	return b.String()
}

func bubbleteaPick(sessions []history.Session) (history.Session, error) {
	m := newPickerModel(sessions)
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return history.Session{}, fmt.Errorf("picker error: %w", err)
	}

	final := result.(pickerModel)
	if final.cancelled || !final.done {
		return history.Session{}, ErrCancelled
	}
	return final.selected, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/sessions/ -v`
Expected: all 8 tests PASS

- [ ] **Step 5: Run full build check**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./...`
Expected: compiles with zero errors

- [ ] **Step 6: Commit**

```bash
git add internal/sessions/picker.go internal/sessions/picker_test.go
git commit -m "feat(sessions): add bubbletea fallback picker when fzf unavailable"
```

---

### Task 4: Wire `--danger` flag into `runResume`

**Files:**
- Modify: `cmd/aimux/main.go`

- [ ] **Step 1: Update `runResume` to parse `--danger` / `-d`**

In `cmd/aimux/main.go`, replace the `runResume` function:

```go
func runResume(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: aimux resume <session-id> [--danger|-d]")
		os.Exit(1)
	}

	var sessionID string
	var danger bool
	for _, arg := range args {
		switch arg {
		case "--danger", "-d":
			danger = true
		default:
			if sessionID == "" {
				sessionID = arg
			}
		}
	}

	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "Usage: aimux resume <session-id> [--danger|-d]")
		os.Exit(1)
	}

	resumeSession(sessionID, danger)
}

// resumeSession runs `claude --resume <id>` with the given options.
// Shared by both `aimux resume` and `aimux sessions` (after picking).
func resumeSession(sessionID string, danger bool) {
	sessions, _ := history.Discover(history.DiscoverOpts{}, "")
	var workDir string
	for _, s := range sessions {
		if s.ID == sessionID {
			workDir = s.Project
			break
		}
	}

	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	cmdArgs := []string{"--resume", sessionID}
	if danger {
		cmdArgs = append(cmdArgs, "--dangerously-skip-permissions")
	}

	cmd := exec.Command(claudeBin, cmdArgs...) // #nosec G204
	if workDir != "" {
		if info, err := os.Stat(workDir); err == nil && info.IsDir() {
			cmd.Dir = workDir
		}
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Resume failed: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build and verify**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./...`
Expected: compiles with zero errors

- [ ] **Step 3: Commit**

```bash
git add cmd/aimux/main.go
git commit -m "feat(resume): add --danger flag for --dangerously-skip-permissions"
```

---

### Task 5: Wire search + picker into `runSessions`

**Files:**
- Modify: `cmd/aimux/main.go`

- [ ] **Step 1: Update `runSessions` to support query arg, `--danger`, and picker**

In `cmd/aimux/main.go`, add the `sessions` import and update `runSessions`:

Add to imports:
```go
"github.com/zanetworker/aimux/internal/sessions"
```

Replace the `runSessions` function:

```go
func runSessions(args []string) {
	appCfg, _ := config.Load(config.DefaultPath())

	var dir string
	var listMode, exportMode, jsonMode, generateTitles, regenerateTitles, danger bool
	var limit int
	var query string
	titleModel := appCfg.Sessions.TitleModel
	if titleModel == "" {
		titleModel = "flash"
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case "--list", "-l":
			listMode = true
		case "--export":
			exportMode = true
		case "--json":
			jsonMode = true
		case "--danger", "-d":
			danger = true
		case "--limit":
			if i+1 < len(args) {
				_, _ = fmt.Sscanf(args[i+1], "%d", &limit)
				i++
			}
		case "--generate-titles":
			generateTitles = true
		case "--regenerate-titles":
			generateTitles = true
			regenerateTitles = true
		case "--title-model":
			if i+1 < len(args) {
				titleModel = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") && query == "" {
				query = args[i]
			}
		}
	}

	opts := history.DiscoverOpts{Dir: dir, Limit: limit}
	allSessions, err := history.Discover(opts, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering sessions: %v\n", err)
		os.Exit(1)
	}

	// Filter out near-empty sessions
	var filtered []history.Session
	for _, s := range allSessions {
		if s.TurnCount <= 5 && s.CostUSD == 0 {
			continue
		}
		if s.LastActive.IsZero() {
			continue
		}
		filtered = append(filtered, s)
	}

	if generateTitles {
		cfg := history.TitleConfig{
			Enabled:    true,
			Model:      titleModel,
			APIKey:     appCfg.Sessions.APIKey,
			Regenerate: regenerateTitles,
		}
		fmt.Printf("Generating titles using %s...\n", titleModel)
		count, err := history.GenerateTitles(filtered, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Stopped after %d titles: %v\n", count, err)
		} else {
			fmt.Printf("Generated %d titles.\n", count)
		}
		allSessions, _ = history.Discover(opts, "")
		filtered = nil
		for _, s := range allSessions {
			if s.TurnCount <= 5 && s.CostUSD == 0 {
				continue
			}
			if s.LastActive.IsZero() {
				continue
			}
			filtered = append(filtered, s)
		}
	}

	if exportMode {
		printSessionsJSONL(filtered)
		return
	}

	if listMode {
		if jsonMode {
			printSessionsJSON(filtered)
		} else {
			printSessionsTable(filtered)
		}
		return
	}

	// Interactive mode: search + pick + resume
	candidates := filtered
	if query != "" {
		candidates = searchSessions(filtered, query)
		if len(candidates) == 0 {
			fmt.Fprintf(os.Stderr, "No sessions matching %q\n", query)
			os.Exit(1)
		}
	}

	selected, err := sessions.PickSession(candidates)
	if err != nil {
		if err == sessions.ErrCancelled {
			return
		}
		fmt.Fprintf(os.Stderr, "Picker error: %v\n", err)
		os.Exit(1)
	}

	resumeSession(selected.ID, danger)
}

// searchSessions filters sessions by query: metadata first, content fallback.
func searchSessions(sessions []history.Session, query string) []history.Session {
	// First pass: match on title/prompt metadata
	matched := history.FilterByPrompt(sessions, query)

	// If we got enough metadata matches, use those
	if len(matched) >= 3 {
		return matched
	}

	// Content fallback: search JSONL files via ripgrep
	contentMatches, err := history.SearchContent(query, "")
	if err != nil {
		return matched
	}

	// Build a set of already-matched IDs
	seen := make(map[string]bool)
	for _, s := range matched {
		seen[s.ID] = true
	}

	// Add content matches that aren't already in the results
	sessionByID := make(map[string]history.Session)
	for _, s := range sessions {
		sessionByID[s.ID] = s
	}
	for _, cm := range contentMatches {
		if seen[cm.SessionID] {
			continue
		}
		if s, ok := sessionByID[cm.SessionID]; ok {
			matched = append(matched, s)
			seen[cm.SessionID] = true
		}
	}

	return matched
}
```

- [ ] **Step 2: Update `printHelp` to document new flags**

In `cmd/aimux/main.go`, update the `printHelp` function:

```go
func printHelp() {
	fmt.Println(`aimux — AI agent multiplexer

Usage:
  aimux                    Launch the TUI dashboard
  aimux --web              Launch TUI + web dashboard
  aimux web                Launch web dashboard only (headless)
  aimux web --port 8080    Custom port (default: 3000)
  aimux sessions           Browse sessions (interactive fuzzy finder)
  aimux sessions <query>   Search sessions by title/prompt/content
  aimux sessions --list    List sessions as a table
  aimux sessions --export  Export sessions as JSONL
  aimux resume <id>        Resume a session by ID
  aimux --version          Show version

Sessions flags:
  --dir <path>            Scope to a specific directory
  --danger, -d            Resume with --dangerously-skip-permissions
  --list                  Plain table output (scriptable)
  --export                JSONL output for eval pipelines
  --json                  JSON output (with --list)
  --limit <n>             Max sessions to show (default: all)
  --generate-titles       Generate LLM titles for sessions without one
  --title-model <model>   Model for titles: haiku (default), sonnet, opus

Resume flags:
  --danger, -d            Resume with --dangerously-skip-permissions`)
}
```

- [ ] **Step 3: Build and verify**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./...`
Expected: compiles with zero errors

- [ ] **Step 4: Run the full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all packages PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/main.go
git commit -m "feat(sessions): wire fuzzy search + picker + --danger flag"
```

---

### Task 6: Verify full build + run vet

- [ ] **Step 1: Build**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./...`
Expected: zero errors

- [ ] **Step 2: Vet**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go vet ./...`
Expected: zero issues

- [ ] **Step 3: Full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: all PASS

- [ ] **Step 4: Manual smoke test**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go run ./cmd/aimux sessions --list | head -5`
Expected: prints session table (existing behavior preserved)

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go run ./cmd/aimux --help`
Expected: shows updated help text with `--danger` and search docs
