# Unified Launcher and Tasks Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `internal/task/` core package, `TaskLister`/`Spawner` provider interfaces, a `:new` picker, a Where toggle in the session launcher, a task launcher, and a Tasks view — all with strict core/TUI separation so a future web frontend requires zero TUI imports.

**Architecture:** Core packages (`internal/task/`, `internal/provider/`) hold all data types and business logic. TUI views are thin renderers. The invariant: `go test ./internal/task/... ./internal/provider/...` must pass with no bubbletea or lipgloss in those packages.

**Tech Stack:** Go, Bubble Tea (TUI only), go-redis/v9 (core, already in go.mod), k8s client-go (core, already in go.mod).

---

### Task 1: `internal/task/` core package

**Files:**
- Create: `internal/task/task.go`
- Create: `internal/task/loader.go`
- Create: `internal/task/task_test.go`

**Step 1: Write failing tests**

```go
// internal/task/task_test.go
package task_test

import (
    "testing"
    "time"
    "github.com/zanetworker/aimux/internal/task"
)

func TestTask_StatusIcon(t *testing.T) {
    cases := []struct{ status, want string }{
        {"completed", "✓"},
        {"claimed", "●"},
        {"in_progress", "●"},
        {"pending", "○"},
        {"failed", "✗"},
        {"dead", "✗"},
        {"", "?"},
    }
    for _, c := range cases {
        got := task.Task{Status: c.status}.StatusIcon()
        if got != c.want {
            t.Errorf("status=%q: got %q want %q", c.status, got, c.want)
        }
    }
}

func TestTask_IsTerminal(t *testing.T) {
    if !task.Task{Status: "completed"}.IsTerminal() {
        t.Error("completed should be terminal")
    }
    if task.Task{Status: "pending"}.IsTerminal() {
        t.Error("pending should not be terminal")
    }
}

func TestTask_FormatAge(t *testing.T) {
    tk := task.Task{CreatedAt: time.Now().Add(-5 * time.Minute)}
    age := tk.FormatAge()
    if age == "" || age == "-" {
        t.Errorf("expected formatted age, got %q", age)
    }
}
```

Run: `go test ./internal/task/... -v`
Expected: **FAIL** — package does not exist yet.

**Step 2: Implement `internal/task/task.go`**

```go
package task

import (
    "fmt"
    "time"
)

// Task is a normalized task entry from any source (Redis, local files).
// It is a core type — no bubbletea or lipgloss imports allowed here.
type Task struct {
    ID          string
    Prompt      string
    Status      string    // pending, claimed, in_progress, completed, failed, dead
    Assignee    string
    CreatedAt   time.Time
    CompletedAt time.Time
    Summary     string    // truncated result summary
    FullResult  string    // full result text (may be empty, fetched separately)
    Error       string
    Loc         string    // "local" or "k8s"
    Cost        float64
    DependsOn   []string  // task IDs this task waits on
}

// StatusIcon returns a single-character status indicator.
func (t Task) StatusIcon() string {
    switch t.Status {
    case "completed":              return "✓"
    case "claimed", "in_progress": return "●"
    case "pending":                return "○"
    case "failed", "dead":         return "✗"
    default:                       return "?"
    }
}

// IsTerminal returns true if the task will not change status again.
func (t Task) IsTerminal() bool {
    return t.Status == "completed" || t.Status == "failed" || t.Status == "dead"
}

// FormatAge returns a compact human-readable age string.
func (t Task) FormatAge() string {
    ref := t.CreatedAt
    if ref.IsZero() {
        return "-"
    }
    return formatDuration(time.Since(ref))
}

func formatDuration(d time.Duration) string {
    if d < time.Minute {
        return fmt.Sprintf("%ds", int(d.Seconds()))
    }
    if d < time.Hour {
        return fmt.Sprintf("%dm", int(d.Minutes()))
    }
    if d < 24*time.Hour {
        return fmt.Sprintf("%dh", int(d.Hours()))
    }
    return fmt.Sprintf("%dd", int(d.Hours()/24))
}
```

**Step 3: Implement `internal/task/loader.go`**

```go
package task

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "strconv"
    "strings"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/zanetworker/aimux/pkg/rediskeys"
)

// Spawner is implemented by providers that can fire a task at a remote agent.
// Keeping this in the core package means TUI and future frontends share it.
type Spawner interface {
    SpawnTask(provider, prompt string) error
}

// LoadFromRedis reads all tasks for teamID from Redis and returns them
// as []Task with Loc="k8s". Returns (nil, nil) if redisURL is empty.
func LoadFromRedis(redisURL, teamID string) ([]Task, error) {
    if redisURL == "" {
        return nil, nil
    }
    opt, err := redis.ParseURL(redisURL)
    if err != nil {
        return nil, fmt.Errorf("task.LoadFromRedis: parse URL: %w", err)
    }
    rdb := redis.NewClient(opt)
    defer rdb.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    taskIDs, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
        Key: rediskeys.TasksAll(teamID), Start: 0, Stop: -1,
    }).Result()
    if err != nil {
        return nil, fmt.Errorf("task.LoadFromRedis: read tasks:all: %w", err)
    }

    var tasks []Task
    for _, id := range taskIDs {
        fields, err := rdb.HGetAll(ctx, rediskeys.Task(teamID, id)).Result()
        if err != nil || len(fields) == 0 {
            continue
        }
        tasks = append(tasks, normalizeRedis(id, fields))
    }
    return tasks, nil
}

// LoadFromLocalFiles reads tasks from ~/.claude/tasks/{teamID}/task-*.json.
// Returns (nil, nil) if the directory does not exist.
func LoadFromLocalFiles(teamID string) ([]Task, error) {
    home, _ := os.UserHomeDir()
    dir := filepath.Join(home, ".claude", "tasks", teamID)
    entries, err := os.ReadDir(dir)
    if os.IsNotExist(err) {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("task.LoadFromLocalFiles: read dir: %w", err)
    }

    var tasks []Task
    for _, e := range entries {
        if !strings.HasPrefix(e.Name(), "task-") || !strings.HasSuffix(e.Name(), ".json") {
            continue
        }
        data, err := os.ReadFile(filepath.Join(dir, e.Name()))
        if err != nil {
            continue
        }
        var raw map[string]any
        if err := json.Unmarshal(data, &raw); err != nil {
            continue
        }
        tasks = append(tasks, normalizeLocal(raw))
    }
    return tasks, nil
}

// GetFullResult fetches the full result text for a completed task from Redis.
func GetFullResult(redisURL, teamID, taskID string) (string, error) {
    opt, err := redis.ParseURL(redisURL)
    if err != nil {
        return "", err
    }
    rdb := redis.NewClient(opt)
    defer rdb.Close()
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    val, err := rdb.Get(ctx, fmt.Sprintf("team:%s:task:%s:result_full", teamID, taskID)).Result()
    if err != nil {
        return "", err
    }
    return val, nil
}

func normalizeRedis(id string, f map[string]string) Task {
    t := Task{ID: id, Loc: "k8s"}
    t.Prompt = f["prompt"]
    t.Status = f["status"]
    t.Assignee = f["assignee"]
    t.Summary = f["result_summary"]
    t.Error = f["error"]
    if ts, err := strconv.ParseFloat(f["created_at"], 64); err == nil && ts > 0 {
        t.CreatedAt = time.Unix(int64(ts), 0)
    }
    if ts, err := strconv.ParseFloat(f["completed_at"], 64); err == nil && ts > 0 {
        t.CompletedAt = time.Unix(int64(ts), 0)
    }
    if deps := f["depends_on"]; deps != "" && deps != "[]" {
        var d []string
        _ = json.Unmarshal([]byte(deps), &d)
        t.DependsOn = d
    }
    return t
}

func normalizeLocal(raw map[string]any) Task {
    str := func(key string) string {
        if v, ok := raw[key].(string); ok { return v }
        return ""
    }
    t := Task{Loc: "local"}
    t.ID = str("id")
    t.Prompt = str("subject")
    t.Status = str("status")
    t.Assignee = str("owner")
    t.Summary = str("output")
    if t.ID == "" {
        t.ID = str("taskId")
    }
    return t
}
```

**Step 4: Run tests**

```bash
go test ./internal/task/... -v
```

Expected: **PASS** — 3 tests green.

**Step 5: Verify no TUI imports**

```bash
grep -r "bubbletea\|lipgloss\|charmbracelet" internal/task/
```

Expected: no output.

**Step 6: Commit**

```bash
git add internal/task/
git commit -m "feat: add internal/task core package (Task, LoadFromRedis, LoadFromLocalFiles, Spawner)"
```

---

### Task 2: `TaskLister` and `Spawner` on K8s provider

**Files:**
- Modify: `internal/provider/provider.go` — add `TaskLister` and `Spawner` interfaces
- Modify: `internal/provider/k8s.go` — implement both
- Modify: `internal/provider/k8s_test.go` — add compile-time checks

**Step 1: Add interfaces to provider.go**

After the existing `Messenger` interface, add:

```go
// TaskLister is an optional interface for providers that can enumerate tasks.
// Returns core task.Task — no TUI types.
type TaskLister interface {
    ListTasks() ([]task.Task, error)
}

// Spawner is an optional interface for providers that can fire a task
// at a remote agent. Returns immediately after queuing; the agent picks
// it up asynchronously.
type Spawner interface {
    SpawnTask(provider, prompt string) error
}
```

Add import: `"github.com/zanetworker/aimux/internal/task"`.

**Step 2: Implement `ListTasks()` on K8s provider**

In `k8s.go`, add:

```go
// ListTasks returns all tasks from Redis for the configured team.
// Implements provider.TaskLister.
func (k *K8s) ListTasks() ([]task.Task, error) {
    return task.LoadFromRedis(k.cfg.RedisURL, k.cfg.TeamID)
}
```

**Step 3: Implement `SpawnTask()` on K8s provider**

```go
// SpawnTask scales up one pod of the default role for the given provider
// and creates a task in Redis with the prompt. Implements provider.Spawner.
// The role is always "researcher" for V1 — Claude can spawn coders itself
// if the task requires code via the MCP spawn_agent tool.
func (k *K8s) SpawnTask(providerName, prompt string) error {
    if k.cfg.RedisURL == "" {
        return fmt.Errorf("SpawnTask: Redis not configured")
    }

    // Scale up deployment
    clientset, err := k.kubeClient()
    if err != nil {
        return fmt.Errorf("SpawnTask: build kube client: %w", err)
    }
    ns := k.cfg.Namespace
    if ns == "" {
        ns = "agents"
    }
    deployName := fmt.Sprintf("agent-%s-researcher", providerName)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    deploy, err := clientset.AppsV1().Deployments(ns).Get(ctx, deployName, metav1.GetOptions{})
    if err != nil {
        return fmt.Errorf("SpawnTask: get deployment %q: %w", deployName, err)
    }
    current := int32(0)
    if deploy.Spec.Replicas != nil {
        current = *deploy.Spec.Replicas
    }
    desired := current + 1
    deploy.Spec.Replicas = &desired
    if _, err := clientset.AppsV1().Deployments(ns).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
        return fmt.Errorf("SpawnTask: scale %q: %w", deployName, err)
    }

    // Create task in Redis
    opt, err := redis.ParseURL(k.cfg.RedisURL)
    if err != nil {
        return fmt.Errorf("SpawnTask: parse Redis URL: %w", err)
    }
    rdb := redis.NewClient(opt)
    defer rdb.Close()

    taskID := fmt.Sprintf("%x", time.Now().UnixNano())[:8]
    now := float64(time.Now().Unix())
    if err := rdb.HSet(ctx, rediskeys.Task(k.cfg.TeamID, taskID), map[string]any{
        "status":        "pending",
        "prompt":        prompt,
        "required_role": "researcher",
        "assignee":      "",
        "result_summary": "",
        "error":         "",
        "retry_count":   "0",
        "created_at":    fmt.Sprintf("%d", time.Now().Unix()),
        "depends_on":    "[]",
    }).Err(); err != nil {
        return fmt.Errorf("SpawnTask: write task hash: %w", err)
    }
    pipe := rdb.Pipeline()
    pipe.ZAdd(ctx, rediskeys.TasksPending(k.cfg.TeamID), redis.Z{Score: now, Member: taskID})
    pipe.ZAdd(ctx, rediskeys.TasksAll(k.cfg.TeamID), redis.Z{Score: now, Member: taskID})
    _, err = pipe.Exec(ctx)
    return err
}
```

**Step 4: Add compile-time interface checks to k8s_test.go**

```go
var _ provider.TaskLister = (*provider.K8s)(nil)
var _ provider.Spawner    = (*provider.K8s)(nil)
```

**Step 5: Build and test**

```bash
go build ./... 2>&1
go test ./internal/provider/... -timeout 30s -v 2>&1 | tail -20
```

Expected: builds clean, all provider tests pass.

**Step 6: Verify no TUI imports in provider package**

```bash
grep -r "bubbletea\|lipgloss\|charmbracelet" internal/provider/
```

Expected: no output.

**Step 7: Commit**

```bash
git add internal/provider/provider.go internal/provider/k8s.go internal/provider/k8s_test.go
git commit -m "feat: add TaskLister and Spawner interfaces, implement on K8s provider"
```

---

### Task 3: `:new` picker

**Files:**
- Modify: `internal/tui/views/launcher.go` — add `PickerView`
- Modify: `internal/tui/app.go` — wire picker

See original plan Task 1 — implementation unchanged. The picker is purely TUI and has no core dependencies to fix.

Commit message: `feat: add :new picker (Session / Task)`

---

### Task 4: Session launcher — Where toggle

**Files:**
- Modify: `internal/tui/views/launcher.go` — add `where` toggle
- Modify: `internal/tui/app.go` — handle `Remote` in `LaunchMsg`

See original plan Task 2. No core/TUI issues here — `LaunchMsg` carries only primitive strings.

Commit message: `feat: add Local/Remote Where toggle to session launcher`

---

### Task 5: Task launcher (TUI only)

**Files:**
- Create: `internal/tui/views/task_launcher.go`
- Modify: `internal/tui/app.go` — `openTaskLauncher()` + `handleRemoteTask()`

**Key change from original plan:** `TaskLauncherView` has no Redis/K8s logic. Provider availability comes from a core query passed in at construction time. `handleRemoteTask()` in `app.go` calls `provider.Spawner`, not Redis directly.

```go
// In app.go:
func (a App) handleRemoteTask(msg views.LaunchTaskMsg) (tea.Model, tea.Cmd) {
    for _, p := range a.providers {
        if p.Name() == "k8s" {
            if spawner, ok := p.(provider.Spawner); ok {
                if err := spawner.SpawnTask(msg.Provider, msg.Prompt); err != nil {
                    a.statusHint = "Spawn failed: " + err.Error()
                } else {
                    a.statusHint = fmt.Sprintf("Task queued (%s): %s", msg.Provider, truncateStr(msg.Prompt, 50))
                }
                a.currentView = viewAgents
                return a, nil
            }
        }
    }
    a.statusHint = "K8s provider not configured"
    return a, nil
}
```

`TaskLauncherView` itself only holds UI state (where, providerCursor, prompt). It emits `LaunchTaskMsg{Where, Provider, Prompt}` — no Redis, no K8s.

Commit message: `feat: add task launcher overlay (delegates to provider.Spawner)`

---

### Task 6: Tasks view (TUI only)

**Files:**
- Create: `internal/tui/views/tasks.go`
- Create: `internal/tui/views/tasks_test.go`
- Modify: `internal/tui/app.go` — `openTasks()` + `loadTasks()` + `T` keybinding
- Modify: `internal/tui/command.go` — add `tasks` command

**Key change from original plan:** `TasksView` renders `[]task.Task` (core type). It never touches Redis. `app.go` calls `provider.TaskLister` to load tasks.

```go
// internal/tui/views/tasks.go
package views

import (
    "github.com/zanetworker/aimux/internal/task" // core import — correct
    // NO redis, NO k8s, NO provider imports
)

type TasksView struct {
    tasks  []task.Task
    cursor int
    width  int
    height int
}

func NewTasksView() *TasksView { return &TasksView{} }
func (v *TasksView) SetTasks(tasks []task.Task) { v.tasks = tasks }
func (v *TasksView) Tasks() []task.Task { return v.tasks }
// ... rendering only
```

```go
// internal/tui/app.go
func (a App) loadTasks() []task.Task {
    for _, p := range a.providers {
        if tl, ok := p.(provider.TaskLister); ok {
            tasks, err := tl.ListTasks()
            if err == nil {
                return tasks
            }
        }
    }
    return nil
}
```

**Test:**

```go
// internal/tui/views/tasks_test.go
func TestTasksView_RendersTasks(t *testing.T) {
    tasks := []task.Task{
        {ID: "abc123", Prompt: "Research LangGraph", Status: "completed", Loc: "k8s"},
        {ID: "def456", Prompt: "Implement API",      Status: "in_progress", Loc: "k8s"},
    }
    tv := views.NewTasksView()
    tv.SetSize(120, 30)
    tv.SetTasks(tasks)
    rendered := tv.View()
    if !strings.Contains(rendered, "abc123") {
        t.Error("expected task ID in render")
    }
}
```

**Verify separation:**

```bash
grep -r "redis\|go-redis\|k8s\|client-go" internal/tui/views/tasks.go
```

Expected: no output.

Commit message: `feat: add Tasks view (T keybinding), reads from provider.TaskLister`

---

### Task 7: Header task summary

**Files:**
- Modify: wherever the header/status bar is rendered in `app.go`

Simple count display using `a.tasksView.Tasks()`. No new dependencies.

Commit message: `feat: add task summary counts to header bar`

---

## Architectural invariant — verify at end

After all tasks are complete, run:

```bash
# No TUI types in core packages
grep -r "bubbletea\|lipgloss\|charmbracelet" internal/task/ internal/provider/
# Expected: no output

# Full test suite passes
go test ./... -timeout 30s
# Expected: all green

# Build is clean
go build ./... && go vet ./...
# Expected: no errors
```
