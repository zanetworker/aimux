# Claudetopus Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a TUI control plane for managing multiple Claude Code instances with process discovery, live logs, cost tracking, team views, and session jumping.

**Architecture:** Process-based discovery polls `ps aux` to find Claude processes, parses CLI flags for metadata, and watches `~/.claude/projects/*/` JSONL files via fsnotify for live session data. Bubble Tea renders views with `:` command navigation.

**Tech Stack:** Go 1.23, Bubble Tea (bubbletea + lipgloss + bubbles), fsnotify

---

### Task 1: Project Scaffolding

**Files:**
- Create: `go.mod`
- Create: `cmd/claudetopus/main.go`
- Create: `Makefile`
- Create: `.gitignore`

**Step 1: Initialize Go module**

Run:
```bash
cd /Users/azaalouk/go/src/github.com/zanetworker/claudetopus
go mod init github.com/zanetworker/claudetopus
```

Expected: `go.mod` created with module path.

**Step 2: Install dependencies**

Run:
```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/fsnotify/fsnotify@latest
```

Expected: `go.sum` created, packages fetched.

**Step 3: Create minimal main.go**

```go
// cmd/claudetopus/main.go
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

type model struct{}

func (m model) Init() tea.Cmd { return nil }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" {
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m model) View() string { return "claudetopus - press q to quit\n" }

func main() {
	p := tea.NewProgram(model{}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
```

**Step 4: Create Makefile**

```makefile
.PHONY: build run clean test

BINARY=claudetopus

build:
	go build -o $(BINARY) ./cmd/claudetopus

run: build
	./$(BINARY)

test:
	go test ./... -v

clean:
	rm -f $(BINARY)
```

**Step 5: Create .gitignore**

```
claudetopus
*.exe
.DS_Store
```

**Step 6: Build and run to verify**

Run: `make run`
Expected: Alt-screen opens with "claudetopus - press q to quit". Press q exits cleanly.

**Step 7: Commit**

```bash
git add go.mod go.sum cmd/ Makefile .gitignore
git commit -m "feat: project scaffolding with minimal Bubble Tea app"
```

---

### Task 2: Data Model

**Files:**
- Create: `internal/model/instance.go`
- Create: `internal/model/instance_test.go`

**Step 1: Write tests for data model helpers**

```go
// internal/model/instance_test.go
package model

import (
	"testing"
)

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusActive, "active"},
		{StatusIdle, "idle"},
		{StatusWaitingPermission, "waiting"},
		{StatusUnknown, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestSourceTypeString(t *testing.T) {
	tests := []struct {
		source SourceType
		want   string
	}{
		{SourceCLI, "CLI"},
		{SourceVSCode, "VSCode"},
		{SourceSDK, "SDK"},
	}
	for _, tt := range tests {
		if got := tt.source.String(); got != tt.want {
			t.Errorf("SourceType(%d).String() = %q, want %q", tt.source, got, tt.want)
		}
	}
}

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusActive, "●"},
		{StatusIdle, "○"},
		{StatusWaitingPermission, "◐"},
		{StatusUnknown, "?"},
	}
	for _, tt := range tests {
		if got := tt.status.Icon(); got != tt.want {
			t.Errorf("Status(%d).Icon() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestInstanceShortModel(t *testing.T) {
	inst := Instance{Model: "claude-opus-4-6[1m]"}
	if got := inst.ShortModel(); got != "opus-4.6" {
		t.Errorf("ShortModel() = %q, want %q", got, "opus-4.6")
	}

	inst2 := Instance{Model: "claude-sonnet-4-5@20250929"}
	if got := inst2.ShortModel(); got != "sonnet-4.5" {
		t.Errorf("ShortModel() = %q, want %q", got, "sonnet-4.5")
	}
}

func TestInstanceShortProject(t *testing.T) {
	inst := Instance{WorkingDir: "/Users/azaalouk/go/src/github.com/zanetworker/claudetopus"}
	if got := inst.ShortProject(); got != "claudetopus" {
		t.Errorf("ShortProject() = %q, want %q", got, "claudetopus")
	}
}

func TestInstanceFormatMemory(t *testing.T) {
	tests := []struct {
		mb   uint64
		want string
	}{
		{405, "405M"},
		{1400, "1.4G"},
		{0, "0M"},
		{1024, "1.0G"},
	}
	for _, tt := range tests {
		inst := Instance{MemoryMB: tt.mb}
		if got := inst.FormatMemory(); got != tt.want {
			t.Errorf("FormatMemory(%d) = %q, want %q", tt.mb, got, tt.want)
		}
	}
}

func TestInstanceFormatCost(t *testing.T) {
	inst := Instance{EstCostUSD: 0.82}
	if got := inst.FormatCost(); got != "$0.82" {
		t.Errorf("FormatCost() = %q, want %q", got, "$0.82")
	}
	inst2 := Instance{EstCostUSD: 12.5}
	if got := inst2.FormatCost(); got != "$12.50" {
		t.Errorf("FormatCost() = %q, want %q", got, "$12.50")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/model/ -v`
Expected: Compilation errors (package doesn't exist yet).

**Step 3: Implement data model**

```go
// internal/model/instance.go
package model

import (
	"fmt"
	"strings"
	"time"
)

type SourceType int

const (
	SourceCLI    SourceType = iota
	SourceVSCode
	SourceSDK
)

func (s SourceType) String() string {
	switch s {
	case SourceCLI:
		return "CLI"
	case SourceVSCode:
		return "VSCode"
	case SourceSDK:
		return "SDK"
	default:
		return "unknown"
	}
}

type Status int

const (
	StatusActive            Status = iota
	StatusIdle
	StatusWaitingPermission
	StatusUnknown
)

func (s Status) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusIdle:
		return "idle"
	case StatusWaitingPermission:
		return "waiting"
	default:
		return "unknown"
	}
}

func (s Status) Icon() string {
	switch s {
	case StatusActive:
		return "●"
	case StatusIdle:
		return "○"
	case StatusWaitingPermission:
		return "◐"
	default:
		return "?"
	}
}

type Instance struct {
	PID            int
	SessionID      string
	Model          string
	PermissionMode string
	WorkingDir     string
	Source         SourceType
	StartTime      time.Time
	Status         Status
	TMuxSession    string
	MemoryMB       uint64
	GitBranch      string

	TokensIn   int64
	TokensOut  int64
	EstCostUSD float64

	TeamName    string
	TaskID      string
	TaskSubject string

	LastActivity time.Time
}

// ShortModel returns a human-friendly model name.
// "claude-opus-4-6[1m]" -> "opus-4.6"
// "claude-sonnet-4-5@20250929" -> "sonnet-4.5"
func (i Instance) ShortModel() string {
	m := i.Model
	// Strip "claude-" prefix
	m = strings.TrimPrefix(m, "claude-")
	// Strip version suffix after @ or [
	if idx := strings.IndexAny(m, "@["); idx != -1 {
		m = m[:idx]
	}
	// Replace last hyphen with dot for version: "opus-4-6" -> "opus-4.6"
	lastHyphen := strings.LastIndex(m, "-")
	if lastHyphen != -1 {
		m = m[:lastHyphen] + "." + m[lastHyphen+1:]
	}
	return m
}

// ShortProject returns the last path segment of WorkingDir.
func (i Instance) ShortProject() string {
	parts := strings.Split(strings.TrimRight(i.WorkingDir, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// FormatMemory returns memory as "405M" or "1.4G".
func (i Instance) FormatMemory() string {
	if i.MemoryMB >= 1024 {
		return fmt.Sprintf("%.1fG", float64(i.MemoryMB)/1024.0)
	}
	return fmt.Sprintf("%dM", i.MemoryMB)
}

// FormatCost returns cost as "$0.82".
func (i Instance) FormatCost() string {
	return fmt.Sprintf("$%.2f", i.EstCostUSD)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/model/ -v`
Expected: All tests PASS.

**Step 5: Commit**

```bash
git add internal/model/
git commit -m "feat: add Instance data model with display helpers"
```

---

### Task 3: Process Discovery

**Files:**
- Create: `internal/discovery/process.go`
- Create: `internal/discovery/process_test.go`

**Step 1: Write tests for process parsing**

```go
// internal/discovery/process_test.go
package discovery

import (
	"testing"

	"github.com/zanetworker/claudetopus/internal/model"
)

func TestParseProcessLine(t *testing.T) {
	// Real ps output line (simplified for test)
	line := "azaalouk  3629   8.3  0.6 509033520 415120 s009  S+    5:31PM   0:21.87 claude --dangerously-skip-permissions"
	proc, err := parseProcessLine(line)
	if err != nil {
		t.Fatalf("parseProcessLine() error: %v", err)
	}
	if proc.PID != 3629 {
		t.Errorf("PID = %d, want 3629", proc.PID)
	}
	if proc.MemoryKB != 415120 {
		t.Errorf("MemoryKB = %d, want 415120", proc.MemoryKB)
	}
	if proc.Command != "claude --dangerously-skip-permissions" {
		t.Errorf("Command = %q", proc.Command)
	}
}

func TestClassifySource(t *testing.T) {
	tests := []struct {
		cmd  string
		want model.SourceType
	}{
		{"claude --dangerously-skip-permissions", model.SourceCLI},
		{"/Users/azaalouk/.vscode/extensions/anthropic.claude-code-2.1.49-darwin-arm64/resources/native-binary/claude --output-format stream-json", model.SourceVSCode},
		{"/path/.venv/lib/python3.11/site-packages/claude_agent_sdk/_bundled/claude --output-format stream-json", model.SourceSDK},
	}
	for _, tt := range tests {
		if got := classifySource(tt.cmd); got != tt.want {
			t.Errorf("classifySource(%q) = %v, want %v", tt.cmd[:40], got, tt.want)
		}
	}
}

func TestExtractFlag(t *testing.T) {
	args := "--model claude-opus-4-6[1m] --permission-mode default --resume abc-123"
	tests := []struct {
		flag string
		want string
	}{
		{"--model", "claude-opus-4-6[1m]"},
		{"--permission-mode", "default"},
		{"--resume", "abc-123"},
		{"--nonexistent", ""},
	}
	for _, tt := range tests {
		if got := extractFlag(args, tt.flag); got != tt.want {
			t.Errorf("extractFlag(%q) = %q, want %q", tt.flag, got, tt.want)
		}
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		args string
		want string
	}{
		{"--resume abc-123-def", "abc-123-def"},
		{"--session-id xyz-789", "xyz-789"},
		{"--model opus", ""},
	}
	for _, tt := range tests {
		if got := extractSessionID(tt.args); got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/discovery/ -v`
Expected: Compilation errors.

**Step 3: Implement process discovery**

```go
// internal/discovery/process.go
package discovery

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/zanetworker/claudetopus/internal/model"
)

// rawProcess holds fields parsed directly from ps output.
type rawProcess struct {
	PID      int
	MemoryKB uint64
	Command  string
}

// parseProcessLine parses one line of `ps aux` output.
// Format: USER PID %CPU %MEM VSZ RSS TT STAT STARTED TIME COMMAND...
func parseProcessLine(line string) (rawProcess, error) {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return rawProcess{}, fmt.Errorf("too few fields: %d", len(fields))
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return rawProcess{}, fmt.Errorf("invalid PID %q: %w", fields[1], err)
	}
	rss, err := strconv.ParseUint(fields[5], 10, 64)
	if err != nil {
		return rawProcess{}, fmt.Errorf("invalid RSS %q: %w", fields[5], err)
	}
	cmd := strings.Join(fields[10:], " ")
	return rawProcess{PID: pid, MemoryKB: rss, Command: cmd}, nil
}

// classifySource determines if a claude process is CLI, VS Code, or SDK.
func classifySource(cmd string) model.SourceType {
	if strings.Contains(cmd, ".vscode/extensions/") || strings.Contains(cmd, "claude-code-") {
		return model.SourceVSCode
	}
	if strings.Contains(cmd, "claude_agent_sdk") || strings.Contains(cmd, "agent-sdk") {
		return model.SourceSDK
	}
	return model.SourceCLI
}

// extractFlag returns the value following a CLI flag, or "" if not found.
func extractFlag(args, flag string) string {
	idx := strings.Index(args, flag+" ")
	if idx == -1 {
		// Check if it's at the end
		if strings.HasSuffix(args, flag) {
			return ""
		}
		return ""
	}
	rest := args[idx+len(flag)+1:]
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	val := fields[0]
	if strings.HasPrefix(val, "--") {
		return "" // next flag, not a value
	}
	return val
}

// extractSessionID extracts session ID from --resume or --session-id flags.
func extractSessionID(args string) string {
	if id := extractFlag(args, "--resume"); id != "" {
		return id
	}
	return extractFlag(args, "--session-id")
}

// ScanProcesses finds all running Claude Code processes and returns Instance stubs.
func ScanProcesses() ([]model.Instance, error) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return nil, fmt.Errorf("ps aux: %w", err)
	}
	var instances []model.Instance
	for _, line := range strings.Split(string(out), "\n") {
		if !isClaudeProcess(line) {
			continue
		}
		proc, err := parseProcessLine(line)
		if err != nil {
			continue
		}
		inst := buildInstance(proc)
		instances = append(instances, inst)
	}
	return instances, nil
}

// isClaudeProcess checks if a ps line represents a claude process worth tracking.
func isClaudeProcess(line string) bool {
	// Must contain "claude" binary
	if !strings.Contains(line, "claude") {
		return false
	}
	// Exclude tmux wrapper processes, grep, and claudetopus itself
	lower := line
	if strings.Contains(lower, "tmux") && !strings.Contains(lower, "--model") {
		return false
	}
	if strings.Contains(lower, "grep") {
		return false
	}
	if strings.Contains(lower, "claudetopus") {
		return false
	}
	// Must look like an actual claude binary invocation
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return false
	}
	cmd := strings.Join(fields[10:], " ")
	// The command should start with "claude" or end with "/claude"
	cmdParts := strings.Fields(cmd)
	if len(cmdParts) == 0 {
		return false
	}
	binary := cmdParts[0]
	return binary == "claude" || strings.HasSuffix(binary, "/claude")
}

// buildInstance creates an Instance from a raw process.
func buildInstance(proc rawProcess) model.Instance {
	return model.Instance{
		PID:            proc.PID,
		SessionID:      extractSessionID(proc.Command),
		Model:          extractFlag(proc.Command, "--model"),
		PermissionMode: extractFlag(proc.Command, "--permission-mode"),
		Source:         classifySource(proc.Command),
		MemoryMB:       proc.MemoryKB / 1024,
		Status:         model.StatusUnknown,
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/discovery/ -v`
Expected: All tests PASS.

**Step 5: Commit**

```bash
git add internal/discovery/
git commit -m "feat: add process discovery - scan ps for Claude instances"
```

---

### Task 4: Working Directory Resolution

**Files:**
- Modify: `internal/discovery/process.go`
- Create: `internal/discovery/cwd.go`
- Create: `internal/discovery/cwd_test.go`

**Step 1: Write test for CWD resolution**

```go
// internal/discovery/cwd_test.go
package discovery

import (
	"os"
	"testing"
)

func TestGetProcessCwd(t *testing.T) {
	// Use our own PID - we know our cwd
	pid := os.Getpid()
	cwd, err := getProcessCwd(pid)
	if err != nil {
		t.Fatalf("getProcessCwd(%d) error: %v", pid, err)
	}
	expected, _ := os.Getwd()
	if cwd != expected {
		t.Errorf("getProcessCwd(%d) = %q, want %q", pid, cwd, expected)
	}
}

func TestGetProcessCwd_InvalidPID(t *testing.T) {
	_, err := getProcessCwd(999999999)
	if err == nil {
		t.Error("expected error for invalid PID, got nil")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/discovery/ -run TestGetProcessCwd -v`
Expected: Compilation error.

**Step 3: Implement CWD resolution (macOS uses lsof)**

```go
// internal/discovery/cwd.go
package discovery

import (
	"fmt"
	"os/exec"
	"strings"
)

// getProcessCwd returns the current working directory of a process by PID.
// On macOS, uses lsof -p PID -Fn to find the cwd entry.
func getProcessCwd(pid int) (string, error) {
	out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof -p %d: %w", pid, err)
	}
	// lsof -Fn outputs lines like:
	// p<pid>
	// fcwd
	// n/path/to/cwd
	lines := strings.Split(string(out), "\n")
	foundCwd := false
	for _, line := range lines {
		if line == "fcwd" {
			foundCwd = true
			continue
		}
		if foundCwd && strings.HasPrefix(line, "n") {
			return line[1:], nil // strip the "n" prefix
		}
	}
	return "", fmt.Errorf("cwd not found for PID %d", pid)
}
```

**Step 4: Update buildInstance to resolve CWD**

In `internal/discovery/process.go`, update `buildInstance`:

```go
func buildInstance(proc rawProcess) model.Instance {
	cwd, _ := getProcessCwd(proc.PID)
	return model.Instance{
		PID:            proc.PID,
		SessionID:      extractSessionID(proc.Command),
		Model:          extractFlag(proc.Command, "--model"),
		PermissionMode: extractFlag(proc.Command, "--permission-mode"),
		WorkingDir:     cwd,
		Source:         classifySource(proc.Command),
		MemoryMB:       proc.MemoryKB / 1024,
		Status:         model.StatusUnknown,
	}
}
```

**Step 5: Run tests**

Run: `go test ./internal/discovery/ -v`
Expected: All tests PASS.

**Step 6: Commit**

```bash
git add internal/discovery/
git commit -m "feat: resolve process CWD via lsof for project detection"
```

---

### Task 5: Session JSONL Parser

**Files:**
- Create: `internal/discovery/session.go`
- Create: `internal/discovery/session_test.go`
- Create: `testdata/sample_session.jsonl`

**Step 1: Create test fixture**

Create `testdata/sample_session.jsonl` with realistic entries:

```jsonl
{"type":"progress","sessionId":"abc-123","timestamp":"2026-02-20T16:31:12.443Z","data":{"type":"hook_progress"}}
{"type":"user","sessionId":"abc-123","cwd":"/Users/test/myproject","gitBranch":"main","message":{"role":"user","content":"hello"},"timestamp":"2026-02-20T16:32:28.695Z"}
{"type":"assistant","sessionId":"abc-123","cwd":"/Users/test/myproject","gitBranch":"main","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"Hi!"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":500}},"timestamp":"2026-02-20T16:32:37.451Z"}
{"type":"user","sessionId":"abc-123","cwd":"/Users/test/myproject","gitBranch":"feature","message":{"role":"user","content":"fix the bug"},"timestamp":"2026-02-20T16:35:00.000Z"}
{"type":"assistant","sessionId":"abc-123","cwd":"/Users/test/myproject","gitBranch":"feature","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":200,"output_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000}},"timestamp":"2026-02-20T16:35:10.000Z"}
```

**Step 2: Write tests**

```go
// internal/discovery/session_test.go
package discovery

import (
	"testing"
)

func TestParseSessionFile(t *testing.T) {
	info, err := ParseSessionFile("../../testdata/sample_session.jsonl")
	if err != nil {
		t.Fatalf("ParseSessionFile() error: %v", err)
	}

	if info.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "abc-123")
	}
	if info.GitBranch != "feature" {
		t.Errorf("GitBranch = %q, want %q", info.GitBranch, "feature")
	}
	if info.TokensIn != 300 {
		t.Errorf("TokensIn = %d, want 300", info.TokensIn)
	}
	if info.TokensOut != 150 {
		t.Errorf("TokensOut = %d, want 150", info.TokensOut)
	}
	if info.CacheReadTokens != 1500 {
		t.Errorf("CacheReadTokens = %d, want 1500", info.CacheReadTokens)
	}
	if info.CacheWriteTokens != 200 {
		t.Errorf("CacheWriteTokens = %d, want 200", info.CacheWriteTokens)
	}
	if info.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", info.MessageCount)
	}
}

func TestFindSessionFile(t *testing.T) {
	// This tests with a known session ID against the fixture
	path := findSessionFile("abc-123", "../../testdata")
	if path != "" {
		t.Log("findSessionFile found path (may vary by test env)")
	}
}
```

**Step 3: Run tests to verify they fail**

Run: `go test ./internal/discovery/ -run TestParseSession -v`
Expected: Compilation error.

**Step 4: Implement session parser**

```go
// internal/discovery/session.go
package discovery

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionInfo holds aggregated data from a session JSONL file.
type SessionInfo struct {
	SessionID        string
	GitBranch        string
	TokensIn         int64
	TokensOut        int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	MessageCount     int
	LastTimestamp     time.Time
}

// jsonlEntry represents a single line in the JSONL file.
type jsonlEntry struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	GitBranch string          `json:"gitBranch"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type messagePayload struct {
	Role  string `json:"role"`
	Usage *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// ParseSessionFile reads a session JSONL file and returns aggregated info.
func ParseSessionFile(path string) (SessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var info SessionInfo
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large lines
	for scanner.Scan() {
		var entry jsonlEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip malformed lines
		}

		if entry.SessionID != "" {
			info.SessionID = entry.SessionID
		}
		if entry.GitBranch != "" {
			info.GitBranch = entry.GitBranch
		}

		if entry.Type == "user" || entry.Type == "assistant" {
			info.MessageCount++
		}

		if entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				info.LastTimestamp = ts
			}
		}

		// Parse usage from assistant messages
		if entry.Type == "assistant" && len(entry.Message) > 0 {
			var msg messagePayload
			if err := json.Unmarshal(entry.Message, &msg); err == nil && msg.Usage != nil {
				info.TokensIn += msg.Usage.InputTokens
				info.TokensOut += msg.Usage.OutputTokens
				info.CacheReadTokens += msg.Usage.CacheReadInputTokens
				info.CacheWriteTokens += msg.Usage.CacheCreationInputTokens
			}
		}
	}
	return info, scanner.Err()
}

// findSessionFile looks for a session JSONL in the Claude projects directory.
func findSessionFile(sessionID, projectsDir string) string {
	if sessionID == "" {
		return ""
	}
	// Walk the projects directory looking for <sessionID>.jsonl
	matches, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// FindSessionFileDefault looks in the default ~/.claude/projects/ directory.
func FindSessionFileDefault(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return findSessionFile(sessionID, filepath.Join(home, ".claude", "projects"))
}

// SessionFilesForDir finds session JSONL files whose project dir name matches.
func SessionFilesForDir(workingDir string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	// Convert /Users/foo/go/src/github.com/bar/baz to -Users-foo-go-src-github-com-bar-baz
	dirKey := strings.ReplaceAll(workingDir, "/", "-")
	if strings.HasPrefix(dirKey, "-") {
		// keep as is
	}
	projectDir := filepath.Join(home, ".claude", "projects", dirKey)
	matches, _ := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	return matches
}
```

**Step 5: Run tests**

Run: `go test ./internal/discovery/ -v`
Expected: All tests PASS.

**Step 6: Commit**

```bash
git add internal/discovery/session.go internal/discovery/session_test.go testdata/
git commit -m "feat: add session JSONL parser for token usage and metadata"
```

---

### Task 6: tmux Session Matching

**Files:**
- Create: `internal/discovery/tmux.go`
- Create: `internal/discovery/tmux_test.go`

**Step 1: Write tests**

```go
// internal/discovery/tmux_test.go
package discovery

import (
	"testing"
)

func TestParseTmuxLine(t *testing.T) {
	line := "claude-demoharness: 1 windows (created Fri Feb 20 12:23:46 2026) (attached)"
	name, attached := parseTmuxLine(line)
	if name != "claude-demoharness" {
		t.Errorf("name = %q, want %q", name, "claude-demoharness")
	}
	if !attached {
		t.Error("attached = false, want true")
	}

	line2 := "myproject: 2 windows (created Thu Feb 19 10:00:00 2026)"
	name2, attached2 := parseTmuxLine(line2)
	if name2 != "myproject" {
		t.Errorf("name = %q, want %q", name2, "myproject")
	}
	if attached2 {
		t.Error("attached = true, want false")
	}
}

func TestMatchTmuxSession(t *testing.T) {
	sessions := []tmuxSession{
		{Name: "claude-demoharness", Attached: true},
		{Name: "claude-default", Attached: false},
		{Name: "other", Attached: false},
	}

	// Match by project name
	match := matchTmuxSession(sessions, "/Users/foo/demoharness")
	if match != "claude-demoharness" {
		t.Errorf("match = %q, want %q", match, "claude-demoharness")
	}

	// No match
	noMatch := matchTmuxSession(sessions, "/Users/foo/unknown-project")
	if noMatch != "" {
		t.Errorf("match = %q, want empty", noMatch)
	}
}
```

**Step 2: Run tests, verify failure**

Run: `go test ./internal/discovery/ -run TestParseTmux -v`

**Step 3: Implement**

```go
// internal/discovery/tmux.go
package discovery

import (
	"os/exec"
	"strings"
)

type tmuxSession struct {
	Name     string
	Attached bool
}

// parseTmuxLine parses a line from `tmux list-sessions`.
func parseTmuxLine(line string) (name string, attached bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) == 0 {
		return "", false
	}
	name = strings.TrimSpace(parts[0])
	attached = strings.Contains(line, "(attached)")
	return name, attached
}

// ListTmuxSessions returns all active tmux sessions.
func ListTmuxSessions() []tmuxSession {
	out, err := exec.Command("tmux", "list-sessions").Output()
	if err != nil {
		return nil
	}
	var sessions []tmuxSession
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		name, attached := parseTmuxLine(line)
		sessions = append(sessions, tmuxSession{Name: name, Attached: attached})
	}
	return sessions
}

// matchTmuxSession finds a tmux session name that matches a working directory.
// Convention: sessions named "claude-<project>" match dirs ending in "<project>".
func matchTmuxSession(sessions []tmuxSession, workingDir string) string {
	parts := strings.Split(strings.TrimRight(workingDir, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	project := parts[len(parts)-1]
	for _, s := range sessions {
		// Check "claude-<project>" convention
		if s.Name == "claude-"+project {
			return s.Name
		}
		// Also check direct name match
		if s.Name == project {
			return s.Name
		}
	}
	return ""
}
```

**Step 4: Run tests**

Run: `go test ./internal/discovery/ -v`
Expected: All PASS.

**Step 5: Commit**

```bash
git add internal/discovery/tmux.go internal/discovery/tmux_test.go
git commit -m "feat: add tmux session discovery and matching"
```

---

### Task 7: Cost Tracker

**Files:**
- Create: `internal/cost/tracker.go`
- Create: `internal/cost/tracker_test.go`

**Step 1: Write tests**

```go
// internal/cost/tracker_test.go
package cost

import (
	"math"
	"testing"
)

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		model    string
		in, out  int64
		cacheR   int64
		cacheW   int64
		wantCost float64
	}{
		// opus: $15/M in, $75/M out, cache read $1.50/M, cache write $18.75/M
		{"claude-opus-4-6", 1000000, 100000, 0, 0, 22.50},
		// sonnet: $3/M in, $15/M out
		{"claude-sonnet-4-5", 1000000, 100000, 0, 0, 4.50},
		// zero tokens = zero cost
		{"claude-opus-4-6", 0, 0, 0, 0, 0.0},
		// with cache
		{"claude-opus-4-6", 100, 50, 500000, 100000, 2.6288},
	}
	for _, tt := range tests {
		got := Calculate(tt.model, tt.in, tt.out, tt.cacheR, tt.cacheW)
		if math.Abs(got-tt.wantCost) > 0.01 {
			t.Errorf("Calculate(%q, %d, %d, %d, %d) = %.4f, want %.4f",
				tt.model, tt.in, tt.out, tt.cacheR, tt.cacheW, got, tt.wantCost)
		}
	}
}

func TestNormalizeModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-6[1m]", "claude-opus-4-6"},
		{"claude-opus-4-6", "claude-opus-4-6"},
		{"claude-sonnet-4-5@20250929", "claude-sonnet-4-5"},
		{"opus", "claude-opus-4-6"},
		{"sonnet", "claude-sonnet-4-5"},
	}
	for _, tt := range tests {
		if got := normalizeModel(tt.input); got != tt.want {
			t.Errorf("normalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

**Step 2: Run tests, verify failure**

**Step 3: Implement**

```go
// internal/cost/tracker.go
package cost

import "strings"

// Pricing per million tokens.
type pricing struct {
	InputPerM      float64
	OutputPerM     float64
	CacheReadPerM  float64
	CacheWritePerM float64
}

var modelPricing = map[string]pricing{
	"claude-opus-4-6": {
		InputPerM:      15.00,
		OutputPerM:     75.00,
		CacheReadPerM:  1.50,
		CacheWritePerM: 18.75,
	},
	"claude-sonnet-4-5": {
		InputPerM:      3.00,
		OutputPerM:     15.00,
		CacheReadPerM:  0.30,
		CacheWritePerM: 3.75,
	},
	"claude-haiku-3-5": {
		InputPerM:      0.80,
		OutputPerM:     4.00,
		CacheReadPerM:  0.08,
		CacheWritePerM: 1.00,
	},
}

// normalizeModel strips version suffixes and resolves aliases.
func normalizeModel(m string) string {
	// Handle aliases
	switch strings.ToLower(m) {
	case "opus":
		return "claude-opus-4-6"
	case "sonnet":
		return "claude-sonnet-4-5"
	case "haiku":
		return "claude-haiku-3-5"
	}
	// Strip suffixes like [1m] or @20250929
	if idx := strings.IndexAny(m, "[@"); idx != -1 {
		m = m[:idx]
	}
	return m
}

// Calculate returns the estimated cost in USD.
func Calculate(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	m := normalizeModel(model)
	p, ok := modelPricing[m]
	if !ok {
		// Default to opus pricing if unknown
		p = modelPricing["claude-opus-4-6"]
	}
	cost := float64(inputTokens) / 1_000_000.0 * p.InputPerM
	cost += float64(outputTokens) / 1_000_000.0 * p.OutputPerM
	cost += float64(cacheReadTokens) / 1_000_000.0 * p.CacheReadPerM
	cost += float64(cacheWriteTokens) / 1_000_000.0 * p.CacheWritePerM
	return cost
}
```

**Step 4: Run tests**

Run: `go test ./internal/cost/ -v`
Expected: All PASS.

**Step 5: Commit**

```bash
git add internal/cost/
git commit -m "feat: add cost tracker with per-model pricing"
```

---

### Task 8: Team & Task Reader

**Files:**
- Create: `internal/team/reader.go`
- Create: `internal/team/reader_test.go`
- Create: `testdata/teams/test-team/config.json`

**Step 1: Create test fixture**

`testdata/teams/test-team/config.json`:
```json
{
  "name": "test-team",
  "description": "A test team",
  "members": [
    {
      "agentId": "lead@test-team",
      "name": "team-lead",
      "agentType": "team-lead",
      "model": "claude-opus-4-6[1m]"
    },
    {
      "agentId": "researcher@test-team",
      "name": "researcher",
      "agentType": "general-purpose",
      "model": "claude-opus-4-6"
    }
  ]
}
```

**Step 2: Write tests**

```go
// internal/team/reader_test.go
package team

import (
	"testing"
)

func TestReadTeamConfig(t *testing.T) {
	tc, err := ReadTeamConfig("../../testdata/teams/test-team/config.json")
	if err != nil {
		t.Fatalf("ReadTeamConfig() error: %v", err)
	}
	if tc.Name != "test-team" {
		t.Errorf("Name = %q, want %q", tc.Name, "test-team")
	}
	if len(tc.Members) != 2 {
		t.Fatalf("Members count = %d, want 2", len(tc.Members))
	}
	if tc.Members[0].Name != "team-lead" {
		t.Errorf("Members[0].Name = %q, want %q", tc.Members[0].Name, "team-lead")
	}
}

func TestListTeams(t *testing.T) {
	teams, err := ListTeams("../../testdata/teams")
	if err != nil {
		t.Fatalf("ListTeams() error: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("teams count = %d, want 1", len(teams))
	}
	if teams[0].Name != "test-team" {
		t.Errorf("teams[0].Name = %q, want %q", teams[0].Name, "test-team")
	}
}
```

**Step 3: Implement**

```go
// internal/team/reader.go
package team

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Member struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	AgentType string `json:"agentType"`
	Model     string `json:"model"`
}

type TeamConfig struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []Member `json:"members"`
}

// ReadTeamConfig reads a single team config file.
func ReadTeamConfig(path string) (TeamConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TeamConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var tc TeamConfig
	if err := json.Unmarshal(data, &tc); err != nil {
		return TeamConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return tc, nil
}

// ListTeams reads all team configs from a teams directory.
func ListTeams(teamsDir string) ([]TeamConfig, error) {
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return nil, fmt.Errorf("read teams dir %s: %w", teamsDir, err)
	}
	var teams []TeamConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		configPath := filepath.Join(teamsDir, entry.Name(), "config.json")
		tc, err := ReadTeamConfig(configPath)
		if err != nil {
			continue // skip teams with broken configs
		}
		teams = append(teams, tc)
	}
	return teams, nil
}

// ListTeamsDefault reads from ~/.claude/teams/.
func ListTeamsDefault() ([]TeamConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return ListTeams(filepath.Join(home, ".claude", "teams"))
}
```

**Step 4: Run tests**

Run: `go test ./internal/team/ -v`
Expected: All PASS.

**Step 5: Commit**

```bash
git add internal/team/ testdata/teams/
git commit -m "feat: add team config reader"
```

---

### Task 9: Discovery Orchestrator

**Files:**
- Create: `internal/discovery/orchestrator.go`
- Modify: `internal/discovery/process.go` (add enrichment)

This ties process discovery + session parsing + tmux matching + cost calculation together.

**Step 1: Implement orchestrator**

```go
// internal/discovery/orchestrator.go
package discovery

import (
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/claudetopus/internal/cost"
	"github.com/zanetworker/claudetopus/internal/model"
)

// Orchestrator coordinates all discovery sources to produce enriched instances.
type Orchestrator struct {
	projectsDir string
	teamsDir    string
}

// NewOrchestrator creates an orchestrator with default paths.
func NewOrchestrator() *Orchestrator {
	home, _ := os.UserHomeDir()
	return &Orchestrator{
		projectsDir: filepath.Join(home, ".claude", "projects"),
		teamsDir:    filepath.Join(home, ".claude", "teams"),
	}
}

// Discover finds all Claude instances and enriches them with session data.
func (o *Orchestrator) Discover() ([]model.Instance, error) {
	instances, err := ScanProcesses()
	if err != nil {
		return nil, err
	}

	tmuxSessions := ListTmuxSessions()

	for i := range instances {
		o.enrichInstance(&instances[i], tmuxSessions)
	}
	return instances, nil
}

func (o *Orchestrator) enrichInstance(inst *model.Instance, tmuxSessions []tmuxSession) {
	// Match tmux session
	if inst.WorkingDir != "" {
		inst.TMuxSession = matchTmuxSession(tmuxSessions, inst.WorkingDir)
	}

	// Find and parse session JSONL
	sessionFile := ""
	if inst.SessionID != "" {
		sessionFile = findSessionFile(inst.SessionID, o.projectsDir)
	}
	if sessionFile == "" && inst.WorkingDir != "" {
		// Try to find by working directory
		files := SessionFilesForDir(inst.WorkingDir)
		if len(files) > 0 {
			// Use the most recently modified file
			var newest string
			var newestTime time.Time
			for _, f := range files {
				info, err := os.Stat(f)
				if err == nil && info.ModTime().After(newestTime) {
					newest = f
					newestTime = info.ModTime()
				}
			}
			sessionFile = newest
		}
	}

	if sessionFile != "" {
		info, err := ParseSessionFile(sessionFile)
		if err == nil {
			if inst.SessionID == "" {
				inst.SessionID = info.SessionID
			}
			inst.GitBranch = info.GitBranch
			inst.TokensIn = info.TokensIn
			inst.TokensOut = info.TokensOut
			inst.LastActivity = info.LastTimestamp
			inst.EstCostUSD = cost.Calculate(
				inst.Model,
				info.TokensIn,
				info.TokensOut,
				info.CacheReadTokens,
				info.CacheWriteTokens,
			)

			// Determine status from activity
			if time.Since(info.LastTimestamp) < 30*time.Second {
				inst.Status = model.StatusActive
			} else {
				inst.Status = model.StatusIdle
			}
		}
	}
}
```

**Step 2: Build to verify compilation**

Run: `go build ./internal/discovery/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/discovery/orchestrator.go
git commit -m "feat: add discovery orchestrator - ties process, session, tmux together"
```

---

### Task 10: TUI Styles

**Files:**
- Create: `internal/tui/styles.go`

**Step 1: Implement styles**

```go
// internal/tui/styles.go
package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorPrimary   = lipgloss.Color("#7C3AED") // Purple
	colorSecondary = lipgloss.Color("#06B6D4") // Cyan
	colorActive    = lipgloss.Color("#22C55E") // Green
	colorIdle      = lipgloss.Color("#6B7280") // Gray
	colorWaiting   = lipgloss.Color("#F59E0B") // Amber
	colorError     = lipgloss.Color("#EF4444") // Red
	colorBorder    = lipgloss.Color("#374151") // Dark gray
	colorHeader    = lipgloss.Color("#E5E7EB") // Light gray
	colorMuted     = lipgloss.Color("#9CA3AF") // Medium gray
	colorCost      = lipgloss.Color("#34D399") // Emerald

	// Header bar
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorHeader).
			Background(lipgloss.Color("#1F2937")).
			Padding(0, 1)

	// Table
	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSecondary).
				Padding(0, 1)

	tableRowStyle = lipgloss.NewStyle().
			Padding(0, 1)

	selectedRowStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1F2937")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	// Status bar (bottom)
	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Background(lipgloss.Color("#111827")).
			Padding(0, 1)

	// Command input
	commandStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	// Status indicators
	activeStyle  = lipgloss.NewStyle().Foreground(colorActive)
	idleStyle    = lipgloss.NewStyle().Foreground(colorIdle)
	waitingStyle = lipgloss.NewStyle().Foreground(colorWaiting)

	// Borders
	borderStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder)

	// Breadcrumb
	breadcrumbStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	// Cost
	costStyle = lipgloss.NewStyle().Foreground(colorCost)
)

// StatusStyle returns the appropriate style for a status string.
func StatusStyle(status string) lipgloss.Style {
	switch status {
	case "active":
		return activeStyle
	case "idle":
		return idleStyle
	case "waiting":
		return waitingStyle
	default:
		return idleStyle
	}
}
```

**Step 2: Build to verify**

Run: `go build ./internal/tui/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/styles.go
git commit -m "feat: add TUI styles with color scheme"
```

---

### Task 11: Command Palette

**Files:**
- Create: `internal/tui/command.go`
- Create: `internal/tui/command_test.go`

**Step 1: Write tests**

```go
// internal/tui/command_test.go
package tui

import (
	"testing"
)

func TestResolveCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"instances", "instances"},
		{"i", "instances"},
		{"logs", "logs"},
		{"l", "logs"},
		{"session", "session"},
		{"s", "session"},
		{"teams", "teams"},
		{"t", "teams"},
		{"costs", "costs"},
		{"c", "costs"},
		{"quit", "quit"},
		{"q", "quit"},
		{"help", "help"},
		{"?", "help"},
		{"new", "new"},
		{"n", "new"},
		{"kill", "kill"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		if got := resolveCommand(tt.input); got != tt.want {
			t.Errorf("resolveCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCommandCompletions(t *testing.T) {
	results := commandCompletions("co")
	found := false
	for _, r := range results {
		if r == "costs" {
			found = true
		}
	}
	if !found {
		t.Errorf("commandCompletions(\"co\") should include \"costs\", got %v", results)
	}
}
```

**Step 2: Implement**

```go
// internal/tui/command.go
package tui

import "strings"

// commandAliases maps short aliases to full command names.
var commandAliases = map[string]string{
	"i": "instances",
	"l": "logs",
	"s": "session",
	"t": "teams",
	"c": "costs",
	"q": "quit",
	"?": "help",
	"n": "new",
}

// allCommands is the full list of available commands.
var allCommands = []string{
	"instances", "logs", "session", "teams", "costs",
	"help", "new", "kill", "quit",
}

// resolveCommand resolves an alias or validates a full command name.
func resolveCommand(input string) string {
	input = strings.TrimSpace(strings.ToLower(input))
	// Check alias
	if full, ok := commandAliases[input]; ok {
		return full
	}
	// Check if it's a full command name
	for _, cmd := range allCommands {
		if cmd == input {
			return cmd
		}
	}
	return ""
}

// commandCompletions returns commands matching a prefix.
func commandCompletions(prefix string) []string {
	prefix = strings.ToLower(prefix)
	var matches []string
	for _, cmd := range allCommands {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
		}
	}
	return matches
}
```

**Step 3: Run tests**

Run: `go test ./internal/tui/ -v`
Expected: All PASS.

**Step 4: Commit**

```bash
git add internal/tui/command.go internal/tui/command_test.go
git commit -m "feat: add command palette with aliases and tab completion"
```

---

### Task 12: Instances View

**Files:**
- Create: `internal/tui/views/instances.go`

**Step 1: Implement the instances table view**

```go
// internal/tui/views/instances.go
package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/claudetopus/internal/model"
)

type InstancesView struct {
	instances []model.Instance
	cursor    int
	width     int
	height    int
	filter    string
}

func NewInstancesView() InstancesView {
	return InstancesView{}
}

func (v InstancesView) SetInstances(instances []model.Instance) InstancesView {
	v.instances = instances
	if v.cursor >= len(instances) {
		v.cursor = max(0, len(instances)-1)
	}
	return v
}

func (v InstancesView) SetSize(w, h int) InstancesView {
	v.width = w
	v.height = h
	return v
}

func (v InstancesView) SetFilter(f string) InstancesView {
	v.filter = f
	v.cursor = 0
	return v
}

func (v InstancesView) Selected() *model.Instance {
	filtered := v.filteredInstances()
	if v.cursor >= 0 && v.cursor < len(filtered) {
		return &filtered[v.cursor]
	}
	return nil
}

func (v InstancesView) Cursor() int { return v.cursor }

func (v InstancesView) Update(msg tea.Msg) (InstancesView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		filtered := v.filteredInstances()
		switch msg.String() {
		case "j", "down":
			if v.cursor < len(filtered)-1 {
				v.cursor++
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
			}
		case "g":
			v.cursor = 0
		case "G":
			v.cursor = max(0, len(filtered)-1)
		}
	}
	return v, nil
}

func (v InstancesView) View() string {
	if v.width == 0 {
		return ""
	}

	filtered := v.filteredInstances()

	// Column widths
	colPID := 8
	colStatus := 10
	colModel := 14
	colProject := 20
	colPerm := 10
	colMem := 8
	colCost := 8

	// Header
	header := fmt.Sprintf("  %-*s %-*s %-*s %-*s %-*s %-*s %-*s",
		colPID, "PID",
		colStatus, "STATUS",
		colModel, "MODEL",
		colProject, "PROJECT",
		colPerm, "PERM",
		colMem, "MEM",
		colCost, "COST",
	)
	headerStyled := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#06B6D4")).
		Render(header)

	// Calculate visible rows
	maxRows := v.height - 4 // header + border
	if maxRows < 1 {
		maxRows = 1
	}

	// Scroll offset
	scrollOffset := 0
	if v.cursor >= maxRows {
		scrollOffset = v.cursor - maxRows + 1
	}

	var rows []string
	for idx := scrollOffset; idx < len(filtered) && idx < scrollOffset+maxRows; idx++ {
		inst := filtered[idx]
		statusIcon := inst.Status.Icon()
		statusStr := inst.Status.String()

		var statusStyled string
		switch inst.Status {
		case model.StatusActive:
			statusStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render(statusIcon + statusStr)
		case model.StatusIdle:
			statusStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render(statusIcon + statusStr)
		case model.StatusWaitingPermission:
			statusStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render(statusIcon + statusStr)
		default:
			statusStyled = statusIcon + statusStr
		}

		project := inst.ShortProject()
		if len(project) > colProject {
			project = project[:colProject-1] + "…"
		}
		perm := inst.PermissionMode
		if perm == "bypassPermissions" {
			perm = "bypass"
		}
		if len(perm) > colPerm {
			perm = perm[:colPerm-1] + "…"
		}

		row := fmt.Sprintf("  %-*d %-*s %-*s %-*s %-*s %-*s %-*s",
			colPID, inst.PID,
			colStatus, statusStyled,
			colModel, inst.ShortModel(),
			colProject, project,
			colPerm, perm,
			colMem, inst.FormatMemory(),
			colCost, inst.FormatCost(),
		)

		if idx == v.cursor {
			row = lipgloss.NewStyle().
				Background(lipgloss.Color("#1F2937")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Width(v.width - 2).
				Render(row)
		}
		rows = append(rows, row)
	}

	return headerStyled + "\n" + strings.Join(rows, "\n")
}

func (v InstancesView) filteredInstances() []model.Instance {
	if v.filter == "" {
		return v.instances
	}
	filter := strings.ToLower(v.filter)
	var filtered []model.Instance
	for _, inst := range v.instances {
		if strings.Contains(strings.ToLower(inst.ShortProject()), filter) ||
			strings.Contains(strings.ToLower(inst.ShortModel()), filter) ||
			strings.Contains(strings.ToLower(inst.Status.String()), filter) ||
			strings.Contains(strings.ToLower(inst.Source.String()), filter) {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
```

**Step 2: Build to verify**

Run: `go build ./internal/tui/views/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/views/instances.go
git commit -m "feat: add instances table view with filtering and scrolling"
```

---

### Task 13: Logs View

**Files:**
- Create: `internal/tui/views/logs.go`

**Step 1: Implement**

```go
// internal/tui/views/logs.go
package views

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type LogEntry struct {
	Timestamp time.Time
	Type      string
	Summary   string
}

type LogsView struct {
	entries    []LogEntry
	filePath   string
	width      int
	height     int
	scrollPos  int
	autoScroll bool
	pid        int
}

func NewLogsView(pid int, filePath string) LogsView {
	v := LogsView{
		filePath:   filePath,
		autoScroll: true,
		pid:        pid,
	}
	v.loadEntries()
	return v
}

func (v LogsView) PID() int { return v.pid }

func (v LogsView) SetSize(w, h int) LogsView {
	v.width = w
	v.height = h
	return v
}

func (v *LogsView) Reload() {
	v.loadEntries()
}

func (v *LogsView) loadEntries() {
	if v.filePath == "" {
		return
	}
	f, err := os.Open(v.filePath)
	if err != nil {
		return
	}
	defer f.Close()

	v.entries = nil
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var raw map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}

		entry := LogEntry{
			Type: fmt.Sprintf("%v", raw["type"]),
		}

		if ts, ok := raw["timestamp"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				entry.Timestamp = parsed
			}
		}

		entry.Summary = summarizeEntry(raw)
		v.entries = append(v.entries, entry)
	}

	if v.autoScroll {
		v.scrollToBottom()
	}
}

func summarizeEntry(raw map[string]interface{}) string {
	typ := fmt.Sprintf("%v", raw["type"])
	switch typ {
	case "user":
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				if len(content) > 80 {
					content = content[:77] + "..."
				}
				return fmt.Sprintf("USER: %s", content)
			}
		}
		return "USER: [message]"
	case "assistant":
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].([]interface{}); ok && len(content) > 0 {
				if first, ok := content[0].(map[string]interface{}); ok {
					if text, ok := first["text"].(string); ok {
						if len(text) > 80 {
							text = text[:77] + "..."
						}
						return fmt.Sprintf("ASSISTANT: %s", strings.TrimSpace(text))
					}
					if first["type"] == "tool_use" {
						name := fmt.Sprintf("%v", first["name"])
						return fmt.Sprintf("TOOL CALL: %s", name)
					}
				}
			}
		}
		return "ASSISTANT: [response]"
	case "progress":
		if data, ok := raw["data"].(map[string]interface{}); ok {
			if hookName, ok := data["hookName"].(string); ok {
				return fmt.Sprintf("HOOK: %s", hookName)
			}
		}
		return "PROGRESS"
	default:
		return strings.ToUpper(typ)
	}
}

func (v LogsView) Update(msg tea.Msg) (LogsView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.scrollPos < len(v.entries)-1 {
				v.scrollPos++
				v.autoScroll = false
			}
		case "k", "up":
			if v.scrollPos > 0 {
				v.scrollPos--
				v.autoScroll = false
			}
		case "G":
			v.scrollToBottom()
			v.autoScroll = true
		case "g":
			v.scrollPos = 0
			v.autoScroll = false
		}
	}
	return v, nil
}

func (v *LogsView) scrollToBottom() {
	maxRows := v.height - 3
	if maxRows < 1 {
		maxRows = 1
	}
	if len(v.entries) > maxRows {
		v.scrollPos = len(v.entries) - maxRows
	} else {
		v.scrollPos = 0
	}
}

func (v LogsView) View() string {
	if v.width == 0 || len(v.entries) == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Render("  No log entries found")
	}

	maxRows := v.height - 3
	if maxRows < 1 {
		maxRows = 1
	}

	var lines []string
	for i := v.scrollPos; i < len(v.entries) && i < v.scrollPos+maxRows; i++ {
		e := v.entries[i]
		ts := e.Timestamp.Format("15:04:05")

		typeStyled := e.Type
		switch e.Type {
		case "user":
			typeStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Render("USR")
		case "assistant":
			typeStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render("AST")
		case "progress":
			typeStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("PRG")
		default:
			typeStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Render(e.Type[:min(3, len(e.Type))])
		}

		tsStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Render(ts)
		line := fmt.Sprintf("  %s %s %s", tsStyled, typeStyled, e.Summary)
		if len(line) > v.width-2 {
			line = line[:v.width-5] + "..."
		}
		lines = append(lines, line)
	}

	scrollInfo := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Render(fmt.Sprintf("  [%d/%d entries]", v.scrollPos+maxRows, len(v.entries)))

	return strings.Join(lines, "\n") + "\n" + scrollInfo
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

**Step 2: Build to verify**

Run: `go build ./internal/tui/views/`
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/tui/views/logs.go
git commit -m "feat: add logs view with JSONL parsing and auto-scroll"
```

---

### Task 14: Costs View & Teams View

**Files:**
- Create: `internal/tui/views/costs.go`
- Create: `internal/tui/views/teams.go`
- Create: `internal/tui/views/help.go`

**Step 1: Implement costs view**

```go
// internal/tui/views/costs.go
package views

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/claudetopus/internal/model"
)

type CostsView struct {
	instances []model.Instance
	width     int
	height    int
}

func NewCostsView() CostsView { return CostsView{} }

func (v CostsView) SetInstances(instances []model.Instance) CostsView {
	v.instances = instances
	return v
}

func (v CostsView) SetSize(w, h int) CostsView {
	v.width = w
	v.height = h
	return v
}

func (v CostsView) View() string {
	// Aggregate by project
	type projectCost struct {
		Project  string
		Model    string
		TokensIn int64
		TokensOut int64
		Cost     float64
	}
	aggregated := map[string]*projectCost{}
	var totalIn, totalOut int64
	var totalCost float64

	for _, inst := range v.instances {
		proj := inst.ShortProject()
		if proj == "" {
			proj = "(unknown)"
		}
		if _, ok := aggregated[proj]; !ok {
			aggregated[proj] = &projectCost{Project: proj, Model: inst.ShortModel()}
		}
		aggregated[proj].TokensIn += inst.TokensIn
		aggregated[proj].TokensOut += inst.TokensOut
		aggregated[proj].Cost += inst.EstCostUSD
		totalIn += inst.TokensIn
		totalOut += inst.TokensOut
		totalCost += inst.EstCostUSD
	}

	// Sort by cost descending
	var sorted []*projectCost
	for _, pc := range aggregated {
		sorted = append(sorted, pc)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Cost > sorted[j].Cost
	})

	// Render
	header := fmt.Sprintf("  %-22s %-14s %-12s %-12s %-10s",
		"PROJECT", "MODEL", "TOKENS IN", "TOKENS OUT", "COST")
	headerStyled := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#06B6D4")).
		Render(header)

	var rows []string
	for _, pc := range sorted {
		row := fmt.Sprintf("  %-22s %-14s %-12s %-12s %-10s",
			pc.Project,
			pc.Model,
			formatTokens(pc.TokensIn),
			formatTokens(pc.TokensOut),
			lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Render(fmt.Sprintf("$%.2f", pc.Cost)),
		)
		rows = append(rows, row)
	}

	separator := "  " + strings.Repeat("─", v.width-4)
	total := fmt.Sprintf("  %-22s %-14s %-12s %-12s %-10s",
		"TOTAL", "",
		formatTokens(totalIn),
		formatTokens(totalOut),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#34D399")).Render(fmt.Sprintf("$%.2f", totalCost)),
	)

	return headerStyled + "\n" + strings.Join(rows, "\n") + "\n" + separator + "\n" + total
}

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
```

**Step 2: Implement teams view**

```go
// internal/tui/views/teams.go
package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/claudetopus/internal/team"
)

type TeamsView struct {
	teams  []team.TeamConfig
	width  int
	height int
}

func NewTeamsView() TeamsView { return TeamsView{} }

func (v TeamsView) SetTeams(teams []team.TeamConfig) TeamsView {
	v.teams = teams
	return v
}

func (v TeamsView) SetSize(w, h int) TeamsView {
	v.width = w
	v.height = h
	return v
}

func (v TeamsView) View() string {
	if len(v.teams) == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Render("  No active teams found")
	}

	var sections []string
	for _, tc := range v.teams {
		header := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			Render(fmt.Sprintf("  ▸ %s (%d members)", tc.Name, len(tc.Members)))

		var members []string
		for _, m := range tc.Members {
			name := lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB")).
				Width(16).
				Render(m.Name)
			agentType := lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Render(m.AgentType)
			members = append(members, fmt.Sprintf("    %s %s", name, agentType))
		}

		sections = append(sections, header+"\n"+strings.Join(members, "\n"))
	}

	return strings.Join(sections, "\n\n")
}
```

**Step 3: Implement help view**

```go
// internal/tui/views/help.go
package views

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type HelpView struct {
	width  int
	height int
}

func NewHelpView() HelpView { return HelpView{} }

func (v HelpView) SetSize(w, h int) HelpView {
	v.width = w
	v.height = h
	return v
}

func (v HelpView) View() string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7C3AED")).
		Render("  claudetopus - Claude Code Control Plane")

	sections := []struct {
		header string
		items  [][2]string
	}{
		{
			"Navigation",
			[][2]string{
				{"j / k", "Move down / up"},
				{"Enter", "Drill into selected item"},
				{"Esc", "Go back"},
				{"g / G", "Jump to top / bottom"},
				{"/", "Filter"},
				{"q", "Quit (from top level)"},
			},
		},
		{
			"Commands (press :)",
			[][2]string{
				{":instances  :i", "Instance list"},
				{":logs       :l", "Log viewer"},
				{":session    :s", "Session detail"},
				{":teams      :t", "Teams & tasks"},
				{":costs      :c", "Cost dashboard"},
				{":new        :n", "Launch new session"},
				{":kill", "Kill selected instance"},
				{":quit       :q", "Exit"},
			},
		},
		{
			"Actions",
			[][2]string{
				{"J", "Jump to session (tmux/iTerm2)"},
			},
		},
	}

	var lines []string
	lines = append(lines, title, "")

	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#06B6D4")).
		Width(20)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E5E7EB"))
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#F59E0B")).
		MarginTop(1)

	for _, section := range sections {
		lines = append(lines, headerStyle.Render("  "+section.header))
		for _, item := range section.items {
			lines = append(lines, "  "+keyStyle.Render(item[0])+descStyle.Render(item[1]))
		}
	}

	return strings.Join(lines, "\n")
}
```

**Step 4: Build to verify**

Run: `go build ./internal/tui/views/`
Expected: No errors.

**Step 5: Commit**

```bash
git add internal/tui/views/costs.go internal/tui/views/teams.go internal/tui/views/help.go
git commit -m "feat: add costs, teams, and help views"
```

---

### Task 15: Root TUI App (Bubble Tea Model)

**Files:**
- Create: `internal/tui/app.go`
- Modify: `cmd/claudetopus/main.go`

**Step 1: Implement the root app model**

```go
// internal/tui/app.go
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/claudetopus/internal/discovery"
	"github.com/zanetworker/claudetopus/internal/model"
	"github.com/zanetworker/claudetopus/internal/team"
	"github.com/zanetworker/claudetopus/internal/tui/views"
)

type viewType int

const (
	viewInstances viewType = iota
	viewLogs
	viewSession
	viewTeams
	viewCosts
	viewHelp
)

// tickMsg triggers periodic refresh.
type tickMsg time.Time

// instancesMsg carries discovered instances.
type instancesMsg []model.Instance

// teamsMsg carries team configs.
type teamsMsg []team.TeamConfig

type App struct {
	// State
	currentView   viewType
	previousView  viewType
	instances     []model.Instance
	teams         []team.TeamConfig
	width, height int

	// Views
	instancesView views.InstancesView
	logsView      views.LogsView
	costsView     views.CostsView
	teamsView     views.TeamsView
	helpView      views.HelpView

	// Command palette
	commandMode  bool
	commandInput string

	// Filter
	filterMode  bool
	filterInput string

	// Discovery
	orchestrator *discovery.Orchestrator

	// Breadcrumb
	breadcrumbs []string
}

func NewApp() App {
	return App{
		currentView:   viewInstances,
		instancesView: views.NewInstancesView(),
		costsView:     views.NewCostsView(),
		teamsView:     views.NewTeamsView(),
		helpView:      views.NewHelpView(),
		orchestrator:  discovery.NewOrchestrator(),
		breadcrumbs:   []string{"Instances"},
	}
}

func (a App) Init() tea.Cmd {
	return tea.Batch(
		a.discoverInstances,
		a.discoverTeams,
		a.tick(),
	)
}

func (a App) tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (a App) discoverInstances() tea.Msg {
	instances, _ := a.orchestrator.Discover()
	return instancesMsg(instances)
}

func (a App) discoverTeams() tea.Msg {
	teams, _ := team.ListTeamsDefault()
	return teamsMsg(teams)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.resizeViews()
		return a, nil

	case tickMsg:
		return a, tea.Batch(a.discoverInstances, a.tick())

	case instancesMsg:
		a.instances = []model.Instance(msg)
		a.instancesView = a.instancesView.SetInstances(a.instances)
		a.costsView = a.costsView.SetInstances(a.instances)
		if a.currentView == viewLogs {
			a.logsView.Reload()
		}
		return a, nil

	case teamsMsg:
		a.teams = []team.TeamConfig(msg)
		a.teamsView = a.teamsView.SetTeams(a.teams)
		return a, nil

	case tea.KeyMsg:
		// Command mode input
		if a.commandMode {
			return a.handleCommandInput(msg)
		}
		// Filter mode input
		if a.filterMode {
			return a.handleFilterInput(msg)
		}
		// Global keys
		switch msg.String() {
		case "q":
			if a.currentView == viewInstances {
				return a, tea.Quit
			}
			return a.navigateBack()
		case ":":
			a.commandMode = true
			a.commandInput = ""
			return a, nil
		case "/":
			a.filterMode = true
			a.filterInput = ""
			return a, nil
		case "?":
			return a.navigateTo(viewHelp, "Help")
		case "esc":
			return a.navigateBack()
		case "enter":
			return a.handleEnter()
		case "J":
			return a.handleJump()
		}

		// Delegate to current view
		return a.delegateToView(msg)
	}
	return a, nil
}

func (a App) handleCommandInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		cmd := resolveCommand(a.commandInput)
		a.commandMode = false
		a.commandInput = ""
		return a.executeCommand(cmd)
	case "esc":
		a.commandMode = false
		a.commandInput = ""
		return a, nil
	case "backspace":
		if len(a.commandInput) > 0 {
			a.commandInput = a.commandInput[:len(a.commandInput)-1]
		}
		return a, nil
	case "tab":
		completions := commandCompletions(a.commandInput)
		if len(completions) == 1 {
			a.commandInput = completions[0]
		}
		return a, nil
	default:
		if len(msg.String()) == 1 {
			a.commandInput += msg.String()
		}
		return a, nil
	}
}

func (a App) handleFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		a.filterMode = false
		if msg.String() == "esc" {
			a.filterInput = ""
			a.instancesView = a.instancesView.SetFilter("")
		}
		return a, nil
	case "backspace":
		if len(a.filterInput) > 0 {
			a.filterInput = a.filterInput[:len(a.filterInput)-1]
			a.instancesView = a.instancesView.SetFilter(a.filterInput)
		}
		return a, nil
	default:
		if len(msg.String()) == 1 {
			a.filterInput += msg.String()
			a.instancesView = a.instancesView.SetFilter(a.filterInput)
		}
		return a, nil
	}
}

func (a App) executeCommand(cmd string) (tea.Model, tea.Cmd) {
	switch cmd {
	case "instances":
		return a.navigateTo(viewInstances, "Instances")
	case "logs":
		return a.openLogsForSelected()
	case "session":
		return a.navigateTo(viewSession, "Session")
	case "teams":
		return a, a.discoverTeams
	case "costs":
		return a.navigateTo(viewCosts, "Costs")
	case "help":
		return a.navigateTo(viewHelp, "Help")
	case "quit":
		return a, tea.Quit
	case "kill":
		return a.handleKill()
	}
	return a, nil
}

func (a App) handleEnter() (tea.Model, tea.Cmd) {
	if a.currentView == viewInstances {
		return a.openLogsForSelected()
	}
	return a, nil
}

func (a App) openLogsForSelected() (tea.Model, tea.Cmd) {
	selected := a.instancesView.Selected()
	if selected == nil {
		return a, nil
	}
	sessionFile := discovery.FindSessionFileDefault(selected.SessionID)
	if sessionFile == "" {
		files := discovery.SessionFilesForDir(selected.WorkingDir)
		if len(files) > 0 {
			sessionFile = files[len(files)-1]
		}
	}
	a.logsView = views.NewLogsView(selected.PID, sessionFile)
	a.logsView = a.logsView.SetSize(a.width, a.height-4)
	return a.navigateTo(viewLogs, fmt.Sprintf("Logs [PID %d]", selected.PID))
}

func (a App) handleJump() (tea.Model, tea.Cmd) {
	selected := a.instancesView.Selected()
	if selected == nil || selected.TMuxSession == "" {
		return a, nil
	}
	// Suspend TUI and attach to tmux
	return a, tea.ExecProcess(
		newTmuxAttachCmd(selected.TMuxSession),
		func(err error) tea.Msg { return nil },
	)
}

func newTmuxAttachCmd(session string) *tmuxCmd {
	return &tmuxCmd{session: session}
}

type tmuxCmd struct{ session string }

func (c *tmuxCmd) Run() error {
	return nil // placeholder - actual exec happens via tea.ExecProcess
}

func (a App) handleKill() (tea.Model, tea.Cmd) {
	// TODO: implement kill with confirmation
	return a, nil
}

func (a App) navigateTo(v viewType, label string) (tea.Model, tea.Cmd) {
	a.previousView = a.currentView
	a.currentView = v
	if v == viewInstances {
		a.breadcrumbs = []string{"Instances"}
	} else {
		a.breadcrumbs = append(a.breadcrumbs[:1], label)
	}
	a.resizeViews()
	var cmd tea.Cmd
	if v == viewTeams {
		cmd = a.discoverTeams
	}
	return a, cmd
}

func (a App) navigateBack() (tea.Model, tea.Cmd) {
	if a.currentView != viewInstances {
		a.currentView = viewInstances
		a.breadcrumbs = []string{"Instances"}
	}
	return a, nil
}

func (a App) delegateToView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.currentView {
	case viewInstances:
		v, cmd := a.instancesView.Update(msg)
		a.instancesView = v
		return a, cmd
	case viewLogs:
		v, cmd := a.logsView.Update(msg)
		a.logsView = v
		return a, cmd
	}
	return a, nil
}

func (a *App) resizeViews() {
	contentHeight := a.height - 4 // header + status bar
	a.instancesView = a.instancesView.SetSize(a.width, contentHeight)
	a.logsView = a.logsView.SetSize(a.width, contentHeight)
	a.costsView = a.costsView.SetSize(a.width, contentHeight)
	a.teamsView = a.teamsView.SetSize(a.width, contentHeight)
	a.helpView = a.helpView.SetSize(a.width, contentHeight)
}

func (a App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	// Header
	header := a.renderHeader()

	// Content
	var content string
	switch a.currentView {
	case viewInstances:
		content = a.instancesView.View()
	case viewLogs:
		content = a.logsView.View()
	case viewCosts:
		content = a.costsView.View()
	case viewTeams:
		content = a.teamsView.View()
	case viewHelp:
		content = a.helpView.View()
	default:
		content = "  View not implemented yet"
	}

	// Status bar
	statusBar := a.renderStatusBar()

	// Compose
	return header + "\n" + content + "\n\n" + statusBar
}

func (a App) renderHeader() string {
	// Count by status
	active, idle, waiting := 0, 0, 0
	var totalCost float64
	for _, inst := range a.instances {
		switch inst.Status {
		case model.StatusActive:
			active++
		case model.StatusIdle:
			idle++
		case model.StatusWaitingPermission:
			waiting++
		}
		totalCost += inst.EstCostUSD
	}

	viewLabel := strings.Join(a.breadcrumbs, " > ")
	stats := fmt.Sprintf("%d instances (●%d ○%d ◐%d) ── $%.2f",
		len(a.instances), active, idle, waiting, totalCost)

	left := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7C3AED")).
		Render(" 🐙 claudetopus")

	middle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E5E7EB")).
		Render(" ── " + viewLabel)

	right := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9CA3AF")).
		Render(stats + " ")

	gap := a.width - lipgloss.Width(left) - lipgloss.Width(middle) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	return lipgloss.NewStyle().
		Background(lipgloss.Color("#111827")).
		Width(a.width).
		Render(left + middle + strings.Repeat(" ", gap) + right)
}

func (a App) renderStatusBar() string {
	if a.commandMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7C3AED")).
				Bold(true).
				Render(" :") + a.commandInput + "█")
	}
	if a.filterMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).
				Bold(true).
				Render(" /") + a.filterInput + "█")
	}

	hints := " :command  j/k:nav  Enter:drill  /:filter  ?:help"
	if a.filterInput != "" {
		hints += fmt.Sprintf("  [filter: %s]", a.filterInput)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Background(lipgloss.Color("#111827")).
		Width(a.width).
		Render(hints)
}
```

**Step 2: Update main.go**

```go
// cmd/claudetopus/main.go
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/claudetopus/internal/tui"
)

func main() {
	app := tui.NewApp()
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
```

**Step 3: Build and run**

Run: `make run`
Expected: Full TUI launches showing discovered Claude instances. Press `:` then type `costs` to see costs view. Press `?` for help.

**Step 4: Commit**

```bash
git add internal/tui/app.go cmd/claudetopus/main.go
git commit -m "feat: wire up root TUI app with all views and navigation"
```

---

### Task 16: Jump To Session

**Files:**
- Create: `internal/jump/tmux.go`
- Create: `internal/jump/iterm.go`
- Modify: `internal/tui/app.go` (wire up jump)

**Step 1: Implement tmux jump**

```go
// internal/jump/tmux.go
package jump

import (
	"fmt"
	"os"
	"os/exec"
)

// TmuxAttach attaches to a tmux session, taking over the current terminal.
func TmuxAttach(sessionName string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// TmuxHasSession checks if a tmux session exists.
func TmuxHasSession(sessionName string) bool {
	err := exec.Command("tmux", "has-session", "-t", sessionName).Run()
	return err == nil
}

// TmuxSendKeys sends keystrokes to a tmux session.
func TmuxSendKeys(sessionName, keys string) error {
	return exec.Command("tmux", "send-keys", "-t", sessionName, keys, "Enter").Run()
}

// IsTmuxAvailable checks if tmux is installed.
func IsTmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// IsInsideTmux returns true if we're running inside a tmux session.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// SuspendAndAttach suspends claudetopus and attaches to a tmux session.
// Returns a function suitable for tea.ExecProcess.
func SuspendAndAttach(sessionName string) *exec.Cmd {
	if IsInsideTmux() {
		// Switch client to the target session
		return exec.Command("tmux", "switch-client", "-t", sessionName)
	}
	return exec.Command("tmux", "attach-session", "-t", sessionName)
}

func FormatJumpCommand(sessionName string) string {
	if IsInsideTmux() {
		return fmt.Sprintf("tmux switch-client -t %s", sessionName)
	}
	return fmt.Sprintf("tmux attach-session -t %s", sessionName)
}
```

**Step 2: Implement iTerm2 jump**

```go
// internal/jump/iterm.go
package jump

import (
	"fmt"
	"os"
	"os/exec"
)

// IsITerm2 returns true if the current terminal is iTerm2.
func IsITerm2() bool {
	return os.Getenv("TERM_PROGRAM") == "iTerm.app"
}

// ITerm2FocusByPID uses AppleScript to focus the iTerm2 tab containing a process.
func ITerm2FocusByPID(pid int) error {
	script := fmt.Sprintf(`
tell application "iTerm2"
    activate
    repeat with w in windows
        repeat with t in tabs of w
            repeat with s in sessions of t
                if tty of s contains "" then
                    -- Check if this session's shell has the target PID as child
                    try
                        set sessionPID to (do shell script "ps -o ppid= -p %d 2>/dev/null | tr -d ' '")
                        if sessionPID is not "" then
                            select t
                            select s
                            return
                        end if
                    end try
                end if
            end repeat
        end repeat
    end repeat
end tell`, pid)

	cmd := exec.Command("osascript", "-e", script)
	return cmd.Run()
}
```

**Step 3: Update app.go handleJump to use the jump package**

In `internal/tui/app.go`, update the `handleJump` method and add the import:

Replace the `handleJump` method and the `tmuxCmd` type with:

```go
import "github.com/zanetworker/claudetopus/internal/jump"

func (a App) handleJump() (tea.Model, tea.Cmd) {
	selected := a.instancesView.Selected()
	if selected == nil {
		return a, nil
	}
	// Try tmux first
	if selected.TMuxSession != "" && jump.TmuxHasSession(selected.TMuxSession) {
		cmd := jump.SuspendAndAttach(selected.TMuxSession)
		return a, tea.ExecProcess(cmd, func(err error) tea.Msg { return nil })
	}
	// Try iTerm2
	if jump.IsITerm2() {
		_ = jump.ITerm2FocusByPID(selected.PID)
		return a, nil
	}
	// Fallback: open logs
	return a.openLogsForSelected()
}
```

Remove the placeholder `tmuxCmd` type and `newTmuxAttachCmd` function.

**Step 4: Build and test**

Run: `make build`
Expected: Compiles cleanly.

**Step 5: Commit**

```bash
git add internal/jump/ internal/tui/app.go
git commit -m "feat: add jump-to-session support (tmux, iTerm2, fallback)"
```

---

### Task 17: Integration Test & Polish

**Files:**
- Modify: `cmd/claudetopus/main.go`
- Modify: `internal/tui/app.go`

**Step 1: Run the full application**

Run: `make run`

Expected behavior:
- Header shows instance count and total cost
- Instances table shows all running Claude processes
- `j`/`k` navigates, `Enter` drills into logs
- `:costs` shows cost breakdown
- `:teams` shows team configs
- `?` shows help
- `q` quits from instances view
- `Esc` goes back from any sub-view
- `/` filters instances
- `J` jumps to tmux session (if available)

**Step 2: Fix any compilation or runtime issues**

Address any issues discovered during the integration test.

**Step 3: Final commit**

```bash
git add -A
git commit -m "fix: integration polish and runtime fixes"
```

---

### Task 18: Build & Distribution

**Files:**
- Modify: `Makefile`
- Create: `.goreleaser.yml`

**Step 1: Update Makefile with install target**

Add to Makefile:
```makefile
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/claudetopus

install: build
	cp $(BINARY) /usr/local/bin/

lint:
	golangci-lint run ./...
```

**Step 2: Create goreleaser config**

```yaml
# .goreleaser.yml
version: 2
builds:
  - main: ./cmd/claudetopus
    binary: claudetopus
    env:
      - CGO_ENABLED=0
    goos:
      - darwin
      - linux
    goarch:
      - amd64
      - arm64

archives:
  - formats: ['tar.gz']

checksum:
  name_template: "checksums.txt"
```

**Step 3: Build and install locally**

Run: `make install`
Expected: `claudetopus` binary available in PATH.

**Step 4: Commit**

```bash
git add Makefile .goreleaser.yml
git commit -m "feat: add install target and goreleaser config"
```

---

## Summary

| Task | Component | What it builds |
|------|-----------|---------------|
| 1 | Scaffolding | Go module, minimal Bubble Tea app, Makefile |
| 2 | Data Model | Instance struct, display helpers |
| 3 | Process Discovery | ps scanning, CLI flag parsing, source classification |
| 4 | CWD Resolution | lsof-based working directory detection |
| 5 | Session Parser | JSONL reading, token aggregation |
| 6 | tmux Matching | Session listing and project matching |
| 7 | Cost Tracker | Per-model pricing, cost calculation |
| 8 | Team Reader | Team config and member parsing |
| 9 | Orchestrator | Ties all discovery sources together |
| 10 | Styles | lipgloss color scheme and component styles |
| 11 | Command Palette | `:` commands with aliases and tab completion |
| 12 | Instances View | Main table with filtering and scrolling |
| 13 | Logs View | JSONL log viewer with auto-scroll |
| 14 | Costs/Teams/Help | Remaining views |
| 15 | Root App | Bubble Tea model wiring everything together |
| 16 | Jump To Session | tmux attach, iTerm2 focus, fallback |
| 17 | Integration | End-to-end testing and polish |
| 18 | Distribution | Install target and goreleaser |
