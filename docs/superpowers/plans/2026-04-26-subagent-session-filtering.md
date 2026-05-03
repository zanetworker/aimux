# Subagent Session Filtering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect agent-spawned subagent sessions during JSONL scanning and hide them by default in the Sessions view, with a toggle to show them.

**Architecture:** Add `IsSubagent` and `PermissionMode` fields to `history.Session`. Detection runs in `scanSession` by parsing `permissionMode` from JSONL entries and counting genuine human turns. The Sessions view filters subagent sessions by default with `H` key toggle and `[agent]` badge.

**Tech Stack:** Go, Bubble Tea, lipgloss

**Key discovery:** `G` is already used in sessions view for "go to bottom" (vim binding). Using `H` instead for "hide/show agent sessions".

---

### Task 1: Add subagent detection to history package

**Files:**
- Modify: `internal/history/history.go:20-40` (Session struct)
- Modify: `internal/history/history.go:208-224` (sessionEntry struct)
- Modify: `internal/history/history.go:153-206` (scanSession function)
- Modify: `internal/history/history.go:229-268` (parseSessionLine function)
- Test: `internal/history/history_test.go`

- [ ] **Step 1: Write failing tests for subagent detection**

Add to `internal/history/history_test.go`:

```go
func TestScanSession_SubagentDetection(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name           string
		lines          []map[string]interface{}
		wantSubagent   bool
		wantPermMode   string
	}{
		{
			name: "bypass_no_human_is_subagent",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "bypassPermissions",
					"message": map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "tool_result", "tool_use_id": "x", "content": "result"},
						},
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(10 * time.Second).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "Done."}},
					},
				},
			},
			wantSubagent: true,
			wantPermMode: "bypassPermissions",
		},
		{
			name: "bypass_with_human_not_subagent",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "bypassPermissions",
					"message": map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "text", "text": "fix the bug in main.go"},
						},
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(30 * time.Second).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "I'll fix that."}},
					},
				},
			},
			wantSubagent: false,
			wantPermMode: "bypassPermissions",
		},
		{
			name: "default_no_human_short_is_subagent",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "default",
					"message": map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "tool_result", "tool_use_id": "x", "content": "data"},
						},
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(2 * time.Minute).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "Analysis complete."}},
					},
				},
			},
			wantSubagent: true,
			wantPermMode: "default",
		},
		{
			name: "default_no_human_long_not_subagent",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "default",
					"message": map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "tool_result", "tool_use_id": "x", "content": "data"},
						},
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(10 * time.Minute).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "Done."}},
					},
				},
			},
			wantSubagent: false,
			wantPermMode: "default",
		},
		{
			name: "normal_interactive_session",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "default",
					"message": map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "text", "text": "add a new feature"},
						},
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(1 * time.Minute).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "Sure, let me implement that."}},
						"usage":   map[string]interface{}{"input_tokens": 1000, "output_tokens": 200},
					},
				},
				{
					"type":      "user",
					"timestamp": now.Add(5 * time.Minute).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "text", "text": "looks good, ship it"},
						},
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(6 * time.Minute).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "Done!"}},
						"usage":   map[string]interface{}{"input_tokens": 1500, "output_tokens": 100},
					},
				},
			},
			wantSubagent: false,
			wantPermMode: "default",
		},
		{
			name: "local_command_caveat_not_human",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "bypassPermissions",
					"message": map[string]interface{}{
						"role":    "user",
						"content": "<local-command-caveat>system message</local-command-caveat>",
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(10 * time.Second).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "OK."}},
					},
				},
			},
			wantSubagent: true,
			wantPermMode: "bypassPermissions",
		},
		{
			name: "command_name_not_human",
			lines: []map[string]interface{}{
				{
					"type":           "user",
					"timestamp":      now.Format(time.RFC3339),
					"permissionMode": "bypassPermissions",
					"message": map[string]interface{}{
						"role":    "user",
						"content": "<command-name>/plugin</command-name>\n<command-message>plugin</command-message>",
					},
				},
				{
					"type":      "assistant",
					"timestamp": now.Add(10 * time.Second).Format(time.RFC3339),
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": []map[string]interface{}{{"type": "text", "text": "OK."}},
					},
				},
			},
			wantSubagent: true,
			wantPermMode: "bypassPermissions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			projDir := filepath.Join(dir, "test-project")
			os.MkdirAll(projDir, 0o755)
			writeSessionJSONL(t, projDir, "test-session", tt.lines)

			s, err := scanSession("test-session", filepath.Join(projDir, "test-session.jsonl"), "/test")
			if err != nil {
				t.Fatalf("scanSession: %v", err)
			}
			if s.IsSubagent != tt.wantSubagent {
				t.Errorf("IsSubagent = %v, want %v", s.IsSubagent, tt.wantSubagent)
			}
			if s.PermissionMode != tt.wantPermMode {
				t.Errorf("PermissionMode = %q, want %q", s.PermissionMode, tt.wantPermMode)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/history/ -run TestScanSession_SubagentDetection -v`
Expected: FAIL (Session struct missing IsSubagent/PermissionMode fields)

- [ ] **Step 3: Add fields to Session struct**

In `internal/history/history.go`, add two fields to the `Session` struct after the `Tags` field (line 39):

```go
	Tags        []string  `json:"tags"`          // failure mode tags
	IsSubagent     bool   `json:"is_subagent"`
	PermissionMode string `json:"permission_mode"`
```

- [ ] **Step 4: Add permissionMode to sessionEntry and parseSessionLine**

In `internal/history/history.go`, add `PermissionMode` to the `sessionEntry` struct:

```go
type sessionEntry struct {
	Type           string    `json:"type"`
	Timestamp      time.Time `json:"timestamp"`
	GitBranch      string    `json:"gitBranch"`
	PermissionMode string    `json:"permissionMode"`
	Message        *struct {
		Model   string          `json:"model"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}
```

Change `parseSessionLine` signature to also return whether this entry has a human turn. Add a new function `isHumanTurn` and update `parseSessionLine`:

```go
func parseSessionLine(raw json.RawMessage, s *Session, extractPrompt bool) (model string, isHuman bool) {
	var entry sessionEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", false
	}

	if !entry.Timestamp.IsZero() {
		if s.StartTime.IsZero() || entry.Timestamp.Before(s.StartTime) {
			s.StartTime = entry.Timestamp
		}
		if entry.Timestamp.After(s.LastActive) {
			s.LastActive = entry.Timestamp
		}
	}

	if entry.PermissionMode != "" && s.PermissionMode == "" {
		s.PermissionMode = entry.PermissionMode
	}

	if entry.Message == nil {
		return "", false
	}

	if entry.Message.Model != "" {
		model = entry.Message.Model
	}

	if entry.Message.Usage != nil {
		s.TokensIn += entry.Message.Usage.InputTokens
		s.TokensOut += entry.Message.Usage.OutputTokens
		s.CacheReadTokens += entry.Message.Usage.CacheReadInputTokens
		s.CacheWriteTokens += entry.Message.Usage.CacheCreationInputTokens
	}

	if entry.Message.Role == "user" {
		isHuman = isHumanMessage(entry.Message.Content)
	}

	if extractPrompt && (s.FirstPrompt == "" || s.FirstPrompt == "(no prompt)") && entry.Message.Role == "user" {
		if text := extractUserText(entry.Message.Content); text != "" && text != "(no prompt)" {
			s.FirstPrompt = text
		}
	}

	return model, isHuman
}
```

Add the `isHumanMessage` function:

```go
func isHumanMessage(content json.RawMessage) bool {
	if content == nil {
		return false
	}

	// Check if content is a plain string
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		if strings.Contains(text, "<local-command-caveat>") {
			return false
		}
		if strings.Contains(text, "<command-name>") {
			return false
		}
		// Plain text string that isn't a system/command message counts as human
		return strings.TrimSpace(text) != ""
	}

	// Check if content is an array of blocks
	var blocks []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		ToolUseID string `json:"tool_use_id"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false
	}

	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			if strings.Contains(b.Text, "<local-command-caveat>") {
				return false
			}
			if strings.Contains(b.Text, "<command-name>") {
				return false
			}
			return true
		}
	}
	// All blocks are tool_result or non-text: not a human turn
	return false
}
```

- [ ] **Step 5: Update scanSession to use human turn count for detection**

In `scanSession`, update the scan loop to track human turns and apply detection:

```go
func scanSession(id, filePath, project string) (Session, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Session{}, fmt.Errorf("open session file %s: %w", filePath, err)
	}
	defer f.Close()

	s := Session{
		ID:        id,
		Provider:  "claude",
		Project:   project,
		FilePath:  filePath,
		Resumable: true,
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	lineCount := 0
	humanTurnCount := 0

	var model string
	for scanner.Scan() {
		lineCount++
		raw := make(json.RawMessage, len(scanner.Bytes()))
		copy(raw, scanner.Bytes())

		extractPrompt := lineCount <= 10
		m, isHuman := parseSessionLine(raw, &s, extractPrompt)
		if m != "" {
			model = m
		}
		if isHuman {
			humanTurnCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return Session{}, fmt.Errorf("scan session file %s: %w", filePath, err)
	}

	if model != "" {
		s.CostUSD = cost.Calculate(model, s.TokensIn, s.TokensOut, s.CacheReadTokens, s.CacheWriteTokens)
	}

	s.TurnCount = lineCount / 4
	if s.TurnCount < 1 && lineCount > 0 {
		s.TurnCount = 1
	}

	// Subagent detection
	if humanTurnCount == 0 {
		if s.PermissionMode == "bypassPermissions" {
			s.IsSubagent = true
		} else if !s.StartTime.IsZero() && !s.LastActive.IsZero() &&
			s.LastActive.Sub(s.StartTime) < 5*time.Minute {
			s.IsSubagent = true
		}
	}

	return s, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/history/ -v -timeout 30s`
Expected: ALL PASS

- [ ] **Step 7: Commit**

```bash
git add internal/history/history.go internal/history/history_test.go
git commit -m "feat(history): detect subagent sessions during JSONL scanning"
```

---

### Task 2: Add subagent filtering and toggle to Sessions view

**Files:**
- Modify: `internal/tui/views/sessions.go:135-178` (SessionsView struct)
- Modify: `internal/tui/views/sessions.go:258-403` (Update method)
- Modify: `internal/tui/views/sessions.go:668-714` (visibleSessions method)
- Modify: `internal/tui/views/sessions.go:1040-1159` (renderSessionRow method)
- Modify: `internal/tui/views/sessions.go:785-900` (View method, header area)
- Modify: `internal/tui/app.go:2196-2197` (hint bar for sessions view)
- Test: `internal/tui/views/sessions_test.go`

- [ ] **Step 1: Write failing tests**

Add to `internal/tui/views/sessions_test.go`:

```go
func testSessionsWithSubagent() []history.Session {
	now := time.Now()
	return []history.Session{
		{
			ID:          "abc-123",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "fix the bug",
			LastActive:  now.Add(-2 * time.Hour),
			TurnCount:   16,
			CostUSD:     0.42,
			Resumable:   true,
		},
		{
			ID:          "sub-001",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "YOU ARE A SESSION ANALYZER",
			LastActive:  now.Add(-3 * time.Hour),
			TurnCount:   6,
			CostUSD:     0.05,
			Resumable:   true,
			IsSubagent:  true,
		},
		{
			ID:          "sub-002",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "Evaluate session abc-123",
			LastActive:  now.Add(-4 * time.Hour),
			TurnCount:   8,
			CostUSD:     0.03,
			Resumable:   true,
			IsSubagent:  true,
		},
		{
			ID:          "def-456",
			Provider:    "claude",
			Project:     "/test",
			FirstPrompt: "add table support",
			LastActive:  now.Add(-5 * time.Hour),
			TurnCount:   8,
			CostUSD:     0.18,
			Resumable:   true,
		},
	}
}

func TestSessionsView_SubagentHiddenByDefault(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	visible := v.visibleSessions()
	for _, s := range visible {
		if s.IsSubagent {
			t.Errorf("subagent session %q should be hidden by default", s.ID)
		}
	}
	if len(visible) != 2 {
		t.Errorf("expected 2 visible sessions, got %d", len(visible))
	}
}

func TestSessionsView_SubagentToggle(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	// Default: subagents hidden
	visible := v.visibleSessions()
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible before toggle, got %d", len(visible))
	}

	// Toggle on: show subagents
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	visible = v.visibleSessions()
	if len(visible) != 4 {
		t.Errorf("expected 4 visible after toggle on, got %d", len(visible))
	}

	// Toggle off: hide again
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	visible = v.visibleSessions()
	if len(visible) != 2 {
		t.Errorf("expected 2 visible after toggle off, got %d", len(visible))
	}
}

func TestSessionsView_SubagentBadge(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	// Show subagents
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})

	output := v.View()
	if !strings.Contains(output, "[agent]") {
		t.Error("expected [agent] badge in rendered output for subagent sessions")
	}
}

func TestSessionsView_SubagentCount(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	output := v.View()
	if !strings.Contains(output, "+2 agent") {
		t.Errorf("expected hidden agent count in output, got:\n%s", output)
	}
}

func TestSessionsView_SubagentVisibleDuringSearch(t *testing.T) {
	v := NewSessionsView()
	v.SetSessions(testSessionsWithSubagent())
	v.SetSize(120, 40)

	// Enter filter mode and type "SESSION ANALYZER"
	v.filterMode = true
	v.filterText = "session analyzer"

	visible := v.visibleSessions()
	found := false
	for _, s := range visible {
		if s.ID == "sub-001" {
			found = true
		}
	}
	if !found {
		t.Error("subagent session should be visible when filter matches it")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/tui/views/ -run TestSessionsView_Subagent -v`
Expected: FAIL (showSubagents field missing, no filtering logic)

- [ ] **Step 3: Add showSubagents field to SessionsView**

In `internal/tui/views/sessions.go`, add to the `SessionsView` struct after `contentSearchIDs`:

```go
	// Subagent filtering
	showSubagents bool // false = hide subagent sessions (default)
```

- [ ] **Step 4: Add subagent filtering to visibleSessions()**

In `internal/tui/views/sessions.go`, in `visibleSessions()`, add the subagent filter right after the opening `for` loop line (`for _, s := range v.sessions {`), before the existing `// Hide near-empty sessions` check:

```go
		// Hide subagent sessions unless toggled on or actively searching
		if !v.showSubagents && !isSearching && s.IsSubagent {
			continue
		}
```

- [ ] **Step 5: Add `H` key handler in Update()**

In `internal/tui/views/sessions.go`, in the `Update()` method, add a case for `"H"` in the main `switch msg.String()` block (after the `"F"` case at line 397):

```go
		case "H":
			v.showSubagents = !v.showSubagents
			v.cursor = 0
			v.previewLogs = nil
```

- [ ] **Step 6: Add `[agent]` badge to renderSessionRow()**

In `internal/tui/views/sessions.go`, in `renderSessionRow()`, after the prefixes are built (after `if len(s.Tags) > 0 {` block, around line 1073), add:

```go
	if s.IsSubagent {
		prefixes = append([]string{"agent"}, prefixes...)
	}
```

- [ ] **Step 7: Add hidden subagent count to View() header**

In `internal/tui/views/sessions.go`, in `View()`, after the line that builds `countStr` (around line 803), add:

```go
	if !v.showSubagents {
		hiddenCount := 0
		for _, s := range v.sessions {
			if s.IsSubagent {
				hiddenCount++
			}
		}
		if hiddenCount > 0 {
			countStr += sessDimStyle.Render(fmt.Sprintf("  (+%d agent)", hiddenCount))
		}
	}
```

- [ ] **Step 8: Add SubagentCount() method for hint bar**

In `internal/tui/views/sessions.go`, add a public method:

```go
// SubagentCount returns the number of subagent sessions (hidden or visible).
func (v *SessionsView) SubagentCount() int {
	count := 0
	for _, s := range v.sessions {
		if s.IsSubagent {
			count++
		}
	}
	return count
}

// ShowSubagents returns whether subagent sessions are currently shown.
func (v *SessionsView) ShowSubagents() bool {
	return v.showSubagents
}
```

- [ ] **Step 9: Update hint bar in app.go**

In `internal/tui/app.go`, replace the sessions hint (line 2197):

```go
	case viewSessions:
		hint := "j/k:nav  Enter:resume  C:copy-id  F:find-content  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  d:delete  D:cleanup  p:preview"
		if a.sessionsView.ShowSubagents() {
			hint += "  H:hide-agents"
		} else {
			hint += "  H:show-agents"
		}
		hint += "  Esc:back"
		a.headerView.SetHint(hint)
```

- [ ] **Step 10: Run all tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/tui/views/ -run TestSessionsView_Subagent -v`
Expected: ALL PASS

Then run the full suite:

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 11: Build and verify**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./...`
Expected: compiles with zero errors

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go vet ./...`
Expected: zero issues

- [ ] **Step 12: Commit**

```bash
git add internal/tui/views/sessions.go internal/tui/views/sessions_test.go internal/tui/app.go
git commit -m "feat(sessions): hide subagent sessions by default with H toggle"
```
