# Unified Launcher and Tasks Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `:new` picker (Session/Task), a Where toggle (Local/Remote) to the session launcher, a minimal task launcher, and a new Tasks view showing all tasks from Redis and local files.

**Architecture:** Five additive changes to the TUI. New `PickerView` and `TaskLauncherView` in `views/`. Existing `LauncherView` gets a `where` toggle. New `TasksView` reads Redis + `~/.claude/tasks/`. All wired in `app.go`. No existing behavior changes.

**Tech Stack:** Go, Bubble Tea, Lipgloss, go-redis/v9 (already in go.mod), aimux provider/k8s.go for Redis config.

---

### Task 1: `:new` picker

The `n` keybinding currently calls `openLauncher()` directly. Change it to open a tiny two-option picker first.

**Files:**
- Modify: `internal/tui/views/launcher.go` — add `PickerView`
- Modify: `internal/tui/app.go` — wire picker

**Step 1: Add `PickerView` and `PickerMsg` to launcher.go**

Add at the top of `launcher.go` (after existing message types):

```go
// PickerChoice is emitted when the user picks Session or Task from :new.
type PickerChoice int

const (
    PickerSession PickerChoice = iota
    PickerTask
)

type PickerMsg struct{ Choice PickerChoice }
type PickerCancelMsg struct{}

// PickerView is the tiny :new overlay — choose Session or Task.
type PickerView struct {
    cursor int // 0=Session, 1=Task
    width  int
    height int
}

func NewPickerView() *PickerView { return &PickerView{} }

func (p *PickerView) SetSize(w, h int) { p.width = w; p.height = h }

func (p *PickerView) Update(msg tea.Msg) tea.Cmd {
    km, ok := msg.(tea.KeyMsg)
    if !ok {
        return nil
    }
    switch km.String() {
    case "esc":
        return func() tea.Msg { return PickerCancelMsg{} }
    case "j", "down":
        if p.cursor < 1 { p.cursor++ }
    case "k", "up":
        if p.cursor > 0 { p.cursor-- }
    case "s":
        return func() tea.Msg { return PickerMsg{PickerSession} }
    case "t":
        return func() tea.Msg { return PickerMsg{PickerTask} }
    case "enter":
        choice := PickerChoice(p.cursor)
        return func() tea.Msg { return PickerMsg{choice} }
    }
    return nil
}

func (p *PickerView) View() string {
    items := []string{"Session", "Task"}
    var b strings.Builder

    // Center vertically
    topPad := (p.height - 6) / 3
    for i := 0; i < topPad; i++ {
        b.WriteString(strings.Repeat(" ", p.width) + "\n")
    }

    // Box
    boxW := 20
    leftPad := strings.Repeat(" ", (p.width-boxW)/2)
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("┌"+strings.Repeat("─", boxW)+"┐") + "\n")
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("│") + "  " + launcherTitleStyle.Render("New") + strings.Repeat(" ", boxW-3) + launcherBoxBorderStyle.Render("│") + "\n")
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("│") + strings.Repeat(" ", boxW+2) + launcherBoxBorderStyle.Render("│") + "\n")
    for i, item := range items {
        prefix := "  "
        style := launcherOptionStyle
        if i == p.cursor {
            prefix = "▸ "
            style = launcherSelectedStyle
        }
        line := prefix + style.Render(fmt.Sprintf("[%s] %s", string(item[0]), item))
        padding := boxW - lipgloss.Width(line) + 2
        if padding < 0 { padding = 0 }
        b.WriteString(leftPad + launcherBoxBorderStyle.Render("│") + line + strings.Repeat(" ", padding) + launcherBoxBorderStyle.Render("│") + "\n")
    }
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("│") + strings.Repeat(" ", boxW+2) + launcherBoxBorderStyle.Render("│") + "\n")
    hint := launcherHintStyle.Render("  s/t:pick  ↵:confirm  esc:cancel  ")
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("│") + hint + strings.Repeat(" ", boxW-lipgloss.Width(hint)+2) + launcherBoxBorderStyle.Render("│") + "\n")
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("└"+strings.Repeat("─", boxW)+"┘") + "\n")

    // Fill remaining
    lines := topPad + 7
    for i := lines; i < p.height; i++ {
        b.WriteString(strings.Repeat(" ", p.width) + "\n")
    }
    return b.String()
}
```

**Step 2: Add `pickerView` field to `App` struct in app.go**

Find the `App` struct. Add:
```go
pickerView *views.PickerView
```

And a new view constant (find where `viewAgents`, `viewLogs` etc are defined):
```go
viewPicker // :new picker overlay
```

**Step 3: Replace `openLauncher()` call with `openPicker()` in app.go**

Find where `openLauncher()` is called (keybinding `n` and command `new`). Replace with:

```go
func (a App) openPicker() (tea.Model, tea.Cmd) {
    if a.pickerView == nil {
        a.pickerView = views.NewPickerView()
    }
    a.pickerView.SetSize(a.width, a.height)
    a.currentView = viewPicker
    return a, nil
}
```

Update the keybinding handler and `executeCommand("new")` to call `openPicker()` instead.

**Step 4: Handle `PickerMsg` and `PickerCancelMsg` in app.go `Update()`**

In the `switch msg := msg.(type)` block, add:
```go
case views.PickerMsg:
    a.currentView = viewAgents
    switch msg.Choice {
    case views.PickerSession:
        return a.openLauncher()
    case views.PickerTask:
        return a.openTaskLauncher()
    }
case views.PickerCancelMsg:
    a.currentView = viewAgents
    return a, nil
```

**Step 5: Route key events to pickerView when active**

In the main key handler, add a check at the top:
```go
if a.currentView == viewPicker && a.pickerView != nil {
    cmd := a.pickerView.Update(msg)
    return a, cmd
}
```

**Step 6: Render pickerView in View()**

In `View()`, add a case for `viewPicker`:
```go
case viewPicker:
    if a.pickerView != nil {
        return a.pickerView.View()
    }
```

**Step 7: Build and test**

```bash
go build ./... 2>&1
go test ./internal/tui/... -timeout 30s 2>&1
```

Expected: builds clean, existing tests pass.

**Step 8: Smoke test manually**

Run `./aimux`, press `n`. Should see the picker overlay. Press `esc` — should return to agents view. Press `s` — should open the existing launcher. Press `t` — should show a "not implemented" stub (we'll build it next).

**Step 9: Commit**

```bash
git add internal/tui/views/launcher.go internal/tui/app.go
git commit -m "feat: add :new picker (Session / Task)"
```

---

### Task 2: Session launcher — Where toggle

Add a Local/Remote toggle to the existing session launcher. The toggle is the first field shown in the provider step. `Tab` switches between Local and Remote.

**Files:**
- Modify: `internal/tui/views/launcher.go` — add `where` field + toggle rendering
- Modify: `internal/tui/app.go` — handle `Remote` flag in `LaunchMsg`

**Step 1: Add `where` and `repoURL` to `LauncherView` and `LaunchMsg`**

In `LauncherView` struct, add:
```go
where string // "local" or "remote"
```

In `LaunchMsg`, add:
```go
Remote  bool
RepoURL string // git remote origin URL, populated when Remote=true
```

Initialize `where` to `"local"` in `NewLauncherView()`.

**Step 2: Add Tab handling to `updateProvider()`**

```go
case "tab":
    if l.where == "local" {
        l.where = "remote"
    } else {
        l.where = "local"
    }
```

**Step 3: Render the Where toggle in `viewProvider()`**

At the top of `viewProvider()`, before the provider list, add:

```go
localTab := launcherInactiveTabStyle.Render("Local")
remoteTab := launcherInactiveTabStyle.Render("Remote")
if l.where == "local" {
    localTab = launcherActiveTabStyle.Render("Local")
} else {
    remoteTab = launcherActiveTabStyle.Render("Remote")
}
b.WriteString(launcherLabelStyle.Render("Where: ") + localTab + " " + remoteTab + "\n\n")
```

Update the hint line to mention Tab:
```go
b.WriteString(launcherHintStyle.Render("j/k:provider  Tab:local/remote  Enter:next  Esc:cancel"))
```

**Step 4: Pass `where` through `emitLaunch()`**

In `emitLaunch()`, populate the new fields:

```go
remote := l.where == "remote"
repoURL := ""
if remote {
    // Read git remote origin from selected directory
    dir := l.selectedDir()
    if dir != "" {
        out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
        if err == nil {
            repoURL = strings.TrimSpace(string(out))
        }
    }
}

msg := LaunchMsg{
    Provider:    l.providers[l.providerCursor],
    Dir:         dir,
    Model:       model,
    Mode:        mode,
    Runtime:     l.runtimes[l.runtimeCursor],
    OTELEnabled: l.otelEnabled,
    Remote:      remote,
    RepoURL:     repoURL,
}
```

Add `"os/exec"` to imports if not present.

**Step 5: Handle `Remote` flag in app.go `handleLaunch()`**

Find where `LaunchMsg` is handled in `app.go`. After the existing logic, add:

```go
if msg.Remote {
    // For remote sessions: inject context that K8s agents are available.
    // The repo URL is passed as an environment variable so Claude's session
    // knows which repo K8s agents should clone.
    if msg.RepoURL != "" {
        cmd.Env = append(cmd.Env, "AIMUX_K8S_REPO="+msg.RepoURL)
    }
    // TODO: inject K8s context hint into Claude's CLAUDE.md or initial prompt
    // For now, the MCP server is already registered globally.
}
```

**Step 6: Build and test**

```bash
go build ./... 2>&1
go test ./internal/tui/... -timeout 30s 2>&1
```

**Step 7: Smoke test**

Run `./aimux`, press `n`, press `s`. In the launcher, press `Tab` — should see the toggle switch between `[Local]` and `[Remote]`. Press `Tab` again — switches back. Launch with Remote selected — should start a local Claude session (same as local for now).

**Step 8: Commit**

```bash
git add internal/tui/views/launcher.go internal/tui/app.go
git commit -m "feat: add Local/Remote toggle to session launcher"
```

---

### Task 3: Task launcher

A new minimal overlay for fire-and-forget tasks. Simple: pick where → pick provider (with availability) → type prompt → launch.

**Files:**
- Create: `internal/tui/views/task_launcher.go`
- Modify: `internal/tui/app.go` — `openTaskLauncher()` + handle `LaunchTaskMsg`

**Step 1: Write failing test for TaskLauncherView**

In a new file `internal/tui/views/task_launcher_test.go`:

```go
package views_test

import (
    "testing"
    "github.com/zanetworker/aimux/internal/tui/views"
)

func TestTaskLauncher_EmitsLaunchMsg_OnEnter(t *testing.T) {
    tl := views.NewTaskLauncherView([]views.ProviderAvailability{
        {Name: "claude", Available: true},
    })
    tl.SetSize(80, 24)
    // Type a prompt character by character
    tl.SetPrompt("research AI")
    // Simulate Enter
    cmd := tl.TriggerLaunch()
    if cmd == nil {
        t.Fatal("expected LaunchTaskMsg, got nil cmd")
    }
}
```

Run: `go test ./internal/tui/views/... -run TestTaskLauncher -v`
Expected: **FAIL** — `views.NewTaskLauncherView` undefined.

**Step 2: Create `internal/tui/views/task_launcher.go`**

```go
package views

import (
    "fmt"
    "strings"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"
)

// ProviderAvailability describes a provider and whether it has a running
// K8s deployment available.
type ProviderAvailability struct {
    Name      string
    Available bool // true = deployment exists and healthy
}

// LaunchTaskMsg is emitted when the user confirms a task launch.
type LaunchTaskMsg struct {
    Where    string // "local" or "remote"
    Provider string
    Prompt   string
}

type taskLauncherState int

const (
    tlStateWhere taskLauncherState = iota
    tlStateProvider
    tlStatePrompt
)

// TaskLauncherView is the minimal :new Task overlay.
type TaskLauncherView struct {
    state          taskLauncherState
    where          string // "local" or "remote"
    providers      []ProviderAvailability
    providerCursor int
    prompt         string
    width          int
    height         int
}

func NewTaskLauncherView(providers []ProviderAvailability) *TaskLauncherView {
    return &TaskLauncherView{
        state:     tlStateWhere,
        where:     "remote",
        providers: providers,
    }
}

func (t *TaskLauncherView) SetSize(w, h int) { t.width = w; t.height = h }
func (t *TaskLauncherView) SetPrompt(p string) { t.prompt = p }

func (t *TaskLauncherView) TriggerLaunch() tea.Cmd {
    if t.prompt == "" {
        return nil
    }
    provider := ""
    if t.providerCursor < len(t.providers) {
        provider = t.providers[t.providerCursor].Name
    }
    msg := LaunchTaskMsg{Where: t.where, Provider: provider, Prompt: t.prompt}
    return func() tea.Msg { return msg }
}

func (t *TaskLauncherView) Update(msg tea.Msg) tea.Cmd {
    km, ok := msg.(tea.KeyMsg)
    if !ok {
        return nil
    }
    key := km.String()
    if key == "esc" {
        return func() tea.Msg { return LaunchCancelMsg{} }
    }
    switch t.state {
    case tlStateWhere:
        switch key {
        case "tab", "h", "l", "left", "right":
            if t.where == "local" { t.where = "remote" } else { t.where = "local" }
        case "enter":
            t.state = tlStateProvider
        }
    case tlStateProvider:
        switch key {
        case "j", "down":
            if t.providerCursor < len(t.providers)-1 { t.providerCursor++ }
        case "k", "up":
            if t.providerCursor > 0 { t.providerCursor-- }
        case "enter":
            t.state = tlStatePrompt
        case "esc":
            t.state = tlStateWhere
        }
    case tlStatePrompt:
        switch key {
        case "enter":
            return t.TriggerLaunch()
        case "backspace":
            if len(t.prompt) > 0 {
                t.prompt = t.prompt[:len(t.prompt)-1]
            }
        default:
            if len(key) == 1 {
                t.prompt += key
            }
        }
    }
    return nil
}

func (t *TaskLauncherView) View() string {
    var b strings.Builder

    topPad := (t.height - 14) / 3
    if topPad < 1 { topPad = 1 }
    for i := 0; i < topPad; i++ {
        b.WriteString(strings.Repeat(" ", t.width) + "\n")
    }

    boxW := 50
    leftPad := strings.Repeat(" ", (t.width-boxW-4)/2)
    if (t.width-boxW-4)/2 < 0 { leftPad = "" }

    lines := t.buildLines(boxW)
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("┌"+strings.Repeat("─", boxW+2)+"┐") + "\n")
    for _, line := range lines {
        lineW := lipgloss.Width(line)
        pad := boxW - lineW + 2
        if pad < 0 { pad = 0 }
        b.WriteString(leftPad + launcherBoxBorderStyle.Render("│") + " " + line + strings.Repeat(" ", pad) + launcherBoxBorderStyle.Render("│") + "\n")
    }
    b.WriteString(leftPad + launcherBoxBorderStyle.Render("└"+strings.Repeat("─", boxW+2)+"┘") + "\n")

    rendered := topPad + len(lines) + 2
    for i := rendered; i < t.height; i++ {
        b.WriteString(strings.Repeat(" ", t.width) + "\n")
    }
    return b.String()
}

func (t *TaskLauncherView) buildLines(boxW int) []string {
    var lines []string

    lines = append(lines, launcherTitleStyle.Render("New Task"))
    lines = append(lines, "")

    // Where toggle
    localTab := launcherInactiveTabStyle.Render("Local")
    remoteTab := launcherInactiveTabStyle.Render("Remote")
    if t.where == "local" { localTab = launcherActiveTabStyle.Render("Local") } else { remoteTab = launcherActiveTabStyle.Render("Remote") }
    whereActive := t.state == tlStateWhere
    whereLabel := launcherLabelStyle.Render("Where:  ")
    if whereActive { whereLabel = launcherSelectedStyle.Render("Where:  ") }
    lines = append(lines, whereLabel+localTab+" "+remoteTab)
    lines = append(lines, "")

    // Provider list
    for i, p := range t.providers {
        cursor := "  "
        style := launcherOptionStyle
        active := t.state == tlStateProvider && i == t.providerCursor
        if active { cursor = "▸ "; style = launcherSelectedStyle }
        avail := "●"
        if !p.Available { avail = "○" }
        lines = append(lines, cursor+style.Render(p.Name)+"  "+launcherDimStyle.Render(avail))
    }
    lines = append(lines, "")

    // Prompt input
    promptLabel := launcherLabelStyle.Render("Task:   ")
    if t.state == tlStatePrompt { promptLabel = launcherSelectedStyle.Render("Task:   ") }
    display := t.prompt
    if t.state == tlStatePrompt { display += "█" }
    if len(display) > boxW-10 { display = "..." + display[len(display)-(boxW-13):] }
    lines = append(lines, promptLabel+launcherOptionStyle.Render(display))
    lines = append(lines, "")

    // Hints
    hints := map[taskLauncherState]string{
        tlStateWhere:    "Tab:toggle  Enter:next  Esc:cancel",
        tlStateProvider: "j/k:select  Enter:next  Esc:back",
        tlStatePrompt:   "type task  Enter:launch  Esc:cancel",
    }
    lines = append(lines, launcherHintStyle.Render(hints[t.state]))

    // Unused: suppress warning
    _ = fmt.Sprintf
    return lines
}
```

**Step 3: Run test**

```bash
go test ./internal/tui/views/... -run TestTaskLauncher -v
```

Expected: **PASS**.

**Step 4: Add `taskLauncherView` to `App` and implement `openTaskLauncher()`**

In `app.go`:
```go
taskLauncherView *views.TaskLauncherView
```

Add `viewTaskLauncher` to the view constants.

```go
func (a App) openTaskLauncher() (tea.Model, tea.Cmd) {
    // Build provider availability list from K8s provider
    var avail []views.ProviderAvailability
    for _, p := range a.providers {
        if p.Name() == "k8s" {
            // TODO: query K8s API for deployment availability
            // For now, always mark claude as available
            avail = append(avail, views.ProviderAvailability{Name: "claude", Available: true})
            avail = append(avail, views.ProviderAvailability{Name: "gemini", Available: false})
        }
    }
    if len(avail) == 0 {
        a.statusHint = "K8s provider not configured — add kubernetes block to ~/.aimux/config.yaml"
        return a, nil
    }
    a.taskLauncherView = views.NewTaskLauncherView(avail)
    a.taskLauncherView.SetSize(a.width, a.height)
    a.currentView = viewTaskLauncher
    return a, nil
}
```

**Step 5: Handle `LaunchTaskMsg` in app.go**

```go
case views.LaunchTaskMsg:
    if msg.Where == "remote" {
        return a.handleRemoteTask(msg)
    }
    a.statusHint = "Local task routing coming soon — use Session for local work"
    return a, nil
```

```go
func (a App) handleRemoteTask(msg views.LaunchTaskMsg) (tea.Model, tea.Cmd) {
    // Find K8s provider and use its MCP connection to spawn + create task.
    // For V1: show status and let the user use MCP tools from a Claude session.
    // Full implementation: call spawn_agent + create_task via the MCP server binary.
    a.statusHint = fmt.Sprintf("Task queued: %q — use list_tasks in Claude to track", truncateStr(msg.Prompt, 50))
    a.currentView = viewAgents
    return a, nil
}

func truncateStr(s string, n int) string {
    if len(s) <= n { return s }
    return s[:n-3] + "..."
}
```

Route key events to `taskLauncherView` when active (same pattern as `pickerView`). Render `taskLauncherView.View()` in `View()` switch.

**Step 6: Build and test**

```bash
go build ./... 2>&1
go test ./internal/tui/... -timeout 30s 2>&1
```

**Step 7: Smoke test**

Press `n` → `t`. Should see the task launcher. Tab switches Local/Remote. Enter on provider, type a prompt, Enter to launch. Should see status hint "Task queued".

**Step 8: Commit**

```bash
git add internal/tui/views/task_launcher.go internal/tui/views/task_launcher_test.go internal/tui/app.go
git commit -m "feat: add task launcher overlay"
```

---

### Task 4: Tasks view

New view showing all tasks from Redis (remote) and local task files. `T` keybinding opens it. Selecting a task shows the full result in the right pane.

**Files:**
- Create: `internal/tui/views/tasks.go`
- Create: `internal/tui/views/tasks_test.go`
- Modify: `internal/tui/app.go` — wire `T` keybinding + handle tasks view
- Modify: `internal/tui/command.go` — add `tasks` command

**Step 1: Define the `Task` struct (shared between Redis and local sources)**

At the top of `tasks.go`:

```go
package views

import (
    "fmt"
    "strings"
    "time"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"
)

// Task is a normalized task entry from either Redis or local ~/.claude/tasks/.
type Task struct {
    ID          string
    Prompt      string    // full task prompt
    Status      string    // pending, claimed, in_progress, completed, failed, dead
    Assignee    string    // agent ID or name
    CreatedAt   time.Time
    CompletedAt time.Time
    Summary     string    // result_summary (truncated)
    Error       string    // error message if failed
    Loc         string    // "local" or "k8s"
    Cost        float64   // estimated cost (if available)
}

// StatusIcon returns a single-character status indicator.
func (t Task) StatusIcon() string {
    switch t.Status {
    case "completed": return "✓"
    case "claimed", "in_progress": return "●"
    case "pending": return "○"
    case "failed", "dead": return "✗"
    default: return "?"
    }
}
```

**Step 2: Write failing test for TasksView**

In `tasks_test.go`:

```go
package views_test

import (
    "testing"
    "time"

    "github.com/zanetworker/aimux/internal/tui/views"
)

func TestTasksView_RendersTasks(t *testing.T) {
    tasks := []views.Task{
        {ID: "abc123", Prompt: "Research LangGraph", Status: "completed", Loc: "k8s",
            CreatedAt: time.Now().Add(-45 * time.Minute), Summary: "LangGraph is a graph-based..."},
        {ID: "def456", Prompt: "Implement API", Status: "in_progress", Loc: "k8s",
            Assignee: "agent-claude-coder-abc"},
    }
    tv := views.NewTasksView()
    tv.SetSize(120, 30)
    tv.SetTasks(tasks)

    rendered := tv.View()
    if !strings.Contains(rendered, "abc123") {
        t.Error("expected task ID in render")
    }
    if !strings.Contains(rendered, "Research LangGraph") {
        t.Error("expected task prompt in render")
    }
}
```

Run: `go test ./internal/tui/views/... -run TestTasksView -v`
Expected: **FAIL** — `views.NewTasksView` undefined.

**Step 3: Implement `TasksView` in tasks.go**

```go
// Column widths for the tasks table.
const (
    colTaskStatus = 3
    colTaskID     = 8
    colTaskPrompt = 35
    colTaskAgent  = 20
    colTaskLoc    = 6
    colTaskAge    = 6
    colTaskCost   = 8
)

var (
    taskHeaderStyle   = tableHeaderStyle // reuse from agents.go
    taskSelectedStyle = agentSelectedStyle
    taskDoneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
    taskRunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5F87FF"))
    taskFailStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
    taskPendingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
)

// TasksView renders the task list with a result pane on the right.
type TasksView struct {
    tasks  []Task
    cursor int
    width  int
    height int
}

func NewTasksView() *TasksView { return &TasksView{} }

func (v *TasksView) SetSize(w, h int) { v.width = w; v.height = h }
func (v *TasksView) SetTasks(tasks []Task) { v.tasks = tasks; if v.cursor >= len(tasks) { v.cursor = 0 } }

func (v *TasksView) Selected() *Task {
    if v.cursor < len(v.tasks) { return &v.tasks[v.cursor] }
    return nil
}

func (v *TasksView) Update(msg tea.Msg) tea.Cmd {
    km, ok := msg.(tea.KeyMsg)
    if !ok { return nil }
    switch km.String() {
    case "j", "down":
        if v.cursor < len(v.tasks)-1 { v.cursor++ }
    case "k", "up":
        if v.cursor > 0 { v.cursor-- }
    }
    return nil
}

func (v *TasksView) View() string {
    leftW := v.width * 60 / 100
    rightW := v.width - leftW - 1

    left := v.viewList(leftW)
    right := v.viewResult(rightW)

    var b strings.Builder
    leftLines := strings.Split(left, "\n")
    rightLines := strings.Split(right, "\n")
    maxLines := len(leftLines)
    if len(rightLines) > maxLines { maxLines = len(rightLines) }

    for i := 0; i < maxLines; i++ {
        l := ""
        if i < len(leftLines) { l = leftLines[i] }
        r := ""
        if i < len(rightLines) { r = rightLines[i] }
        lw := lipgloss.Width(l)
        if lw < leftW { l += strings.Repeat(" ", leftW-lw) }
        b.WriteString(l + "│" + r + "\n")
    }
    return b.String()
}

func (v *TasksView) viewList(width int) string {
    var b strings.Builder

    // Header
    header := " " + padRight("", colTaskStatus) + " " +
        padRight("ID", colTaskID) + " " +
        padRight("TASK", colTaskPrompt) + " " +
        padRight("AGENT", colTaskAgent) + " " +
        padRight("LOC", colTaskLoc) + " " +
        padRight("AGE", colTaskAge) + " " +
        padRight("COST", colTaskCost)
    if lipgloss.Width(header) < width {
        header += strings.Repeat(" ", width-lipgloss.Width(header))
    }
    b.WriteString(taskHeaderStyle.Render(header) + "\n")

    if len(v.tasks) == 0 {
        b.WriteString(launcherDimStyle.Render("  No tasks found.") + "\n")
        return b.String()
    }

    visH := v.height - 2
    if visH < 1 { visH = len(v.tasks) }
    start := 0
    if v.cursor >= visH { start = v.cursor - visH + 1 }
    end := start + visH
    if end > len(v.tasks) { end = len(v.tasks) }

    for i := start; i < end; i++ {
        t := v.tasks[i]
        icon := t.StatusIcon()
        iconStyled := icon
        switch t.Status {
        case "completed": iconStyled = taskDoneStyle.Render(icon)
        case "claimed", "in_progress": iconStyled = taskRunningStyle.Render(icon)
        case "failed", "dead": iconStyled = taskFailStyle.Render(icon)
        default: iconStyled = taskPendingStyle.Render(icon)
        }

        prompt := truncate(t.Prompt, colTaskPrompt)
        agent := truncate(t.Assignee, colTaskAgent)
        if agent == "" { agent = launcherDimStyle.Render("(pending)") }
        age := "-"
        if !t.CreatedAt.IsZero() { age = formatAge(t.CreatedAt) }
        cost := "-"
        if t.Cost > 0 { cost = fmt.Sprintf("$%.2f", t.Cost) }

        row := " " + padRight(iconStyled, colTaskStatus) + " " +
            padRight(t.ID, colTaskID) + " " +
            padRight(prompt, colTaskPrompt) + " " +
            padRight(agent, colTaskAgent) + " " +
            padRight(t.Loc, colTaskLoc) + " " +
            padRight(age, colTaskAge) + " " +
            padRight(cost, colTaskCost)

        if i == v.cursor {
            if lipgloss.Width(row) < width { row += strings.Repeat(" ", width-lipgloss.Width(row)) }
            b.WriteString(taskSelectedStyle.Render(row) + "\n")
        } else {
            b.WriteString(row + "\n")
        }
    }
    return b.String()
}

func (v *TasksView) viewResult(width int) string {
    t := v.Selected()
    if t == nil {
        return launcherDimStyle.Render(" No task selected.")
    }
    var b strings.Builder
    b.WriteString(" " + launcherTitleStyle.Render(t.ID) + "  " + launcherDimStyle.Render(t.Status) + "\n")
    b.WriteString(" " + launcherPathStyle.Render(truncate(t.Prompt, width-2)) + "\n\n")
    if t.Summary != "" {
        // Word-wrap summary to width
        words := strings.Fields(t.Summary)
        line := " "
        for _, w := range words {
            if lipgloss.Width(line)+len(w)+1 > width-1 {
                b.WriteString(line + "\n")
                line = " " + w + " "
            } else {
                line += w + " "
            }
        }
        if line != " " { b.WriteString(line + "\n") }
    } else if t.Error != "" {
        b.WriteString(" " + taskFailStyle.Render("Error: "+t.Error) + "\n")
    } else {
        b.WriteString(" " + launcherDimStyle.Render("(no result yet)") + "\n")
    }
    return b.String()
}
```

**Step 4: Run test**

```bash
go test ./internal/tui/views/... -run TestTasksView -v
```

Expected: **PASS**.

**Step 5: Add `tasks` command to command.go**

```go
// In allCommands slice, add "tasks"
var allCommands = []string{
    "instances", "logs", "traces", "session", "teams", "costs",
    "help", "new", "kill", "export", "export-otel", "send", "tasks", "quit",
}
```

**Step 6: Wire `T` keybinding and `tasks` command in app.go**

Add `viewTasks` to the view constants. Add `tasksView *views.TasksView` to `App` struct.

In `Update()` key handler:
```go
case "T":
    return a.openTasks()
```

In `executeCommand()`:
```go
case "tasks":
    return a.openTasks()
```

```go
func (a App) openTasks() (tea.Model, tea.Cmd) {
    if a.tasksView == nil {
        a.tasksView = views.NewTasksView()
    }
    a.tasksView.SetSize(a.width, a.height-1)
    // Load tasks from K8s provider (Redis)
    tasks := a.loadTasks()
    a.tasksView.SetTasks(tasks)
    a.currentView = viewTasks
    a.updateHints()
    return a, nil
}

func (a App) loadTasks() []views.Task {
    // Find K8s provider and read from Redis
    for _, p := range a.providers {
        if p.Name() == "k8s" {
            // K8s provider implements TaskLister (to be added)
            // For now: return empty — Tasks view still renders
            break
        }
    }
    return nil
}
```

Route key events to `tasksView` when `currentView == viewTasks`. Render in `View()`. Add `T:tasks` to `updateHints()`.

**Step 7: Add `TaskLister` optional interface to provider.go**

```go
// TaskLister is an optional interface for providers that can enumerate tasks.
type TaskLister interface {
    ListTasks() ([]views.Task, error)
}
```

Implement `ListTasks()` on `K8s` provider in `k8s.go` — reads `tasks:all` from Redis, returns normalized `[]views.Task`.

**Step 8: Wire `ListTasks()` in `loadTasks()`**

```go
func (a App) loadTasks() []views.Task {
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

**Step 9: Build, test, smoke test**

```bash
go build ./... 2>&1
go test ./... -timeout 30s 2>&1
```

Run `./aimux` with K8s provider configured and Redis accessible. Press `T`. Should see the tasks table. Select a task with j/k — right pane shows result.

**Step 10: Commit**

```bash
git add internal/tui/views/tasks.go internal/tui/views/tasks_test.go \
        internal/tui/app.go internal/tui/command.go \
        internal/provider/provider.go internal/provider/k8s.go
git commit -m "feat: add Tasks view (T keybinding) with Redis + result pane"
```

---

### Task 5: Header task summary

Add task counts to the header bar when tasks are loaded.

**Files:**
- Modify: `internal/tui/views/header.go` (or wherever the header is rendered)

**Step 1: Find the header rendering**

```bash
grep -n "header\|Header\|statusBar" internal/tui/app.go | head -20
grep -rn "func.*header\|renderHeader" internal/tui/views/ | head -10
```

**Step 2: Add task counts to header**

Find where agent count is rendered (e.g., "Agents 6"). After it, add:

```go
if len(a.tasksView.Tasks()) > 0 {
    done := 0; running := 0; pending := 0; failed := 0
    for _, t := range a.tasksView.Tasks() {
        switch t.Status {
        case "completed": done++
        case "claimed", "in_progress": running++
        case "pending": pending++
        case "failed", "dead": failed++
        }
    }
    headerStr += fmt.Sprintf("  Tasks ●%d ✓%d ○%d ✗%d", running, done, pending, failed)
}
```

**Step 3: Add `Tasks()` accessor to `TasksView`**

```go
func (v *TasksView) Tasks() []Task { return v.tasks }
```

**Step 4: Build and test**

```bash
go build ./... && go test ./... -timeout 30s
```

**Step 5: Commit**

```bash
git add internal/tui/views/tasks.go internal/tui/app.go
git commit -m "feat: add task summary counts to header bar"
```

---

## Testing the full flow end-to-end

```bash
# 1. Ensure Redis accessible (LoadBalancer endpoint configured)
# 2. Scale up a researcher
kubectl scale deployment agent-claude-researcher -n agents --replicas=1

# 3. Run aimux
./aimux

# 4. Press n → s → verify session launcher has Local/Remote toggle
# 5. Press n → t → verify task launcher opens
#    Pick Remote, type "What is Redis?", Enter
#    Check status bar shows "Task queued"

# 6. Press T → verify tasks view opens
#    Should show any tasks in Redis

# 7. kubectl scale back to 0
kubectl scale deployment agent-claude-researcher -n agents --replicas=0
```
