# Competitive Feature Sprint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement features inspired by competitive analysis, plus structural improvements for frontend parity and performance safety.

**Architecture:** Task 0 is a structural refactor that extracts business logic from `app.go` (3,108 lines) into `controller/` so all three frontends (TUI, web, CLI) call the same functions. Tasks 1-3 are complete. Tasks 4-5 are test infrastructure. Tasks 6-7 are config extensions. Task 8 is the runtime/sandbox abstraction.

**Tech Stack:** Go 1.25, Bubble Tea, lipgloss, cobra, YAML config, build tags

---

## File Map

| Feature | Files Created | Files Modified |
|---------|--------------|----------------|
| 0. Controller Refactor | `internal/controller/kill.go`, `internal/controller/kill_test.go`, `internal/controller/sort.go`, `internal/controller/sort_test.go`, `internal/controller/filter.go`, `internal/controller/filter_test.go`, `internal/controller/notify.go`, `internal/controller/notify_test.go`, `internal/controller/session_meta.go`, `internal/controller/session_meta_test.go` | `internal/frontend/tui/app.go`, `internal/frontend/tui/views/agents.go`, `internal/frontend/web/handlers.go` |
| 0B. Benchmarks | `internal/agent/fade_bench_test.go`, `internal/discovery/orchestrator_bench_test.go`, `internal/provider/claude_bench_test.go` | - |
| 1. Smart Attend | DONE | DONE |
| 2. Fading Colors | DONE | DONE |
| 3. Auto-Archive | DONE | DONE |
| 4. Build Tags | - | Multiple `*_test.go` files get build tags added |
| 5. E2E Tests | `internal/e2e/cli_test.go` | - |
| 6. Badges | `internal/badge/badge.go`, `internal/badge/badge_test.go` | `internal/config/config.go`, `internal/config/config_test.go`, `internal/agent/agent.go`, `internal/frontend/tui/views/agents.go` |
| 7. Project Config | `internal/config/project.go`, `internal/config/project_test.go` | `internal/config/config.go`, `internal/config/config_test.go` |
| 8A. Runtime Interface | `internal/runtime/runtime.go`, `internal/runtime/runtime_test.go`, `internal/runtime/local.go`, `internal/runtime/local_test.go` | - |
| 8B. Container Runtime | `internal/runtime/container.go`, `internal/runtime/container_test.go` | `internal/config/config.go` |
| 8C. Sandbox Policy (design) | `internal/runtime/policy.go`, `internal/runtime/policy_test.go`, `internal/runtime/openshell.go`, `internal/runtime/openshell_test.go` | - |

---

## Task 0: Controller Refactor (extract from app.go)

Extract business logic from `app.go` (3,108 lines) into `controller/` so TUI, Web API, and CLI all call the same functions. This is the structural fix that prevents future parity drift.

**Extraction targets (6 operations):**

### 0.1: Sort Agents [P]

**Files:**
- Create: `internal/controller/sort.go`
- Create: `internal/controller/sort_test.go`
- Modify: `internal/frontend/tui/views/agents.go` (call `controller.SortAgents` instead of inline sort)

- [ ] **Step 1: Write failing test**

```go
// internal/controller/sort_test.go
package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestSortAgents_DefaultStatusThenName(t *testing.T) {
	agents := []agent.Agent{
		{Name: "zzz", Status: agent.StatusIdle},
		{Name: "aaa", Status: agent.StatusActive},
		{Name: "mmm", Status: agent.StatusActive},
	}
	SortAgents(agents, "")
	if agents[0].Name != "aaa" || agents[1].Name != "mmm" || agents[2].Name != "zzz" {
		t.Errorf("default sort: active first then alpha, got %s %s %s", agents[0].Name, agents[1].Name, agents[2].Name)
	}
}

func TestSortAgents_ByCost(t *testing.T) {
	agents := []agent.Agent{
		{Name: "low", EstCostUSD: 1.0},
		{Name: "high", EstCostUSD: 10.0},
	}
	SortAgents(agents, "cost")
	if agents[0].Name != "high" {
		t.Errorf("cost sort: highest first, got %s", agents[0].Name)
	}
}

func TestSortAgents_ByAge(t *testing.T) {
	now := time.Now()
	agents := []agent.Agent{
		{Name: "new", StartTime: now},
		{Name: "old", StartTime: now.Add(-1 * time.Hour)},
	}
	SortAgents(agents, "age")
	if agents[0].Name != "old" {
		t.Errorf("age sort: oldest first, got %s", agents[0].Name)
	}
}

func TestSortAgents_Stable(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Name: "aaa", Status: agent.StatusActive},
		{PID: 2, Name: "aaa", Status: agent.StatusActive},
	}
	SortAgents(agents, "")
	if agents[0].PID != 1 || agents[1].PID != 2 {
		t.Error("sort must be stable: equal elements keep original order")
	}
}
```

- [ ] **Step 2: Implement**

```go
// internal/controller/sort.go
package controller

import (
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// SortAgents sorts agents in place by the given field using stable sort.
// Valid fields: "", "name", "cost", "cpu", "mem", "age", "model".
// Default ("") sorts by status priority then name.
func SortAgents(agents []agent.Agent, field string) {
	switch field {
	case "name":
		sort.SliceStable(agents, func(i, j int) bool {
			return strings.ToLower(agents[i].ShortProject()) < strings.ToLower(agents[j].ShortProject())
		})
	case "cost":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].EstCostUSD > agents[j].EstCostUSD
		})
	case "cpu":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].CPUPercent > agents[j].CPUPercent
		})
	case "mem":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].MemoryMB > agents[j].MemoryMB
		})
	case "age":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].AgeTime().Before(agents[j].AgeTime())
		})
	case "model":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].ShortModel() < agents[j].ShortModel()
		})
	default:
		sort.SliceStable(agents, func(i, j int) bool {
			si, sj := agents[i].Status, agents[j].Status
			if si != sj {
				return si < sj
			}
			return strings.ToLower(agents[i].ShortProject()) < strings.ToLower(agents[j].ShortProject())
		})
	}
}
```

- [ ] **Step 3: Wire into AgentsView.SetAgents (replace inline sort)**
- [ ] **Step 4: Run tests, verify no change in behavior**

### 0.2: Filter Agents [P]

**Files:**
- Create: `internal/controller/filter.go`
- Create: `internal/controller/filter_test.go`
- Modify: `internal/frontend/tui/views/agents.go` (call `controller.FilterAgents`)

- [ ] **Step 1: Write failing test**

```go
// internal/controller/filter_test.go
package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestFilterAgents_ByName(t *testing.T) {
	agents := []agent.Agent{
		{Name: "aimux"},
		{Name: "showtime"},
	}
	result := FilterAgents(agents, "aimux")
	if len(result) != 1 || result[0].Name != "aimux" {
		t.Errorf("expected 1 match, got %d", len(result))
	}
}

func TestFilterAgents_CaseInsensitive(t *testing.T) {
	agents := []agent.Agent{{Name: "AiMux"}}
	result := FilterAgents(agents, "aimux")
	if len(result) != 1 {
		t.Error("filter should be case-insensitive")
	}
}

func TestFilterAgents_ByModel(t *testing.T) {
	agents := []agent.Agent{{Model: "claude-opus-4-6[1m]"}}
	result := FilterAgents(agents, "opus")
	if len(result) != 1 {
		t.Error("filter should match model")
	}
}

func TestFilterAgents_Empty(t *testing.T) {
	agents := []agent.Agent{{Name: "test"}}
	result := FilterAgents(agents, "")
	if len(result) != 1 {
		t.Error("empty filter should return all")
	}
}
```

- [ ] **Step 2: Implement**

```go
// internal/controller/filter.go
package controller

import (
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// FilterAgents returns agents matching the query (case-insensitive).
// Matches against project name, model, status, source, provider, dir, and last action.
// Empty query returns all agents.
func FilterAgents(agents []agent.Agent, query string) []agent.Agent {
	if query == "" {
		return agents
	}
	q := strings.ToLower(query)
	var out []agent.Agent
	for _, a := range agents {
		if strings.Contains(strings.ToLower(a.ShortProject()), q) ||
			strings.Contains(strings.ToLower(a.ShortModel()), q) ||
			strings.Contains(strings.ToLower(a.Status.String()), q) ||
			strings.Contains(strings.ToLower(a.Source.String()), q) ||
			strings.Contains(strings.ToLower(a.ProviderName), q) ||
			strings.Contains(strings.ToLower(a.ShortDir()), q) ||
			strings.Contains(strings.ToLower(a.LastAction), q) {
			out = append(out, a)
		}
	}
	return out
}
```

- [ ] **Step 3: Replace `AgentsView.filtered()` with `controller.FilterAgents`**
- [ ] **Step 4: Run tests**

### 0.3: Notify Logic [P]

**Files:**
- Create: `internal/controller/notify.go`
- Create: `internal/controller/notify_test.go`
- Modify: `internal/frontend/tui/app.go` (call `controller.ShouldNotify`)

- [ ] **Step 1: Write test**

```go
// internal/controller/notify_test.go
package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

func TestShouldNotify_WaitingEnabled(t *testing.T) {
	cfg := config.NotificationsConfig{Enabled: true, OnWaiting: true}
	msg := ShouldNotify(agent.StatusWaitingPermission, "myapp", cfg)
	if msg == nil {
		t.Error("expected notification for waiting status")
	}
	if msg.Title != "aimux: myapp" {
		t.Errorf("unexpected title: %s", msg.Title)
	}
}

func TestShouldNotify_WaitingDisabled(t *testing.T) {
	cfg := config.NotificationsConfig{Enabled: true, OnWaiting: false}
	msg := ShouldNotify(agent.StatusWaitingPermission, "myapp", cfg)
	if msg != nil {
		t.Error("should not notify when on_waiting is false")
	}
}

func TestShouldNotify_MasterDisabled(t *testing.T) {
	cfg := config.NotificationsConfig{Enabled: false, OnWaiting: true}
	msg := ShouldNotify(agent.StatusWaitingPermission, "myapp", cfg)
	if msg != nil {
		t.Error("should not notify when master switch is off")
	}
}

func TestShouldNotify_ActiveNoNotify(t *testing.T) {
	cfg := config.NotificationsConfig{Enabled: true, OnWaiting: true, OnError: true, OnIdle: true}
	msg := ShouldNotify(agent.StatusActive, "myapp", cfg)
	if msg != nil {
		t.Error("active status should never trigger notification")
	}
}
```

- [ ] **Step 2: Implement**

```go
// internal/controller/notify.go
package controller

import (
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

// Notification describes a notification to send.
type Notification struct {
	Title   string
	Message string
	Sound   bool
}

// ShouldNotify returns a Notification if the agent status warrants one,
// or nil if no notification should fire. The TUI/web layer is responsible
// for actually delivering it (macOS notification, SSE event, etc.).
func ShouldNotify(status agent.Status, projectName string, cfg config.NotificationsConfig) *Notification {
	if !cfg.Enabled {
		return nil
	}
	title := "aimux: " + projectName

	switch status {
	case agent.StatusWaitingPermission:
		if !cfg.OnWaiting {
			return nil
		}
		return &Notification{Title: title, Message: "Needs permission", Sound: cfg.Sound}
	case agent.StatusError:
		if !cfg.OnError {
			return nil
		}
		return &Notification{Title: title, Message: "Agent error", Sound: cfg.Sound}
	case agent.StatusIdle:
		if !cfg.OnIdle {
			return nil
		}
		return &Notification{Title: title, Message: "Finished", Sound: false}
	default:
		return nil
	}
}
```

- [ ] **Step 3: Replace `maybeNotify` in `app.go` with `controller.ShouldNotify`**
- [ ] **Step 4: Run tests**

### 0.4: Session Meta Operations [P]

**Files:**
- Create: `internal/controller/session_meta.go`
- Create: `internal/controller/session_meta_test.go`

- [ ] **Step 1: Write test**

```go
// internal/controller/session_meta_test.go
package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToggleStar(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "session.jsonl")
	os.WriteFile(file, []byte("{}"), 0644)

	starred, err := ToggleStar(file)
	if err != nil {
		t.Fatal(err)
	}
	if !starred {
		t.Error("first toggle should star")
	}
	starred, err = ToggleStar(file)
	if err != nil {
		t.Fatal(err)
	}
	if starred {
		t.Error("second toggle should unstar")
	}
}
```

- [ ] **Step 2: Implement**

```go
// internal/controller/session_meta.go
package controller

import "github.com/zanetworker/aimux/internal/history"

// ToggleStar flips the starred state of a session file. Returns the new state.
func ToggleStar(sessionFile string) (bool, error) {
	meta := history.LoadMeta(sessionFile)
	meta.Starred = !meta.Starred
	if err := history.SaveMeta(sessionFile, meta); err != nil {
		return false, err
	}
	return meta.Starred, nil
}

// SetAnnotation sets the session-level annotation.
func SetAnnotation(sessionFile, annotation string) error {
	meta := history.LoadMeta(sessionFile)
	meta.Annotation = annotation
	return history.SaveMeta(sessionFile, meta)
}

// SetTags replaces session tags.
func SetTags(sessionFile string, tags []string) error {
	meta := history.LoadMeta(sessionFile)
	meta.Tags = tags
	return history.SaveMeta(sessionFile, meta)
}

// SetNote sets the session note.
func SetNote(sessionFile, note string) error {
	meta := history.LoadMeta(sessionFile)
	meta.Note = note
	return history.SaveMeta(sessionFile, meta)
}
```

- [ ] **Step 3: Replace inline meta logic in `app.go` (`*` key handler, sessions view)**
- [ ] **Step 4: Run tests**

### 0.5: Kill Agent [P]

**Files:**
- Create: `internal/controller/kill.go`
- Create: `internal/controller/kill_test.go`

- [ ] **Step 1: Write test**

```go
// internal/controller/kill_test.go
package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestKillAction_LocalProcess(t *testing.T) {
	ag := agent.Agent{PID: 12345, Name: "test"}
	action := KillAction(ag)
	if action.Type != KillProcess {
		t.Errorf("expected KillProcess, got %v", action.Type)
	}
}

func TestKillAction_K8sPod(t *testing.T) {
	ag := agent.Agent{PID: 0, SessionID: "pod-my-agent-0", WorkingDir: "k8s://agents/repo"}
	action := KillAction(ag)
	if action.Type != KillPod {
		t.Errorf("expected KillPod, got %v", action.Type)
	}
	if action.PodName != "my-agent-0" {
		t.Errorf("expected pod name 'my-agent-0', got %q", action.PodName)
	}
	if action.Namespace != "agents" {
		t.Errorf("expected namespace 'agents', got %q", action.Namespace)
	}
}

func TestKillAction_SessionOnly(t *testing.T) {
	ag := agent.Agent{PID: 0, SessionID: "", SessionFile: "/tmp/session.jsonl"}
	action := KillAction(ag)
	if action.Type != KillRemoveOnly {
		t.Errorf("expected KillRemoveOnly, got %v", action.Type)
	}
}
```

- [ ] **Step 2: Implement**

```go
// internal/controller/kill.go
package controller

import (
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// KillType describes how to kill/remove an agent.
type KillType int

const (
	KillProcess    KillType = iota // local process: SIGTERM
	KillPod                       // K8s pod: kubectl delete + scale down
	KillRemoveOnly                // session-only: just hide from view
)

// KillAction describes what to do when the user requests killing an agent.
type KillAction struct {
	Type      KillType
	PodName   string // for KillPod
	Namespace string // for KillPod
}

// DetermineKillAction inspects an agent and returns the appropriate kill action.
func DetermineKillAction(ag agent.Agent) KillAction {
	if strings.HasPrefix(ag.SessionID, "pod-") {
		podName := strings.TrimPrefix(ag.SessionID, "pod-")
		namespace := "agents"
		if parts := strings.SplitN(strings.TrimPrefix(ag.WorkingDir, "k8s://"), "/", 2); len(parts) == 2 {
			namespace = parts[0]
		}
		return KillAction{Type: KillPod, PodName: podName, Namespace: namespace}
	}
	if ag.PID == 0 {
		return KillAction{Type: KillRemoveOnly}
	}
	return KillAction{Type: KillProcess}
}
```

- [ ] **Step 3: Replace pod/process detection in `handleKillConfirm` with `controller.DetermineKillAction`**
- [ ] **Step 4: Run tests**

### 0.6: Commit and verify

- [ ] **Step 1: Run full suite**

```bash
go build ./... && go vet ./... && go test ./... -timeout 30s
```

- [ ] **Step 2: Verify app.go shrank**

`wc -l internal/frontend/tui/app.go` should be noticeably smaller (target: <2,800 lines).

- [ ] **Step 3: Commit**

```bash
git add internal/controller/ internal/frontend/tui/
git commit -m "refactor: extract sort, filter, notify, kill, session_meta from app.go to controller"
```

---

## Task 0B: Benchmark Suite [P]

Establish performance baselines for hot paths before adding more features.

**Files:**
- Create: `internal/agent/fade_bench_test.go`
- Create: `internal/discovery/orchestrator_bench_test.go`
- Create: `internal/provider/claude_bench_test.go`

- [ ] **Step 1: FadeHex benchmark**

```go
// internal/agent/fade_bench_test.go
package agent

import (
	"testing"
	"time"
)

func BenchmarkFadeHex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		FadeHex("#9CA3AF", "#374151", 30*time.Minute, 1*time.Hour)
	}
}

func BenchmarkStatusFadeColor(b *testing.B) {
	activity := time.Now().Add(-30 * time.Minute)
	for i := 0; i < b.N; i++ {
		StatusFadeColor(StatusIdle, activity)
	}
}
```

- [ ] **Step 2: Discovery orchestrator benchmark**

```go
// internal/discovery/orchestrator_bench_test.go
package discovery

import "testing"

func BenchmarkDiscover_NoProviders(b *testing.B) {
	o := NewOrchestrator()
	for i := 0; i < b.N; i++ {
		o.Discover()
	}
}
```

- [ ] **Step 3: Trace parsing benchmark**

```go
// internal/provider/claude_bench_test.go
package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkParseTrace_Small(b *testing.B) {
	// Create a minimal JSONL file with 10 turns
	dir := b.TempDir()
	file := filepath.Join(dir, "session.jsonl")
	content := ""
	for i := 0; i < 10; i++ {
		content += `{"type":"human","message":{"content":"hello"}}` + "\n"
		content += `{"type":"assistant","message":{"content":"world"}}` + "\n"
	}
	os.WriteFile(file, []byte(content), 0644)

	c := &Claude{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.ParseTrace(file)
	}
}
```

- [ ] **Step 4: Run benchmarks**

```bash
go test -bench=. -benchmem ./internal/agent/ ./internal/discovery/ ./internal/provider/ 2>&1 | tee benchmarks-baseline.txt
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/fade_bench_test.go internal/discovery/orchestrator_bench_test.go internal/provider/claude_bench_test.go benchmarks-baseline.txt
git commit -m "perf: add benchmark suite for fade, discovery, and trace parsing"
```

---

## Task 0C: Parity Enforcement Rule

Add a rule to `.claude/CLAUDE.md` that prevents future parity drift.

- [ ] **Step 1: Add rule**

Append to the "Architecture Rules" section in `.claude/CLAUDE.md`:

```markdown
## Frontend Parity Rule

Every new feature that involves user-visible behavior MUST follow this pattern:

1. **Business logic in `controller/`** — the pure function that does the work (no bubbletea, no lipgloss, no HTTP types)
2. **TUI wires the key** — `app.go` calls the controller function on keypress
3. **Web API wires the endpoint** — `handlers.go` calls the same controller function on HTTP request
4. **CLI wires the flag** — `cmd/*.go` calls the same controller function on flag

If a feature exists in only one frontend, it's tech debt. Track it.

Before merging any PR that adds a keybinding to `app.go`, verify:
- The logic is in `controller/` (or another core package), not inline in `app.go`
- There is a corresponding web API endpoint (even if the React UI doesn't consume it yet)
- The controller function has tests independent of any UI framework
```

- [ ] **Step 2: Commit**

```bash
git add .claude/CLAUDE.md
git commit -m "docs: add frontend parity enforcement rule"
```

---

## Task 1: Smart Attend Cycling [P] -- DONE

Business logic in `controller/attend.go` (UI-agnostic). TUI wires `a` key to call it.

**Files:**
- Create: `internal/controller/attend.go`
- Create: `internal/controller/attend_test.go`
- Modify: `internal/frontend/tui/app.go:1000-1200` (handleKey)

- [ ] **Step 1: Write the failing test for NextAttend**

```go
// internal/controller/attend_test.go
package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestNextAttend_WaitingFirst(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, Name: "working"},
		{PID: 2, Status: agent.StatusWaitingPermission, Name: "waiting"},
		{PID: 3, Status: agent.StatusIdle, Name: "idle"},
	}
	idx := NextAttend(agents, -1)
	if idx != 1 {
		t.Errorf("expected index 1 (waiting), got %d", idx)
	}
}

func TestNextAttend_SkipsActive(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, Name: "a"},
		{PID: 2, Status: agent.StatusActive, Name: "b"},
	}
	idx := NextAttend(agents, -1)
	if idx != -1 {
		t.Errorf("expected -1 (nothing needs attention), got %d", idx)
	}
}

func TestNextAttend_CyclesFromCurrent(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle, Name: "idle1"},
		{PID: 2, Status: agent.StatusIdle, Name: "idle2"},
		{PID: 3, Status: agent.StatusActive, Name: "active"},
	}
	// Start from idle1 (index 0), should advance to idle2 (index 1)
	idx := NextAttend(agents, 0)
	if idx != 1 {
		t.Errorf("expected index 1, got %d", idx)
	}
}

func TestNextAttend_WrapsAround(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle, Name: "idle1"},
		{PID: 2, Status: agent.StatusActive, Name: "active"},
		{PID: 3, Status: agent.StatusIdle, Name: "idle2"},
	}
	// Start from idle2 (index 2), should wrap to idle1 (index 0)
	idx := NextAttend(agents, 2)
	if idx != 0 {
		t.Errorf("expected index 0 (wrap), got %d", idx)
	}
}

func TestNextAttend_PriorityOrder(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle, Name: "idle"},
		{PID: 2, Status: agent.StatusError, Name: "error"},
		{PID: 3, Status: agent.StatusWaitingPermission, Name: "waiting"},
	}
	// Waiting has highest priority, should be returned first regardless of position
	idx := NextAttend(agents, -1)
	if idx != 2 {
		t.Errorf("expected index 2 (waiting), got %d", idx)
	}
}

func TestNextAttend_Empty(t *testing.T) {
	idx := NextAttend(nil, -1)
	if idx != -1 {
		t.Errorf("expected -1 for empty list, got %d", idx)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -timeout 30s -run TestNextAttend -v`
Expected: FAIL with "undefined: NextAttend"

- [ ] **Step 3: Write the implementation**

```go
// internal/controller/attend.go
package controller

import "github.com/zanetworker/aimux/internal/agent"

// attendPriority returns the urgency tier for an agent status.
// Lower number = higher urgency. Active agents return -1 (skip).
func attendPriority(s agent.Status) int {
	switch s {
	case agent.StatusWaitingPermission:
		return 0
	case agent.StatusError:
		return 1
	case agent.StatusIdle:
		return 2
	case agent.StatusUnknown:
		return 3
	default:
		return -1 // Active: skip
	}
}

// NextAttend returns the index of the next agent that needs attention,
// cycling from the current position. Priority: Waiting > Error > Idle > Unknown.
// Active agents are always skipped. Returns -1 if nothing needs attention.
func NextAttend(agents []agent.Agent, currentIdx int) int {
	if len(agents) == 0 {
		return -1
	}

	// First pass: find the highest-priority tier that has candidates
	bestTier := 100
	for _, a := range agents {
		p := attendPriority(a.Status)
		if p >= 0 && p < bestTier {
			bestTier = p
		}
	}
	if bestTier == 100 {
		return -1
	}

	// Second pass: cycle through agents of that tier starting after currentIdx
	n := len(agents)
	start := currentIdx + 1
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		if attendPriority(agents[idx].Status) == bestTier {
			return idx
		}
	}
	return -1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -timeout 30s -run TestNextAttend -v`
Expected: all 6 tests PASS

- [ ] **Step 5: Wire into TUI**

In `internal/frontend/tui/app.go`, inside `handleKey()`, add a case for `"a"` after the existing `"*"` case (~line 1056):

```go
	case "a":
		if a.currentView == viewAgents {
			idx := controller.NextAttend(a.instances, a.agentsView.Cursor())
			if idx >= 0 {
				a.agentsView.SetCursor(idx)
				ag := a.instances[idx]
				a.statusHint = fmt.Sprintf("Attend: %s (%s)", ag.ShortProject(), ag.Status)
			} else {
				a.statusHint = "No agents need attention"
			}
			return a, nil
		}
```

Add `SetCursor` method to `AgentsView` in `internal/frontend/tui/views/agents.go`:

```go
// SetCursor moves the cursor to a specific row index, clamped to bounds.
func (v *AgentsView) SetCursor(idx int) {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(v.rows) {
		idx = len(v.rows) - 1
	}
	v.cursor = idx
	if idx >= 0 && idx < len(v.rows) {
		v.selectedPID = v.rows[idx].agent.PID
	}
}
```

- [ ] **Step 6: Update hints**

In `app.go` `updateHints()`, add `a:attend` to the agents view hint string.

- [ ] **Step 7: Run full suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: all pass

- [ ] **Step 8: Commit**

```bash
git add internal/controller/attend.go internal/controller/attend_test.go internal/frontend/tui/app.go internal/frontend/tui/views/agents.go
git commit -m "feat: add smart attend cycling (a key) for urgency-based agent navigation"
```

---

## Task 2: Fading Status Colors [P]

Color interpolation in `agent/fade.go` (UI-agnostic math). TUI calls it for rendering.

**Files:**
- Create: `internal/agent/fade.go`
- Create: `internal/agent/fade_test.go`
- Modify: `internal/frontend/tui/views/agents.go:526-540` (renderStatusIcon)

- [ ] **Step 1: Write the failing test for FadeColor**

```go
// internal/agent/fade_test.go
package agent

import (
	"testing"
	"time"
)

func TestFadeHex_ZeroElapsed(t *testing.T) {
	hex := FadeHex("#22C55E", "#6B7280", 0, 5*time.Minute)
	if hex != "#22C55E" {
		t.Errorf("expected start color, got %s", hex)
	}
}

func TestFadeHex_FullyElapsed(t *testing.T) {
	hex := FadeHex("#22C55E", "#6B7280", 5*time.Minute, 5*time.Minute)
	if hex != "#6B7280" {
		t.Errorf("expected end color, got %s", hex)
	}
}

func TestFadeHex_OverElapsed(t *testing.T) {
	hex := FadeHex("#22C55E", "#6B7280", 10*time.Minute, 5*time.Minute)
	if hex != "#6B7280" {
		t.Errorf("expected end color when over duration, got %s", hex)
	}
}

func TestFadeHex_Midpoint(t *testing.T) {
	hex := FadeHex("#000000", "#FFFFFF", 30*time.Second, 1*time.Minute)
	// At 50% linear, sqrt(0.5) = ~0.707, so ~70% through
	// Each channel: 0 + 0.707 * 255 = ~180 = 0xB4
	if len(hex) != 7 || hex[0] != '#' {
		t.Errorf("expected valid hex color, got %s", hex)
	}
}

func TestStatusFade_Active(t *testing.T) {
	fg := StatusFadeColor(StatusActive, time.Time{})
	if fg != "" {
		t.Errorf("active agents should not fade, got %s", fg)
	}
}

func TestStatusFade_Idle_Fresh(t *testing.T) {
	fg := StatusFadeColor(StatusIdle, time.Now())
	if fg != "#9CA3AF" {
		t.Errorf("fresh idle should be bright grey, got %s", fg)
	}
}

func TestStatusFade_Idle_Stale(t *testing.T) {
	fg := StatusFadeColor(StatusIdle, time.Now().Add(-2*time.Hour))
	if fg != "#374151" {
		t.Errorf("stale idle should be dark grey, got %s", fg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -timeout 30s -run TestFade -v`
Expected: FAIL with "undefined: FadeHex"

- [ ] **Step 3: Write the implementation**

```go
// internal/agent/fade.go
package agent

import (
	"fmt"
	"math"
	"time"
)

// FadeHex interpolates between two hex colors (#RRGGBB) using a square-root
// curve for perceptual smoothness. elapsed/duration controls progress (0..1).
func FadeHex(startHex, endHex string, elapsed, duration time.Duration) string {
	if duration <= 0 || elapsed >= duration {
		return endHex
	}
	if elapsed <= 0 {
		return startHex
	}

	t := math.Sqrt(float64(elapsed) / float64(duration))

	sr, sg, sb := parseHex(startHex)
	er, eg, eb := parseHex(endHex)

	r := sr + int(t*float64(er-sr))
	g := sg + int(t*float64(eg-sg))
	b := sb + int(t*float64(eb-sb))

	return fmt.Sprintf("#%02X%02X%02X", clamp(r), clamp(g), clamp(b))
}

func parseHex(hex string) (int, int, int) {
	if len(hex) != 7 {
		return 0, 0, 0
	}
	var r, g, b int
	fmt.Sscanf(hex[1:], "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// StatusFadeColor returns a foreground hex color for a status icon, accounting
// for time-based fading. Returns "" for statuses that don't fade (Active, Waiting).
//
// Fade rules:
//   - Idle: #9CA3AF -> #374151 over 1 hour
//   - Error: no fade (always bright red)
//   - Active, Waiting: no fade (return "")
func StatusFadeColor(s Status, lastActivity time.Time) string {
	switch s {
	case StatusIdle:
		if lastActivity.IsZero() {
			return "#374151" // fully faded
		}
		return FadeHex("#9CA3AF", "#374151", time.Since(lastActivity), 1*time.Hour)
	default:
		return ""
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -timeout 30s -run TestFade -v && go test ./internal/agent/ -timeout 30s -run TestStatus -v`
Expected: all tests PASS

- [ ] **Step 5: Wire fading into agents view**

In `internal/frontend/tui/views/agents.go`, modify `renderStatusIcon` (~line 526):

```go
func (v *AgentsView) renderStatusIcon(s agent.Status, lastActivity time.Time) string {
	icon := s.Icon()

	fadeColor := agent.StatusFadeColor(s, lastActivity)
	if fadeColor != "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(fadeColor)).Render(icon)
	}

	switch s {
	case agent.StatusActive:
		return agentActiveIcon.Render(icon)
	case agent.StatusIdle:
		return agentIdleIcon.Render(icon)
	case agent.StatusWaitingPermission:
		return agentWaitingIcon.Render(icon)
	case agent.StatusError:
		return agentErrorIcon.Render(icon)
	default:
		return agentMutedIcon.Render(icon)
	}
}
```

Update the call site in `renderRow` (~line 503) to pass `r.agent.LastActivity`:

```go
status := v.renderStatusIcon(r.agent.Status, r.agent.LastActivity)
```

- [ ] **Step 6: Run full suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: all pass

- [ ] **Step 7: Commit**

```bash
git add internal/agent/fade.go internal/agent/fade_test.go internal/frontend/tui/views/agents.go
git commit -m "feat: add time-aware fading for idle agent status icons"
```

---

## Task 3: Auto-Archive Idle Agents [P]

Archive logic in `controller/archive.go`. Config in `config.go`. TUI wires toggle.

**Files:**
- Create: `internal/controller/archive.go`
- Create: `internal/controller/archive_test.go`
- Modify: `internal/config/config.go` (add `AutoArchiveAfter` field)
- Modify: `internal/frontend/tui/app.go` (apply archive filter, `o` key toggle)
- Modify: `internal/frontend/tui/views/agents.go` (render archived section count)

- [ ] **Step 1: Write the failing test for PartitionByArchive**

```go
// internal/controller/archive_test.go
package controller

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestPartitionByArchive_SplitsCorrectly(t *testing.T) {
	now := time.Now()
	threshold := 1 * time.Hour
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, LastActivity: now},
		{PID: 2, Status: agent.StatusIdle, LastActivity: now.Add(-2 * time.Hour)},
		{PID: 3, Status: agent.StatusIdle, LastActivity: now.Add(-30 * time.Minute)},
		{PID: 4, Status: agent.StatusWaitingPermission, LastActivity: now.Add(-3 * time.Hour)},
	}
	active, archived := PartitionByArchive(agents, threshold)
	if len(active) != 3 {
		t.Errorf("expected 3 active, got %d", len(active))
	}
	if len(archived) != 1 {
		t.Errorf("expected 1 archived, got %d", len(archived))
	}
	if archived[0].PID != 2 {
		t.Errorf("expected PID 2 archived, got %d", archived[0].PID)
	}
}

func TestPartitionByArchive_ActiveNeverArchived(t *testing.T) {
	stale := time.Now().Add(-10 * time.Hour)
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusActive, LastActivity: stale},
	}
	active, archived := PartitionByArchive(agents, 1*time.Hour)
	if len(active) != 1 {
		t.Errorf("active agents should never be archived")
	}
	if len(archived) != 0 {
		t.Errorf("expected 0 archived, got %d", len(archived))
	}
}

func TestPartitionByArchive_WaitingNeverArchived(t *testing.T) {
	stale := time.Now().Add(-10 * time.Hour)
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusWaitingPermission, LastActivity: stale},
	}
	active, archived := PartitionByArchive(agents, 1*time.Hour)
	if len(active) != 1 {
		t.Errorf("waiting agents should never be archived")
	}
	if len(archived) != 0 {
		t.Errorf("expected 0 archived, got %d", len(archived))
	}
}

func TestPartitionByArchive_ZeroThreshold(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1, Status: agent.StatusIdle, LastActivity: time.Now().Add(-1 * time.Second)},
	}
	active, archived := PartitionByArchive(agents, 0)
	if len(active) != 1 {
		t.Errorf("zero threshold should disable archiving")
	}
	if len(archived) != 0 {
		t.Errorf("expected 0 archived with zero threshold")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -timeout 30s -run TestPartition -v`
Expected: FAIL with "undefined: PartitionByArchive"

- [ ] **Step 3: Write the implementation**

```go
// internal/controller/archive.go
package controller

import (
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

// PartitionByArchive splits agents into active and archived based on idle
// duration. Only Idle and Error agents can be archived. Active and Waiting
// agents are never archived. A zero threshold disables archiving.
func PartitionByArchive(agents []agent.Agent, threshold time.Duration) (active, archived []agent.Agent) {
	if threshold <= 0 {
		return agents, nil
	}
	now := time.Now()
	for _, a := range agents {
		if canArchive(a.Status) && !a.LastActivity.IsZero() && now.Sub(a.LastActivity) > threshold {
			archived = append(archived, a)
		} else {
			active = append(active, a)
		}
	}
	return active, archived
}

func canArchive(s agent.Status) bool {
	return s == agent.StatusIdle || s == agent.StatusError || s == agent.StatusUnknown
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -timeout 30s -run TestPartition -v`
Expected: all 4 tests PASS

- [ ] **Step 5: Add config field**

In `internal/config/config.go`, add to `Config` struct:

```go
	AutoArchiveAfter string `yaml:"auto_archive_after"` // e.g. "1h", "30m", "0" to disable
```

Add a method to parse it:

```go
// ArchiveThreshold returns the duration after which idle agents are archived.
// Returns 0 (disabled) if not set or invalid.
func (c Config) ArchiveThreshold() time.Duration {
	if c.AutoArchiveAfter == "" {
		return 0
	}
	d, err := time.ParseDuration(c.AutoArchiveAfter)
	if err != nil {
		return 0
	}
	return d
}
```

- [ ] **Step 6: Add config test**

Add to `internal/config/config_test.go`:

```go
func TestArchiveThreshold(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"1h", time.Hour},
		{"30m", 30 * time.Minute},
		{"", 0},
		{"invalid", 0},
		{"0", 0},
	}
	for _, tt := range tests {
		cfg := Config{AutoArchiveAfter: tt.input}
		if got := cfg.ArchiveThreshold(); got != tt.expected {
			t.Errorf("ArchiveThreshold(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
```

- [ ] **Step 7: Wire into TUI**

In `app.go`, in the `instancesMsg` handler (~line 328), after `FilterHidden`, add archive partitioning:

```go
	// Auto-archive idle agents
	threshold := a.cfg.ArchiveThreshold()
	if threshold > 0 && !a.showArchived {
		active, archived := controller.PartitionByArchive(a.instances, threshold)
		a.instances = active
		a.archivedCount = len(archived)
	}
```

Add fields to `App` struct:

```go
	showArchived   bool // toggle to show/hide archived agents
	archivedCount  int  // count of currently archived agents
```

Add `o` key handler in `handleKey`:

```go
	case "o":
		if a.currentView == viewAgents {
			a.showArchived = !a.showArchived
			if a.showArchived {
				a.statusHint = "Showing all agents (including archived)"
			} else {
				a.statusHint = fmt.Sprintf("Hiding %d archived agents", a.archivedCount)
			}
			return a, nil
		}
```

- [ ] **Step 8: Run full suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: all pass

- [ ] **Step 9: Commit**

```bash
git add internal/controller/archive.go internal/controller/archive_test.go internal/config/config.go internal/config/config_test.go internal/frontend/tui/app.go
git commit -m "feat: auto-archive idle agents with configurable threshold (o key toggle)"
```

---

## Task 4: Build Tag Test Separation [P]

Categorize existing tests. Tests that require external processes (tmux, kubectl, real processes) get build tags.

**Files:**
- Modify: `internal/terminal/tmux_test.go` (add `//go:build integration`)
- Modify: `internal/terminal/kubectl_test.go` (add `//go:build integration`)
- Modify: `internal/terminal/embed_test.go` (add `//go:build integration`)
- Modify: `internal/spawn/spawn_test.go` (add `//go:build integration`)
- Modify: `internal/clipboard/clipboard_test.go` (add `//go:build integration`)

- [ ] **Step 1: Identify tests that call external programs**

Run: `grep -rl "exec.Command\|os.Process\|tmux\|kubectl" --include="*_test.go" .`

For each file, check if the tests actually require a running process or just test pure logic.

- [ ] **Step 2: Add build tag to files that need external tools**

For each test file that requires tmux, kubectl, or a real PTY, prepend:

```go
//go:build integration

```

(with a blank line after the tag, before `package`)

Files that should get the `integration` tag:
- `internal/terminal/tmux_test.go` (calls tmux commands)
- `internal/terminal/kubectl_test.go` (calls kubectl)

Files that should stay untagged (pure logic tests even if they import exec):
- `internal/spawn/spawn_test.go` (if it tests command construction, not execution)
- `internal/discovery/process_test.go` (if mocked)

Review each file individually before tagging.

- [ ] **Step 3: Verify unit tests still pass without tags**

Run: `go test ./... -timeout 30s`
Expected: all pass (integration-tagged tests are excluded)

- [ ] **Step 4: Verify tagged tests are accessible**

Run: `go test -tags integration ./internal/terminal/ -timeout 30s -v -list '.*' 2>&1 | head -20`
Expected: lists the integration test names

- [ ] **Step 5: Update Makefile**

Add targets:

```makefile
test:
	go test ./... -timeout 30s

test-integration:
	go test -tags integration ./... -timeout 30s

test-all:
	go test -tags "integration e2e" ./... -timeout 60s
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "build: add integration build tag to tests requiring external tools"
```

---

## Task 5: Binary-as-Subprocess E2E Tests

Compile the real binary in TestMain, exercise CLI commands as subprocesses.

**Files:**
- Create: `internal/e2e/cli_test.go`

- [ ] **Step 1: Write E2E test file with TestMain**

```go
//go:build e2e

// internal/e2e/cli_test.go
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "aimux-e2e-*")
	if err != nil {
		panic("cannot create temp dir: " + err.Error())
	}
	defer os.RemoveAll(tmpDir)

	binaryPath = filepath.Join(tmpDir, "aimux")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "../../cmd/aimux")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		panic("cannot build aimux: " + err.Error())
	}

	os.Exit(m.Run())
}

func TestVersion(t *testing.T) {
	out, err := exec.Command(binaryPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("version failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "aimux") {
		t.Errorf("expected 'aimux' in version output, got: %s", out)
	}
}

func TestVersionJSON(t *testing.T) {
	out, err := exec.Command(binaryPath, "version", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("version --json failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("version --json output is not valid JSON: %v\n%s", err, out)
	}
}

func TestAgentsJSON(t *testing.T) {
	out, err := exec.Command(binaryPath, "agents", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("agents --json failed: %v\n%s", err, out)
	}
	// Should return valid JSON (possibly empty array)
	var result interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("agents --json is not valid JSON: %v\n%s", err, out)
	}
}

func TestSessionsList(t *testing.T) {
	out, err := exec.Command(binaryPath, "sessions", "--list", "--json", "--limit", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("sessions --list --json failed: %v\n%s", err, out)
	}
	var result interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("sessions --list --json is not valid JSON: %v\n%s", err, out)
	}
}

func TestSpawnDryRun(t *testing.T) {
	out, err := exec.Command(binaryPath, "spawn", "claude", "--dry-run", "--json", "--dir", "/tmp").CombinedOutput()
	if err != nil {
		t.Fatalf("spawn --dry-run failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("spawn --dry-run --json is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := result["command"]; !ok {
		t.Error("expected 'command' field in dry-run output")
	}
}

func TestAgentContext(t *testing.T) {
	out, err := exec.Command(binaryPath, "agent-context", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("agent-context --json failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("agent-context --json is not valid JSON: %v\n%s", err, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	cmd := exec.Command(binaryPath, "nonexistent-cmd")
	err := cmd.Run()
	if err == nil {
		t.Error("expected non-zero exit code for unknown command")
	}
}
```

- [ ] **Step 2: Run E2E tests**

Run: `go test -tags e2e ./internal/e2e/ -timeout 60s -v`
Expected: all tests PASS (binary builds, each command returns valid JSON)

- [ ] **Step 3: Commit**

```bash
git add internal/e2e/cli_test.go
git commit -m "test: add binary-as-subprocess E2E tests for CLI commands"
```

---

## Task 6: Configurable Badges

Badge evaluation in `internal/badge/` (core package). Config adds `badges` section. TUI renders badges in agent rows.

**Files:**
- Create: `internal/badge/badge.go`
- Create: `internal/badge/badge_test.go`
- Modify: `internal/config/config.go` (add `Badges` field)
- Modify: `internal/agent/agent.go` (add `Badges` field)
- Modify: `internal/frontend/tui/views/agents.go` (render badges in name column)

- [ ] **Step 1: Write the failing test for badge evaluation**

```go
// internal/badge/badge_test.go
package badge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluate_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	content := `{"name": "my-app", "version": "1.0.0"}`
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0644)

	rules := []Rule{
		{Path: "package.json", JSONPath: "name", Label: "pkg"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != "my-app" {
		t.Errorf("expected 'my-app', got %q", badges[0].Value)
	}
	if badges[0].Label != "pkg" {
		t.Errorf("expected label 'pkg', got %q", badges[0].Label)
	}
}

func TestEvaluate_FileExistence(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".python-version"), []byte("3.11\n"), 0644)

	rules := []Rule{
		{Path: ".python-version", Label: "py"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != "3.11" {
		t.Errorf("expected '3.11', got %q", badges[0].Value)
	}
}

func TestEvaluate_MissingFile(t *testing.T) {
	dir := t.TempDir()
	rules := []Rule{
		{Path: "nonexistent.json", JSONPath: "name", Label: "x"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 0 {
		t.Errorf("expected 0 badges for missing file, got %d", len(badges))
	}
}

func TestEvaluate_NestedJSONPath(t *testing.T) {
	dir := t.TempDir()
	content := `{"engines": {"node": ">=18"}}`
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0644)

	rules := []Rule{
		{Path: "package.json", JSONPath: "engines.node", Label: "node"},
	}
	badges := Evaluate(dir, rules)
	if len(badges) != 1 {
		t.Fatalf("expected 1 badge, got %d", len(badges))
	}
	if badges[0].Value != ">=18" {
		t.Errorf("expected '>=18', got %q", badges[0].Value)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/badge/ -timeout 30s -run TestEvaluate -v`
Expected: FAIL (package doesn't exist)

- [ ] **Step 3: Write the implementation**

```go
// internal/badge/badge.go
package badge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Rule defines how to extract a badge value from a project file.
type Rule struct {
	Path     string `yaml:"path"`      // file path relative to working dir
	JSONPath string `yaml:"json_path"` // dot-separated path into JSON (optional)
	Label    string `yaml:"label"`     // display label
	Color    string `yaml:"color"`     // hex color (optional)
}

// Badge is an evaluated badge ready for display.
type Badge struct {
	Label string
	Value string
	Color string
}

// Evaluate runs all badge rules against a working directory.
// Rules that fail (missing file, bad JSON path) are silently skipped.
func Evaluate(workDir string, rules []Rule) []Badge {
	var badges []Badge
	for _, r := range rules {
		b, ok := evaluate(workDir, r)
		if ok {
			badges = append(badges, b)
		}
	}
	return badges
}

func evaluate(workDir string, r Rule) (Badge, bool) {
	path := filepath.Join(workDir, r.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Badge{}, false
	}

	value := strings.TrimSpace(string(data))

	if r.JSONPath != "" {
		v, ok := extractJSONPath(data, r.JSONPath)
		if !ok {
			return Badge{}, false
		}
		value = v
	} else {
		// For plain files, take first line only
		if idx := strings.IndexByte(value, '\n'); idx != -1 {
			value = value[:idx]
		}
	}

	return Badge{
		Label: r.Label,
		Value: value,
		Color: r.Color,
	}, true
}

func extractJSONPath(data []byte, path string) (string, bool) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", false
	}

	parts := strings.Split(path, ".")
	current := obj
	for _, key := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return "", false
		}
		current, ok = m[key]
		if !ok {
			return "", false
		}
	}
	return fmt.Sprintf("%v", current), true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/badge/ -timeout 30s -v`
Expected: all 4 tests PASS

- [ ] **Step 5: Add config section**

In `internal/config/config.go`, add to `Config`:

```go
	Badges []BadgeRule `yaml:"badges"` // sidebar badge rules
```

Add the struct:

```go
// BadgeRule maps a project file path to a badge display.
type BadgeRule struct {
	Path     string `yaml:"path"`
	JSONPath string `yaml:"json_path"`
	Label    string `yaml:"label"`
	Color    string `yaml:"color"`
}
```

Add merge logic in `Load()`:

```go
	if len(fileCfg.Badges) > 0 {
		cfg.Badges = fileCfg.Badges
	}
```

- [ ] **Step 6: Add Badges field to Agent**

In `internal/agent/agent.go`, add:

```go
	Badges []BadgeValue // evaluated badge results from project files
```

And the type:

```go
// BadgeValue is an evaluated badge for display.
type BadgeValue struct {
	Label string
	Value string
	Color string
}
```

- [ ] **Step 7: Wire badge evaluation in discovery**

In `app.go`, in the `instancesMsg` handler, after setting agents but before `SetAgents`:

```go
	// Evaluate badges for each agent
	if len(a.cfg.Badges) > 0 {
		rules := make([]badge.Rule, len(a.cfg.Badges))
		for i, b := range a.cfg.Badges {
			rules[i] = badge.Rule{Path: b.Path, JSONPath: b.JSONPath, Label: b.Label, Color: b.Color}
		}
		for i := range a.instances {
			if a.instances[i].WorkingDir != "" {
				badges := badge.Evaluate(a.instances[i].WorkingDir, rules)
				for _, b := range badges {
					a.instances[i].Badges = append(a.instances[i].Badges, agent.BadgeValue{
						Label: b.Label, Value: b.Value, Color: b.Color,
					})
				}
			}
		}
	}
```

- [ ] **Step 8: Render badges in agents view**

In `views/agents.go`, in `renderRow`, append badges after the name column:

```go
	// Render badges after name
	var badgeStr string
	for _, b := range a.Badges {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
		if b.Color != "" {
			style = style.Foreground(lipgloss.Color(b.Color))
		}
		badgeStr += style.Render(" [" + b.Value + "]")
	}
```

And include `badgeStr` in the row after the name.

- [ ] **Step 9: Run full suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: all pass

- [ ] **Step 10: Update docs**

Add badges section to `docs-site/src/content/docs/configuration.mdx`:

```yaml
badges:
  - path: "package.json"
    json_path: "name"
    label: "pkg"
  - path: ".python-version"
    label: "py"
    color: "#3776AB"
```

- [ ] **Step 11: Commit**

```bash
git add internal/badge/ internal/config/config.go internal/config/config_test.go internal/agent/agent.go internal/frontend/tui/views/agents.go docs-site/
git commit -m "feat: configurable project badges in agent table"
```

---

## Task 7: Project-Local Config (.aimux/) [P]

Adds `.aimux/config.yaml` support in the project working directory, merged with global config.

**Files:**
- Create: `internal/config/project.go`
- Create: `internal/config/project_test.go`
- Modify: `internal/config/config.go` (add `MergeProject` method)

- [ ] **Step 1: Write the failing test**

```go
// internal/config/project_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProject_MergesOverGlobal(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte(`
shell: /bin/fish
auto_archive_after: "30m"
badges:
  - path: "go.mod"
    label: "go"
`), 0644)

	global := Default()
	merged, err := LoadProject(dir, global)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if merged.Shell != "/bin/fish" {
		t.Errorf("expected /bin/fish, got %s", merged.Shell)
	}
	if merged.AutoArchiveAfter != "30m" {
		t.Errorf("expected 30m, got %s", merged.AutoArchiveAfter)
	}
	if len(merged.Badges) != 1 {
		t.Errorf("expected 1 badge, got %d", len(merged.Badges))
	}
}

func TestLoadProject_NoProjectDir(t *testing.T) {
	global := Default()
	merged, err := LoadProject("/nonexistent", global)
	if err != nil {
		t.Fatalf("LoadProject should not error on missing dir: %v", err)
	}
	if merged.Shell != global.Shell {
		t.Error("missing project config should return global unchanged")
	}
}

func TestLoadProject_PreservesGlobalProviders(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".aimux")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "config.yaml"), []byte(`
shell: /bin/bash
`), 0644)

	global := Default()
	merged, err := LoadProject(dir, global)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.IsProviderEnabled("claude") {
		t.Error("project config should not wipe global providers")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -timeout 30s -run TestLoadProject -v`
Expected: FAIL with "undefined: LoadProject"

- [ ] **Step 3: Write the implementation**

```go
// internal/config/project.go
package config

import "path/filepath"

// ProjectConfigDir is the directory name for project-local config.
const ProjectConfigDir = ".aimux"

// LoadProject reads project-local config from dir/.aimux/config.yaml and
// merges it over the global config. Project values override global values.
// If no project config exists, the global config is returned unchanged.
func LoadProject(projectDir string, global Config) (Config, error) {
	path := filepath.Join(projectDir, ProjectConfigDir, "config.yaml")
	return Load(path) // Load already returns Default() for missing files
}
```

Wait, that won't work because `Load` starts from `Default()`, not from `global`. We need a merge function. Let me adjust:

```go
// internal/config/project.go
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectConfigDir is the directory name for project-local config.
const ProjectConfigDir = ".aimux"

// LoadProject reads project-local config from dir/.aimux/config.yaml and
// merges it over the global config. Project values override global values.
// If no project config exists, the global config is returned unchanged.
func LoadProject(projectDir string, global Config) (Config, error) {
	path := filepath.Join(projectDir, ProjectConfigDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return global, nil
		}
		return global, err
	}

	var proj Config
	if err := yaml.Unmarshal(data, &proj); err != nil {
		return global, err
	}

	return mergeOver(global, proj), nil
}

// mergeOver applies non-zero values from overlay onto base.
func mergeOver(base, overlay Config) Config {
	if overlay.Shell != "" {
		base.Shell = overlay.Shell
	}
	if overlay.RefreshInterval != "" {
		base.RefreshInterval = overlay.RefreshInterval
	}
	if overlay.DefaultRuntime != "" {
		base.DefaultRuntime = overlay.DefaultRuntime
	}
	if overlay.AutoArchiveAfter != "" {
		base.AutoArchiveAfter = overlay.AutoArchiveAfter
	}
	if len(overlay.Badges) > 0 {
		base.Badges = overlay.Badges
	}
	if len(overlay.QuickLaunch.Directories) > 0 {
		base.QuickLaunch = overlay.QuickLaunch
	}
	if overlay.Providers != nil {
		for name, pc := range overlay.Providers {
			base.Providers[name] = pc
		}
	}
	return base
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -timeout 30s -run TestLoadProject -v`
Expected: all 3 tests PASS

- [ ] **Step 5: Run full suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add internal/config/project.go internal/config/project_test.go
git commit -m "feat: support project-local config via .aimux/config.yaml"
```

---

## Task 8: Runtime + Sandbox Abstraction (Three-Layer Model)

Sessions are containers. They can run locally (Podman) or on Kubernetes.
Sandboxing is a separate policy layer that wraps the runtime. OpenShell is
one sandbox implementation. The design follows the optional-capability
interface pattern already used by aimux (Messenger, TaskLister, Spawner).

```
Provider (Claude/Codex/Gemini)     -- WHAT agent to run
    +-- Runtime (local/container/k8s) -- WHERE to run it
         +-- Sandbox (none/openshell) -- HOW to isolate it (TODO)
```

### Task 8A: Runtime Interface + LocalRuntime [implement now]

**Files:**
- Create: `internal/runtime/runtime.go` (interface + types)
- Create: `internal/runtime/runtime_test.go`
- Create: `internal/runtime/local.go` (wraps current spawn.Launch behavior)
- Create: `internal/runtime/local_test.go`

### Task 8B: ContainerRuntime [implement now]

**Files:**
- Create: `internal/runtime/container.go` (Podman/Docker lifecycle)
- Create: `internal/runtime/container_test.go`
- Modify: `internal/config/config.go` (add `Runtimes` section)

### Task 8C: Sandbox Policy Interface + OpenShell Stub [design now, implement later]

**Files:**
- Create: `internal/runtime/policy.go` (PolicyEnforcer interface + types)
- Create: `internal/runtime/policy_test.go`
- Create: `internal/runtime/openshell.go` (stub: interface + TODO markers)
- Create: `internal/runtime/openshell_test.go` (compile-time interface check only)

#### Task 8A: Runtime Interface + LocalRuntime

- [ ] **Step 1: Write failing test for Runtime interface**

```go
// internal/runtime/runtime_test.go
package runtime

import (
	"context"
	"testing"
)

var _ Runtime = (*mockRuntime)(nil)

type mockRuntime struct{ state State }

func (m *mockRuntime) Type() string                                    { return "mock" }
func (m *mockRuntime) Name() string                                    { return "test" }
func (m *mockRuntime) Create(_ context.Context, _ CreateOpts) error    { m.state = StateRunning; return nil }
func (m *mockRuntime) Start(_ context.Context) error                   { m.state = StateRunning; return nil }
func (m *mockRuntime) Stop(_ context.Context) error                    { m.state = StateStopped; return nil }
func (m *mockRuntime) Delete(_ context.Context) error                  { return nil }
func (m *mockRuntime) Status(_ context.Context) (*RuntimeStatus, error) {
	return &RuntimeStatus{State: m.state}, nil
}
func (m *mockRuntime) ExecPrefix() []string { return nil }
func (m *mockRuntime) Attach(_ context.Context) error { return nil }

func TestState_String(t *testing.T) {
	tests := []struct {
		s    State
		want string
	}{
		{StateStopped, "stopped"},
		{StateRunning, "running"},
		{StateCreating, "creating"},
		{StateError, "error"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -timeout 30s -v`
Expected: FAIL (package doesn't exist)

- [ ] **Step 3: Write the Runtime interface**

```go
// internal/runtime/runtime.go
package runtime

import "context"

// State represents the lifecycle state of a runtime environment.
type State int

const (
	StateStopped  State = iota
	StateCreating
	StateRunning
	StateError
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateCreating:
		return "creating"
	case StateRunning:
		return "running"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// RuntimeStatus is the current state of a runtime environment.
type RuntimeStatus struct {
	State   State
	Message string
}

// CreateOpts configures a new runtime environment.
type CreateOpts struct {
	Name      string
	Image     string            // container image (ignored for local)
	WorkDir   string            // project directory to mount
	Env       map[string]string // environment variables
	Resources *Resources        // CPU/memory limits (container/k8s only)
	Sandbox   *SandboxConfig    // optional policy to apply after creation (see policy.go)
}

// Resources specifies compute limits for container/k8s runtimes.
type Resources struct {
	CPULimit    string // e.g. "2"
	MemoryLimit string // e.g. "4Gi"
}

// Runtime manages the execution environment for an agent session.
// Local: process on host. Container: Podman/Docker. K8s: pod in cluster.
type Runtime interface {
	Type() string // "local", "container", "k8s"
	Name() string

	// Lifecycle
	Create(ctx context.Context, opts CreateOpts) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Delete(ctx context.Context) error
	Status(ctx context.Context) (*RuntimeStatus, error)

	// Session interaction
	ExecPrefix() []string // command prefix to run inside, e.g. ["podman","exec","-it","name"]
	Attach(ctx context.Context) error
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/ -timeout 30s -v`
Expected: PASS

- [ ] **Step 5: Write LocalRuntime test**

```go
// internal/runtime/local_test.go
package runtime

import "testing"

var _ Runtime = (*Local)(nil)

func TestLocal_Type(t *testing.T) {
	l := NewLocal("test-session")
	if l.Type() != "local" {
		t.Errorf("expected 'local', got %q", l.Type())
	}
}

func TestLocal_ExecPrefix(t *testing.T) {
	l := NewLocal("test")
	if prefix := l.ExecPrefix(); prefix != nil {
		t.Errorf("local runtime should have nil exec prefix, got %v", prefix)
	}
}
```

- [ ] **Step 6: Write LocalRuntime implementation**

```go
// internal/runtime/local.go
package runtime

import "context"

// Local is the default runtime: agent runs as a process on the host.
// Create/Start/Stop/Delete are no-ops since the process lifecycle is
// managed by spawn.Launch and the provider's Kill method.
type Local struct {
	name string
}

func NewLocal(name string) *Local { return &Local{name: name} }

func (l *Local) Type() string { return "local" }
func (l *Local) Name() string { return l.name }

func (l *Local) Create(_ context.Context, _ CreateOpts) error { return nil }
func (l *Local) Start(_ context.Context) error                { return nil }
func (l *Local) Stop(_ context.Context) error                 { return nil }
func (l *Local) Delete(_ context.Context) error               { return nil }

func (l *Local) Status(_ context.Context) (*RuntimeStatus, error) {
	return &RuntimeStatus{State: StateRunning}, nil
}

func (l *Local) ExecPrefix() []string { return nil }
func (l *Local) Attach(_ context.Context) error { return nil }
```

- [ ] **Step 7: Run tests, commit**

Run: `go test ./internal/runtime/ -timeout 30s -v`

```bash
git add internal/runtime/
git commit -m "feat: add Runtime interface with Local implementation"
```

#### Task 8B: ContainerRuntime

- [ ] **Step 1: Write Container test**

```go
// internal/runtime/container_test.go
package runtime

import "testing"

var _ Runtime = (*Container)(nil)

func TestContainer_Type(t *testing.T) {
	c := NewContainer("test", "podman")
	if c.Type() != "container" {
		t.Errorf("expected 'container', got %q", c.Type())
	}
}

func TestContainer_ExecPrefix(t *testing.T) {
	c := NewContainer("my-ctr", "podman")
	prefix := c.ExecPrefix()
	if len(prefix) != 4 {
		t.Fatalf("expected 4 elements, got %d: %v", len(prefix), prefix)
	}
	if prefix[0] != "podman" || prefix[3] != "my-ctr" {
		t.Errorf("unexpected prefix: %v", prefix)
	}
}

func TestContainer_DefaultEngine(t *testing.T) {
	c := NewContainer("test", "")
	if c.engine != "podman" {
		t.Errorf("expected default engine 'podman', got %q", c.engine)
	}
}
```

- [ ] **Step 2: Write Container implementation**

```go
// internal/runtime/container.go
package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Container manages an agent session inside a Podman or Docker container.
type Container struct {
	name   string
	engine string // "podman" or "docker"
}

func NewContainer(name, engine string) *Container {
	if engine == "" {
		engine = "podman"
	}
	return &Container{name: name, engine: engine}
}

func (c *Container) Type() string { return "container" }
func (c *Container) Name() string { return c.name }

func (c *Container) Create(ctx context.Context, opts CreateOpts) error {
	args := []string{"run", "-d", "--name", c.name}
	if opts.WorkDir != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/workspace:Z", opts.WorkDir), "-w", "/workspace")
	}
	for k, v := range opts.Env {
		args = append(args, "-e", k+"="+v)
	}
	if opts.Resources != nil {
		if opts.Resources.CPULimit != "" {
			args = append(args, "--cpus", opts.Resources.CPULimit)
		}
		if opts.Resources.MemoryLimit != "" {
			args = append(args, "--memory", opts.Resources.MemoryLimit)
		}
	}
	image := opts.Image
	if image == "" {
		image = "fedora:41"
	}
	args = append(args, image, "sleep", "infinity")
	cmd := exec.CommandContext(ctx, c.engine, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s run: %w\n%s", c.engine, err, out)
	}
	return nil
}

func (c *Container) Start(ctx context.Context) error {
	return c.run(ctx, "start", c.name)
}

func (c *Container) Stop(ctx context.Context) error {
	return c.run(ctx, "stop", c.name)
}

func (c *Container) Delete(ctx context.Context) error {
	return c.run(ctx, "rm", "-f", c.name)
}

func (c *Container) Status(ctx context.Context) (*RuntimeStatus, error) {
	cmd := exec.CommandContext(ctx, c.engine, "inspect", "--format", "{{.State.Status}}", c.name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &RuntimeStatus{State: StateStopped}, nil
	}
	if strings.TrimSpace(string(out)) == "running" {
		return &RuntimeStatus{State: StateRunning}, nil
	}
	return &RuntimeStatus{State: StateStopped}, nil
}

func (c *Container) ExecPrefix() []string {
	return []string{c.engine, "exec", "-it", c.name}
}

func (c *Container) Attach(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.engine, "exec", "-it", c.name, "/bin/bash")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil // caller wires these
	return cmd.Run()
}

func (c *Container) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, c.engine, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", c.engine, args[0], err, out)
	}
	return nil
}
```

- [ ] **Step 3: Add Runtimes config**

In `internal/config/config.go`, add to Config:

```go
	Runtimes map[string]RuntimeConfig `yaml:"runtimes"` // named runtime profiles
```

```go
// RuntimeConfig defines a runtime profile for spawning agent sessions.
type RuntimeConfig struct {
	Type    string `yaml:"type"`    // "local", "container", "k8s"
	Engine  string `yaml:"engine"`  // "podman" or "docker" (container only)
	Image   string `yaml:"image"`   // container image (container/k8s only)
	Sandbox string `yaml:"sandbox"` // sandbox profile name (future: references SandboxConfig)
}
```

- [ ] **Step 4: Run tests, commit**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`

```bash
git add internal/runtime/ internal/config/
git commit -m "feat: add ContainerRuntime with Podman/Docker support"
```

#### Task 8C: Sandbox Policy Interface + OpenShell Stub [design only]

- [ ] **Step 1: Write PolicyEnforcer interface test**

```go
// internal/runtime/policy_test.go
package runtime

import (
	"context"
	"testing"
)

var _ PolicyEnforcer = (*mockEnforcer)(nil)

type mockEnforcer struct {
	applied bool
	policy  *SandboxConfig
}

func (m *mockEnforcer) ApplyPolicy(_ context.Context, p SandboxConfig) error {
	m.applied = true
	m.policy = &p
	return nil
}
func (m *mockEnforcer) UpdatePolicy(_ context.Context, p SandboxConfig) error {
	m.policy = &p
	return nil
}
func (m *mockEnforcer) CurrentPolicy() *SandboxConfig { return m.policy }

func TestNetworkRule_String(t *testing.T) {
	r := NetworkRule{
		Name:     "github-readonly",
		Hosts:    []string{"api.github.com"},
		Ports:    []int{443},
		Binaries: []string{"/usr/bin/git"},
	}
	if r.Name != "github-readonly" {
		t.Error("unexpected name")
	}
}

func TestSandboxConfig_HasPolicy(t *testing.T) {
	empty := SandboxConfig{}
	if empty.HasNetworkPolicy() {
		t.Error("empty config should not have network policy")
	}
	withPolicy := SandboxConfig{
		Network: &NetworkPolicy{DenyAll: true},
	}
	if !withPolicy.HasNetworkPolicy() {
		t.Error("config with network rules should have policy")
	}
}
```

- [ ] **Step 2: Write policy types**

```go
// internal/runtime/policy.go
package runtime

import "context"

// PolicyEnforcer is an optional capability for runtimes that support
// security policy enforcement. Check via type assertion:
//   if pe, ok := rt.(PolicyEnforcer); ok { pe.ApplyPolicy(...) }
//
// Runtimes that implement this: OpenShellRuntime (full per-binary policy),
// and potentially K8sRuntime (via NetworkPolicy CRDs).
// LocalRuntime and plain ContainerRuntime do NOT implement this.
type PolicyEnforcer interface {
	ApplyPolicy(ctx context.Context, policy SandboxConfig) error
	UpdatePolicy(ctx context.Context, policy SandboxConfig) error
	CurrentPolicy() *SandboxConfig
}

// SandboxConfig holds all policy layers for a sandboxed runtime.
// Maps to OpenShell's SandboxPolicy proto.
type SandboxConfig struct {
	Type       string          // "openshell", "none"
	Network    *NetworkPolicy  // per-binary outbound network rules
	Filesystem *FSPolicy       // Landlock read-only/read-write paths
	// TODO: ProcessPolicy (run_as_user, seccomp profile)
	// TODO: InferencePolicy (route inference.local to managed backends)
	// TODO: CredentialPolicy (provider-based credential injection)
}

// HasNetworkPolicy returns true if any network restrictions are configured.
func (s SandboxConfig) HasNetworkPolicy() bool {
	return s.Network != nil
}

// NetworkPolicy defines outbound network restrictions.
// OpenShell enforces these per-binary via OPA + proxy interception.
// Plain containers can only enforce at the pod/container level.
type NetworkPolicy struct {
	DenyAll bool            // deny all outbound except rules below
	Rules   []NetworkRule   // per-endpoint allow rules
	Groups  []string        // named domain groups: "python", "nodejs", "github"
}

// NetworkRule allows specific binaries to reach specific endpoints.
// This is the key differentiator of OpenShell over plain containers.
type NetworkRule struct {
	Name     string   // e.g. "anthropic-api"
	Hosts    []string // e.g. ["api.anthropic.com"]
	Ports    []int    // e.g. [443]
	Binaries []string // e.g. ["/usr/local/bin/claude", "/usr/bin/node"]
	Access   string   // "read-only", "read-write", "full"
}

// FSPolicy defines filesystem isolation via Landlock.
type FSPolicy struct {
	ReadOnly  []string // e.g. ["/usr", "/lib", "/etc"]
	ReadWrite []string // e.g. ["/workspace", "/tmp"]
}
```

- [ ] **Step 3: Write OpenShell stub**

```go
// internal/runtime/openshell.go
package runtime

import (
	"context"
	"fmt"
)

// OpenShellRuntime is a Runtime + PolicyEnforcer backed by NVIDIA OpenShell.
// It owns the full stack: the container, the policy engine, and credential
// injection. When you Create an OpenShellRuntime, it calls `openshell sandbox
// create` which provisions the container AND applies the policy internally.
//
// TODO: Implement via openshell CLI wrapper (see zanetworker/openshell).
// The CLI is preferred over gRPC because OpenShell is alpha (v0.0.36) and
// the proto API changes frequently. The CLI handles mTLS/cert management.
type OpenShellRuntime struct {
	name   string
	binary string // path to openshell CLI
	policy *SandboxConfig
}

// Compile-time interface checks
var (
	_ Runtime        = (*OpenShellRuntime)(nil)
	_ PolicyEnforcer = (*OpenShellRuntime)(nil)
)

func NewOpenShellRuntime(name string) *OpenShellRuntime {
	return &OpenShellRuntime{name: name, binary: "openshell"}
}

func (o *OpenShellRuntime) Type() string { return "openshell" }
func (o *OpenShellRuntime) Name() string { return o.name }

func (o *OpenShellRuntime) Create(_ context.Context, _ CreateOpts) error {
	return fmt.Errorf("openshell runtime not yet implemented")
}
func (o *OpenShellRuntime) Start(_ context.Context) error {
	return fmt.Errorf("openshell runtime not yet implemented")
}
func (o *OpenShellRuntime) Stop(_ context.Context) error {
	return fmt.Errorf("openshell runtime not yet implemented")
}
func (o *OpenShellRuntime) Delete(_ context.Context) error {
	return fmt.Errorf("openshell runtime not yet implemented")
}
func (o *OpenShellRuntime) Status(_ context.Context) (*RuntimeStatus, error) {
	return &RuntimeStatus{State: StateError, Message: "not implemented"}, nil
}
func (o *OpenShellRuntime) ExecPrefix() []string {
	return []string{o.binary, "sandbox", "exec", "-n", o.name, "--tty", "--"}
}
func (o *OpenShellRuntime) Attach(_ context.Context) error {
	return fmt.Errorf("openshell runtime not yet implemented")
}

// PolicyEnforcer implementation
func (o *OpenShellRuntime) ApplyPolicy(_ context.Context, p SandboxConfig) error {
	o.policy = &p
	return fmt.Errorf("openshell policy enforcement not yet implemented")
}
func (o *OpenShellRuntime) UpdatePolicy(_ context.Context, p SandboxConfig) error {
	o.policy = &p
	return fmt.Errorf("openshell policy hot-reload not yet implemented")
}
func (o *OpenShellRuntime) CurrentPolicy() *SandboxConfig { return o.policy }
```

- [ ] **Step 4: Write OpenShell stub test**

```go
// internal/runtime/openshell_test.go
package runtime

import "testing"

func TestOpenShellRuntime_ImplementsBothInterfaces(t *testing.T) {
	o := NewOpenShellRuntime("test")
	var _ Runtime = o
	var _ PolicyEnforcer = o
}

func TestOpenShellRuntime_Type(t *testing.T) {
	o := NewOpenShellRuntime("test")
	if o.Type() != "openshell" {
		t.Errorf("expected 'openshell', got %q", o.Type())
	}
}

func TestOpenShellRuntime_ExecPrefix(t *testing.T) {
	o := NewOpenShellRuntime("my-sandbox")
	prefix := o.ExecPrefix()
	if prefix[0] != "openshell" {
		t.Errorf("expected 'openshell', got %q", prefix[0])
	}
}
```

- [ ] **Step 5: Run all tests, commit**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`

```bash
git add internal/runtime/
git commit -m "feat: add PolicyEnforcer interface and OpenShell runtime stub"
```

---

## Parallelism Map

Tasks marked `[P]` are fully independent and can be dispatched to separate subagents simultaneously:

| Group | Tasks | Why parallel |
|-------|-------|-------------|
| 0 | 0.1-0.5 (Controller Refactor) | All [P] -- different controller files, no shared state |
| 0+ | 0B (Benchmarks), 0C (Parity Rule) | Independent of each other and of 0.1-0.5 |
| A | 1 (Attend), 2 (Fade), 3 (Archive) | **DONE** |
| B | 4 (Build Tags), 5 (E2E) | Sequential (5 depends on 4's Makefile) |
| C | 6 (Badges), 7 (Project Config) | Both touch config.go, run sequentially |
| D | 8A (Runtime), 8B (Container), 8C (Policy) | Sequential within group, independent of B/C |

**Recommended execution order:**
1. **Task 0** (controller refactor, 5 subtasks in parallel) + **0B** (benchmarks) + **0C** (parity rule) -- all independent
2. **Task 4 + 5** (test infrastructure)
3. **Task 6 + 7** (config extensions)
4. **Task 8A + 8B + 8C** (runtime/sandbox)

---

## Update docs-site

After all tasks, rebuild docs:

```bash
cd docs-site && npm run build
```

Verify no broken links or missing pages.
