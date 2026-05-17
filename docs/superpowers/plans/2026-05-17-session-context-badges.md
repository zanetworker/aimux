# Session Context Badges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add git branch, last prompt, last action, and model as inline context badges to session rows across TUI and web.

**Architecture:** Extend `history.Session` with four new fields populated during the existing JSONL scan loop (no extra I/O). Duplicate `extractLastToolAction` from `discovery/session.go` into `history/` rather than creating a shared package. Render new fields as inline badges in TUI sessions list and agents table, and expose via web API.

**Tech Stack:** Go (core), Bubble Tea/lipgloss (TUI), React/TypeScript (web)

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/history/history.go` | Modify | Add 4 fields to `Session`, extend `parseSessionLine`, add `extractLastToolAction` |
| `internal/history/history_test.go` | Modify | Test new field extraction from JSONL fixtures |
| `internal/frontend/tui/views/sessions.go` | Modify | Add branch/action columns, update title preference, adjust column widths |
| `internal/frontend/tui/views/sessions_test.go` | Modify | Test rendering with branch/action data |
| `internal/frontend/tui/views/agents.go` | Modify | Add branch badge to agents table rows |
| `internal/frontend/web/handlers.go` | Modify | Expose new fields in `/api/history` response |
| `internal/frontend/web/handlers_test.go` | Modify | Test new fields in API response |
| `web/src/components/SessionsTable.tsx` | Modify | Add `HistorySession` fields, render branch/action badges |

---

### Task 1: Add fields to Session struct and extract during scan

**Files:**
- Modify: `internal/history/history.go:20-43` (Session struct)
- Modify: `internal/history/history.go:160-225` (scanSession)
- Modify: `internal/history/history.go:289-341` (parseSessionLine)
- Modify: `internal/history/history_test.go`

- [ ] **Step 1: Write test for git branch extraction**

Add to `internal/history/history_test.go`:

```go
func TestParseSessionLine_ExtractsGitBranch(t *testing.T) {
	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	lines := []map[string]interface{}{
		{
			"type":      "human",
			"timestamp": ts.Format(time.RFC3339),
			"gitBranch": "feat/resize-handle",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "fix the bug"},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": ts.Add(30 * time.Second).Format(time.RFC3339),
			"gitBranch": "feat/resize-handle",
			"message": map[string]interface{}{
				"role":  "assistant",
				"model": "claude-sonnet-4-6",
				"content": []map[string]interface{}{
					{"type": "text", "text": "I'll fix that."},
				},
				"usage": map[string]interface{}{
					"input_tokens":  1000,
					"output_tokens": 200,
				},
			},
		},
	}
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-proj")
	_ = os.MkdirAll(projDir, 0o750)
	writeSessionJSONL(t, projDir, "branch-test", lines)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].GitBranch != "feat/resize-handle" {
		t.Errorf("GitBranch = %q, want %q", sessions[0].GitBranch, "feat/resize-handle")
	}
	if sessions[0].Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want %q", sessions[0].Model, "claude-sonnet-4-6")
	}
}
```

- [ ] **Step 2: Write test for last prompt extraction**

Add to `internal/history/history_test.go`:

```go
func TestParseSessionLine_ExtractsLastPrompt(t *testing.T) {
	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	lines := []map[string]interface{}{
		{
			"type":      "human",
			"timestamp": ts.Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "fix the markdown rendering"},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": ts.Add(30 * time.Second).Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Done."},
				},
				"usage": map[string]interface{}{
					"input_tokens":  1000,
					"output_tokens": 200,
				},
			},
		},
		{
			"type":      "human",
			"timestamp": ts.Add(60 * time.Second).Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "now add table support"},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": ts.Add(90 * time.Second).Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Sure."},
				},
				"usage": map[string]interface{}{
					"input_tokens":  1200,
					"output_tokens": 400,
				},
			},
		},
	}
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-proj")
	_ = os.MkdirAll(projDir, 0o750)
	writeSessionJSONL(t, projDir, "lastprompt-test", lines)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].FirstPrompt != "fix the markdown rendering" {
		t.Errorf("FirstPrompt = %q, want %q", sessions[0].FirstPrompt, "fix the markdown rendering")
	}
	if sessions[0].LastPrompt != "now add table support" {
		t.Errorf("LastPrompt = %q, want %q", sessions[0].LastPrompt, "now add table support")
	}
}
```

- [ ] **Step 3: Write test for last action extraction**

Add to `internal/history/history_test.go`:

```go
func TestParseSessionLine_ExtractsLastAction(t *testing.T) {
	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	lines := []map[string]interface{}{
		{
			"type":      "human",
			"timestamp": ts.Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "fix the bug"},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": ts.Add(30 * time.Second).Format(time.RFC3339),
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "I'll edit the file."},
					{
						"type": "tool_use",
						"name": "Read",
						"input": map[string]interface{}{
							"file_path": "/Users/test/main.go",
						},
					},
					{
						"type": "tool_use",
						"name": "Edit",
						"input": map[string]interface{}{
							"file_path": "/Users/test/config.go",
						},
					},
				},
				"usage": map[string]interface{}{
					"input_tokens":  1500,
					"output_tokens": 300,
				},
			},
		},
	}
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-proj")
	_ = os.MkdirAll(projDir, 0o750)
	writeSessionJSONL(t, projDir, "action-test", lines)

	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].LastAction != "Ed config.go" {
		t.Errorf("LastAction = %q, want %q", sessions[0].LastAction, "Ed config.go")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/history/ -timeout 30s -run "TestParseSessionLine_Extracts"`
Expected: FAIL — `Session` struct doesn't have `GitBranch`, `LastPrompt`, `LastAction`, or `Model` fields yet.

- [ ] **Step 5: Add fields to Session struct**

In `internal/history/history.go`, add four fields after the existing `Starred` field (line 42):

```go
	Starred        bool   `json:"starred"`
	GitBranch      string `json:"git_branch"`
	LastPrompt     string `json:"last_prompt"`
	LastAction     string `json:"last_action"`
	Model          string `json:"model"`
```

- [ ] **Step 6: Add extractLastToolAction and helpers to history package**

Add to `internal/history/history.go`, after the `extractUserText` function (after the `cleanPrompt` function ends, around line 440):

```go
// extractLastToolAction parses the content array of an assistant message to find
// the last tool_use block and return a short summary like "Ed main.go".
func extractLastToolAction(content json.RawMessage) string {
	if content == nil {
		return ""
	}
	var blocks []struct {
		Type  string                 `json:"type"`
		Name  string                 `json:"name"`
		Input map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}

	var lastTool string
	var lastInput map[string]interface{}
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name != "" {
			lastTool = b.Name
			lastInput = b.Input
		}
	}
	if lastTool == "" {
		return ""
	}

	short := shortToolLabel(lastTool)
	snippet := toolSnippetForAction(lastTool, lastInput)
	if snippet != "" {
		return short + " " + snippet
	}
	return short
}

func shortToolLabel(name string) string {
	switch name {
	case "Read":
		return "Rd"
	case "Write":
		return "Wr"
	case "Edit":
		return "Ed"
	case "Bash":
		return "Sh"
	case "Grep":
		return "Gr"
	case "Glob":
		return "Gl"
	case "Task":
		return "Tk"
	default:
		if len(name) > 3 {
			return name[:3]
		}
		return name
	}
}

func toolSnippetForAction(name string, input map[string]interface{}) string {
	if input == nil {
		return ""
	}
	switch name {
	case "Read", "Write", "Edit":
		if path, ok := input["file_path"].(string); ok {
			return filepath.Base(path)
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 20 {
				cmd = cmd[:17] + "..."
			}
			return cmd
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			if len(p) > 15 {
				p = p[:12] + "..."
			}
			return "/" + p + "/"
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	}
	return ""
}
```

Note: These are duplicated from `internal/discovery/session.go`. Both call sites parse different structures, so keeping them independent avoids coupling. The `filepath` import is already present in `history.go`.

- [ ] **Step 7: Extend parseSessionLine to track new fields**

In `internal/history/history.go`, modify `parseSessionLine` (line 292). Add git branch tracking after the permission mode block (line 309), and add last prompt + last action tracking after the existing first prompt extraction (line 338):

After the permission mode block (line 309), add:
```go
	// Track git branch (last one wins)
	if entry.GitBranch != "" {
		s.GitBranch = entry.GitBranch
	}
```

After the existing first prompt extraction block (line 338), add:
```go
	// Track last user prompt (always overwrite — last one wins)
	if entry.Message.Role == "user" {
		if text := extractUserText(entry.Message.Content); text != "" && text != "(no prompt)" {
			s.LastPrompt = text
		}
	}

	// Track last tool action from assistant messages
	if entry.Message.Role == "assistant" {
		if action := extractLastToolAction(entry.Message.Content); action != "" {
			s.LastAction = action
		}
	}
```

- [ ] **Step 8: Store model on Session in scanSession**

In `internal/history/history.go`, in `scanSession`, after the cost calculation block (line 215), add:

```go
	s.Model = model
```

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/history/ -timeout 30s -run "TestParseSessionLine_Extracts"`
Expected: PASS — all three new tests pass.

- [ ] **Step 10: Run full history package tests**

Run: `go test ./internal/history/ -timeout 30s`
Expected: PASS — no regressions.

- [ ] **Step 11: Commit**

```bash
git add internal/history/history.go internal/history/history_test.go
git commit -m "feat: extract git branch, last prompt, last action, model from session JSONL"
```

---

### Task 2: Add branch and action columns to TUI sessions list

**Files:**
- Modify: `internal/frontend/tui/views/sessions.go:28-63` (styles)
- Modify: `internal/frontend/tui/views/sessions.go:1118-1147` (colLayout, columnWidths)
- Modify: `internal/frontend/tui/views/sessions.go:1149-1277` (renderSessionRow)
- Modify: `internal/frontend/tui/views/sessions.go:930-965` (column headers)
- Modify: `internal/frontend/tui/views/sessions_test.go`

- [ ] **Step 1: Write test for branch badge rendering**

Add to `internal/frontend/tui/views/sessions_test.go`:

```go
func TestSessionsView_BranchBadge(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(120, 30)
	sessions := testSessions()
	sessions[0].GitBranch = "feat/resize-handle"
	sessions[1].GitBranch = "main"
	v.SetSessions(sessions)

	output := v.View()
	if !strings.Contains(output, "feat/resize-h") {
		t.Error("expected branch badge 'feat/resize-h' in output")
	}
	// "main" should still appear but dimmed (we can't test color, but it should be present)
	if !strings.Contains(output, "main") {
		t.Error("expected 'main' branch in output")
	}
}
```

- [ ] **Step 2: Write test for last action rendering**

Add to `internal/frontend/tui/views/sessions_test.go`:

```go
func TestSessionsView_LastAction(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(140, 30)
	sessions := testSessions()
	sessions[0].LastAction = "Ed config.go"
	v.SetSessions(sessions)

	output := v.View()
	if !strings.Contains(output, "Ed config.go") {
		t.Error("expected last action 'Ed config.go' in output")
	}
}
```

- [ ] **Step 3: Write test for title preference (last prompt over first)**

Add to `internal/frontend/tui/views/sessions_test.go`:

```go
func TestSessionsView_TitlePrefersLastPrompt(t *testing.T) {
	v := NewSessionsView()
	v.SetSize(120, 30)
	sessions := []history.Session{
		{
			ID:          "test-1",
			Provider:    "claude",
			FirstPrompt: "initial question",
			LastPrompt:  "final refined question",
			LastActive:  time.Now().Add(-1 * time.Hour),
			TurnCount:   10,
			CostUSD:     0.50,
			Resumable:   true,
		},
	}
	v.SetSessions(sessions)

	output := v.View()
	if !strings.Contains(output, "final refined") {
		t.Errorf("expected last prompt in output, got:\n%s", output)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/frontend/tui/views/ -timeout 30s -run "TestSessionsView_Branch|TestSessionsView_LastAction|TestSessionsView_TitlePrefers"`
Expected: FAIL — `GitBranch`, `LastPrompt`, `LastAction` fields exist on `Session` (from Task 1) but aren't rendered yet.

- [ ] **Step 5: Add styles for branch badge**

In `internal/frontend/tui/views/sessions.go`, add to the styles block (around line 45):

```go
	sessBranchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A78BFA"))
	sessBranchDimStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280"))
	sessActionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
```

- [ ] **Step 6: Update colLayout and columnWidths**

Replace the `colLayout` struct and `columnWidths` function in `internal/frontend/tui/views/sessions.go` (lines 1118-1147):

```go
type colLayout struct {
	age     int
	branch  int
	project int
	prompt  int
	action  int
	turns   int
	cost    int
}

func (v *SessionsView) columnWidths(w int) colLayout {
	c := colLayout{
		age:    9,
		branch: 16,
		action: 20,
		turns:  6,
		cost:   8,
	}
	if v.showAll {
		c.project = 14
	}
	// marker(3) + spacing between columns (3 per gap)
	gaps := 5 * 3 // age, branch, prompt, turns, cost = 5 gaps
	if v.showAll {
		gaps += 3 // project gap
	}
	if c.action > 0 {
		gaps += 3 // action gap
	}
	fixed := 3 + c.age + c.branch + c.action + c.turns + c.cost + gaps
	if v.showAll {
		fixed += c.project
	}
	c.prompt = w - fixed
	if c.prompt < 15 {
		c.prompt = 15
	}
	return c
}
```

- [ ] **Step 7: Update column headers**

In `internal/frontend/tui/views/sessions.go`, in the `View()` method header section (around line 952-965), add the BRANCH header after AGE and add ACTION header before TURNS:

After the AGE header line, add:
```go
	headerParts = append(headerParts, "   ")
	headerParts = append(headerParts, fmt.Sprintf("%-*s", cols.branch, "BRANCH"))
```

Before the TURNS header line, add:
```go
	headerParts = append(headerParts, "   ")
	headerParts = append(headerParts, fmt.Sprintf("%-*s", cols.action, "LAST ACTION"))
```

- [ ] **Step 8: Update renderSessionRow with branch badge**

In `internal/frontend/tui/views/sessions.go`, in `renderSessionRow` (line 1150), add the branch column after the age column rendering (after line 1205, after `b.WriteString("   ")`):

```go
	// Branch column
	branch := s.GitBranch
	if branch == "" {
		branch = ""
	}
	branchStr := fmt.Sprintf("%-*s", cols.branch, truncate(branch, cols.branch))
	if branch == "" {
		b.WriteString(sessDimStyle.Render(branchStr))
	} else if branch == "main" || branch == "master" {
		b.WriteString(sessBranchDimStyle.Render(branchStr))
	} else {
		b.WriteString(sessBranchStyle.Render(branchStr))
	}
	b.WriteString("   ")
```

- [ ] **Step 9: Update title preference logic**

In `internal/frontend/tui/views/sessions.go`, in `renderSessionRow`, replace the title selection block (lines 1166-1174):

```go
	// Use LLM-generated title if available, fall back to last prompt, then first prompt
	prompt := s.Title
	if prompt == "" {
		prompt = s.LastPrompt
	}
	if prompt == "" {
		prompt = s.FirstPrompt
	}
	if prompt == "" {
		prompt = "(no prompt)"
	}
	prompt = strings.TrimLeft(prompt, "# ")
```

- [ ] **Step 10: Add last action column rendering**

In `internal/frontend/tui/views/sessions.go`, in `renderSessionRow`, add the action column after the prompt column (after line 1238, after `b.WriteString("   ")`):

```go
	// Last action column
	actionStr := fmt.Sprintf("%-*s", cols.action, truncate(s.LastAction, cols.action))
	if s.LastAction == "" || isEmpty {
		b.WriteString(sessDimStyle.Render(actionStr))
	} else {
		b.WriteString(sessActionStyle.Render(actionStr))
	}
	b.WriteString("   ")
```

- [ ] **Step 11: Update search matching to include LastPrompt**

In `internal/frontend/tui/views/sessions.go`, in the `matchesFilter` function (around line 815), add `LastPrompt` to the search targets:

After the `FirstPrompt` check, add:
```go
	if strings.Contains(strings.ToLower(s.LastPrompt), needle) {
		return true
	}
```

- [ ] **Step 12: Update the sort-by-title comparison to include LastPrompt**

In `internal/frontend/tui/views/sessions.go`, in `sortKey` function (around line 710), update the title sort to use the same preference chain:

```go
func (s history.Session) sortKey() string {
	if s.Title != "" {
		return strings.ToLower(s.Title)
	}
	if s.LastPrompt != "" {
		return strings.ToLower(s.LastPrompt)
	}
	return strings.ToLower(s.FirstPrompt)
}
```

Note: Check the exact function name. The existing code at line 713 is `return strings.ToLower(s.FirstPrompt)` — this is the body of a sort comparison. Find and update the actual comparison.

- [ ] **Step 13: Run tests to verify they pass**

Run: `go test ./internal/frontend/tui/views/ -timeout 30s -run "TestSessionsView_Branch|TestSessionsView_LastAction|TestSessionsView_TitlePrefers"`
Expected: PASS

- [ ] **Step 14: Run full views test suite**

Run: `go test ./internal/frontend/tui/views/ -timeout 30s`
Expected: PASS — no regressions in existing tests. Some tests that check column widths (`TestSessionsView_ColumnWidths`) may need adjustment since the total fixed width changed.

- [ ] **Step 15: Build and verify**

Run: `go build ./...`
Expected: Compiles cleanly.

- [ ] **Step 16: Commit**

```bash
git add internal/frontend/tui/views/sessions.go internal/frontend/tui/views/sessions_test.go
git commit -m "feat: add branch badge and last action to TUI sessions list"
```

---

### Task 3: Add branch badge to TUI agents table

**Files:**
- Modify: `internal/frontend/tui/views/agents.go:15-22` (column constants)
- Modify: `internal/frontend/tui/views/agents.go:325-332` (header)
- Modify: `internal/frontend/tui/views/agents.go:383-425` (renderParentRow)

- [ ] **Step 1: Write test for branch in agents table**

Check if `internal/frontend/tui/views/views_test.go` has agent rendering tests. If so, add there. Otherwise add to a new test in the same file or `agents_test.go`:

```go
func TestAgentsView_BranchInRow(t *testing.T) {
	// Verify that renderParentRow includes the git branch
	v := NewAgentsView()
	v.SetSize(140, 30)
	a := agent.Agent{
		PID:          12345,
		Name:         "test-agent",
		ProviderName: "claude",
		Model:        "claude-sonnet-4-6",
		GitBranch:    "feat/auth",
		Status:       agent.StatusIdle,
	}
	v.SetAgents([]agent.Agent{a})
	output := v.View()
	if !strings.Contains(output, "feat/auth") {
		t.Errorf("expected 'feat/auth' branch in agents view output")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/frontend/tui/views/ -timeout 30s -run "TestAgentsView_BranchInRow"`
Expected: FAIL — branch not rendered in agents table yet.

- [ ] **Step 3: Add branch column to agents table**

In `internal/frontend/tui/views/agents.go`, add a branch column constant (line 22):

```go
	colBranch = 14
```

Reduce `colDir` from 12 to 10 to reclaim space:
```go
	colDir   = 10
```

- [ ] **Step 4: Update agents table header**

In `internal/frontend/tui/views/agents.go`, add BRANCH to the header row (around line 325). Insert after the DIR column:

```go
	header := " " + padRight(nameHeader, colName) + " " +
		padRight("AGENT", colAgent) + " " +
		padRight(modelHeader, colModel) + " " +
		padRight("LOC", colLoc) + " " +
		padRight("DIR", colDir) + " " +
		padRight("BRANCH", colBranch) + " " +
		padRight("LAST", colLast) + " " +
		padRight(ageHeader, colAge) + " " +
		padRight(costHeader, colCostA)
```

- [ ] **Step 5: Update renderParentRow with branch**

In `internal/frontend/tui/views/agents.go`, in `renderParentRow` (line 409), insert the branch column after DIR:

```go
	branchDisplay := a.GitBranch
	if branchDisplay == "" {
		branchDisplay = ""
	}
	branchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	if branchDisplay == "main" || branchDisplay == "master" {
		branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	}

	row := " " + padRight(nameCol, colName) + " " +
		padRight(truncate(a.ProviderName, colAgent), colAgent) + " " +
		padRight(truncate(a.ShortModel(), colModel), colModel) + " " +
		padRight(truncate(loc, colLoc), colLoc) + " " +
		padRight(truncate(a.ShortDir(), colDir), colDir) + " " +
		padRight(branchStyle.Render(truncate(branchDisplay, colBranch)), colBranch) + " " +
		padRight(truncate(a.LastAction, colLast), colLast) + " " +
		padRight(a.FormatAge(), colAge) + " " +
		padRight(costRendered, colCostA)
```

Note: `padRight` with styled content may miscount width since ANSI codes add invisible characters. Check if the existing `padRight` function handles styled strings. If it uses `lipgloss.Width()` it works; if it uses `len()` it doesn't. If needed, render the truncated text without style first for width calculation, then apply style.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/frontend/tui/views/ -timeout 30s -run "TestAgentsView_BranchInRow"`
Expected: PASS

- [ ] **Step 7: Run full test suite**

Run: `go test ./internal/frontend/tui/views/ -timeout 30s`
Expected: PASS

- [ ] **Step 8: Build**

Run: `go build ./...`
Expected: Compiles cleanly.

- [ ] **Step 9: Commit**

```bash
git add internal/frontend/tui/views/agents.go
git commit -m "feat: add branch column to TUI agents table"
```

---

### Task 4: Expose new fields in web API

**Files:**
- Modify: `internal/frontend/web/handlers.go:245-267`
- Modify: `internal/frontend/web/handlers_test.go`
- Modify: `web/src/components/SessionsTable.tsx:3-23` (HistorySession interface)
- Modify: `web/src/components/SessionsTable.tsx:442-566` (table row rendering)

- [ ] **Step 1: Write test for new API fields**

Add to `internal/frontend/web/handlers_test.go` (find the existing history handler test and extend it, or add a new one):

```go
func TestHistoryHandler_NewFields(t *testing.T) {
	// This test verifies the /api/history response includes the new context fields.
	// The exact structure depends on how the test harness creates sessions.
	// At minimum, verify the JSON keys exist in the response shape.
	// ... (adapt to existing test patterns in this file)
}
```

Note: Check the existing test patterns in `handlers_test.go` to follow the same mock/setup approach. The key assertion is that the JSON response contains `git_branch`, `last_prompt`, `last_action`, and `model` keys.

- [ ] **Step 2: Add fields to web API response**

In `internal/frontend/web/handlers.go`, in the history handler (line 246-266), add the four new fields to the session map:

After `"starred": s.Starred,` (line 265), add:
```go
				"gitBranch":  s.GitBranch,
				"lastPrompt": s.LastPrompt,
				"lastAction": s.LastAction,
				"model":      s.Model,
```

- [ ] **Step 3: Run backend tests**

Run: `go test ./internal/frontend/web/ -timeout 30s`
Expected: PASS

- [ ] **Step 4: Add fields to HistorySession TypeScript interface**

In `web/src/components/SessionsTable.tsx`, add to the `HistorySession` interface (after line 22):

```typescript
export interface HistorySession {
  id: string;
  provider: string;
  project: string;
  filePath: string;
  startTime: string;
  lastActive: string;
  turnCount: number;
  tokensIn: number;
  tokensOut: number;
  costUSD: number;
  firstPrompt: string;
  title: string;
  resumable: boolean;
  annotation: string;
  tags: string[];
  note: string;
  isSubagent: boolean;
  permissionMode: string;
  starred: boolean;
  gitBranch?: string;
  lastPrompt?: string;
  lastAction?: string;
  model?: string;
}
```

- [ ] **Step 5: Add branch and action badges to table rows**

In `web/src/components/SessionsTable.tsx`, in the table row rendering (around line 466, the Title `<td>`), add a branch badge and last action. Replace the title cell content:

```tsx
<td style={{ padding: '8px 10px' }}>
  <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
      {s.annotation && (
        <span style={{
          fontSize: 8, fontWeight: 700, textTransform: 'uppercase',
          padding: '1px 4px', borderRadius: 2,
          color: annotationColor(s.annotation),
          border: `1px solid ${annotationColor(s.annotation)}`,
        }}>
          {s.annotation}
        </span>
      )}
      {s.tags?.map(t => (
        <span key={t} style={{
          fontSize: 8, padding: '1px 4px', borderRadius: 2,
          background: 'var(--accent-dim)', color: 'var(--accent)',
        }}>
          {t}
        </span>
      ))}
      {s.gitBranch && (
        <span style={{
          fontFamily: 'var(--mono)', fontSize: 9, padding: '1px 5px',
          borderRadius: 2, background: 'var(--bg-3)',
          color: (s.gitBranch === 'main' || s.gitBranch === 'master')
            ? 'var(--fg-4)' : 'var(--purple)',
        }}>
          {s.gitBranch.length > 18 ? s.gitBranch.slice(0, 16) + '…' : s.gitBranch}
        </span>
      )}
      <span style={{
        color: 'var(--fg)', fontSize: 11,
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }}>
        {s.title || s.lastPrompt || s.firstPrompt || '(no prompt)'}
      </span>
      {s.isSubagent && (
        <span style={{ fontSize: 8, color: 'var(--fg-4)', fontStyle: 'italic' }}>agent</span>
      )}
      {s.lastAction && (
        <span style={{
          fontFamily: 'var(--mono)', fontSize: 9, color: 'var(--fg-4)',
          marginLeft: 'auto', flexShrink: 0,
        }}>
          {s.lastAction.length > 22 ? s.lastAction.slice(0, 20) + '…' : s.lastAction}
        </span>
      )}
    </div>
    {snippet && (
      <span style={{
        fontSize: 10, fontFamily: 'var(--mono)', color: 'var(--purple)',
        fontStyle: 'italic', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }}>
        {snippet}
      </span>
    )}
  </div>
</td>
```

- [ ] **Step 6: Update title preference in sort and filter**

In `web/src/components/SessionsTable.tsx`, update the sort comparison (line 243) and the title variable (line 445) to use the new preference chain:

Line 243:
```typescript
case 'title': cmp = (a.title || a.lastPrompt || a.firstPrompt || '').localeCompare(b.title || b.lastPrompt || b.firstPrompt || ''); break;
```

Line 445 (already handled in Step 5 rendering).

Also update the filter (line 212) to search `lastPrompt`:
```typescript
(s.lastPrompt || '').toLowerCase().includes(q) ||
```

- [ ] **Step 7: Build web frontend**

Run: `cd web && npm run build`
Expected: Compiles cleanly.

- [ ] **Step 8: Build Go backend**

Run: `go build ./...`
Expected: Compiles cleanly.

- [ ] **Step 9: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/handlers_test.go web/src/components/SessionsTable.tsx
git commit -m "feat: expose branch, last action, model in web sessions list"
```

---

### Task 5: Full integration test and cleanup

**Files:**
- All modified files from Tasks 1-4

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: No issues.

- [ ] **Step 3: Build web frontend**

Run: `cd web && npm run build`
Expected: Clean build.

- [ ] **Step 4: Build binary**

Run: `go build -o aimux ./cmd/aimux`
Expected: Binary builds successfully.

- [ ] **Step 5: Visual smoke test**

Launch `./aimux` and verify:
- Sessions list shows branch badges in purple for feature branches, dim for main
- Sessions list shows last action in dim text between title and turns
- Title column shows last prompt when no LLM title exists
- Empty branch/action fields don't leave gaps
- Agents table shows branch column
- Verify at terminal widths 80, 120, 200 that columns don't overflow

- [ ] **Step 6: Final commit if any fixups needed**

```bash
git add -A
git commit -m "fix: adjust column widths and rendering for context badges"
```
