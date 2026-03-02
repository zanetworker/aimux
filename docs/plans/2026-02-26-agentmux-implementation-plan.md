# aimux Redesign Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Rename claudetopus to aimux, introduce AgentProvider abstraction, redesign TUI to with in-TUI session preview/zoom via PTY embedding.

**Architecture:** Provider interface abstracts AI CLI discovery (Claude real, Codex/Gemini stubs). PTY embedding via `creack/pty` + `charmbracelet/x/vt` enables in-TUI session viewing. Split-pane layout shows agent list + live preview. Instance model renamed to Agent throughout.

**Tech Stack:** Go 1.24, Bubble Tea, Lip Gloss, creack/pty, charmbracelet/x/vt

---

### Task 1: Rename Module — claudetopus → aimux

**Files:**
- Modify: `go.mod`
- Modify: `cmd/claudetopus/main.go` → move to `cmd/aimux/main.go`
- Modify: `Makefile`
- Modify: `.goreleaser.yml`
- Modify: `.gitignore`
- Modify: All `*.go` files with import paths

**Step 1: Update go.mod module path**

Change line 1 of `go.mod` from:
```
module github.com/zanetworker/claudetopus
```
to:
```
module github.com/zanetworker/aimux
```

**Step 2: Move cmd directory**

Run:
```bash
mkdir -p cmd/aimux
mv cmd/claudetopus/main.go cmd/aimux/main.go
rmdir cmd/claudetopus
```

**Step 3: Update all import paths**

Run:
```bash
find . -name '*.go' -exec sed -i '' 's|github.com/zanetworker/claudetopus|github.com/zanetworker/aimux|g' {} +
```

**Step 4: Update Makefile**

Replace `BINARY=claudetopus` with `BINARY=aimux` and update the build path from `./cmd/claudetopus` to `./cmd/aimux`.

**Step 5: Update .goreleaser.yml**

Change `main: ./cmd/claudetopus` to `main: ./cmd/aimux` and `binary: claudetopus` to `binary: aimux`.

**Step 6: Update .gitignore**

Change `/claudetopus` to `/aimux`.

**Step 7: Update process filter**

In `internal/discovery/process.go`, the `isClaudeProcess` function filters out "claudetopus" from results. Change to filter out "aimux":
```go
if strings.Contains(cmd, "grep") || strings.Contains(cmd, "aimux") {
    return false
}
```

**Step 8: Update test references**

In `internal/discovery/process_test.go`, update the test case:
```go
{"aimux", "user 123 0.0 0.0 0 0 s0 S 10:00 0:00 aimux", false},
```

**Step 9: Verify build and tests**

Run:
```bash
go build ./cmd/aimux && go test ./... -timeout 30s
```

Expected: Clean build, all tests pass.

**Step 10: Commit**

```bash
git add -A
git commit -m "refactor: rename claudetopus to aimux"
```

---

### Task 2: Data Model — Instance → Agent

**Files:**
- Rename: `internal/model/` → `internal/agent/`
- Modify: `internal/agent/agent.go` (was `instance.go`)
- Modify: `internal/agent/agent_test.go` (was `instance_test.go`)
- Modify: All files importing `model`

**Step 1: Move and rename the package**

Run:
```bash
mv internal/model internal/agent
mv internal/agent/instance.go internal/agent/agent.go
mv internal/agent/instance_test.go internal/agent/agent_test.go
```

**Step 2: Update package declaration and type name**

In `internal/agent/agent.go`:
- Change `package model` → `package agent`
- Rename `Instance` → `Agent` throughout the file
- Update doc comments to say "Agent" not "Instance"

```go
package agent

// Agent represents a running AI coding agent session.
type Agent struct {
    PID            int
    Name           string // project name, derived from WorkingDir
    ProviderName   string // "claude", "codex", "gemini"
    SessionID      string
    Model          string
    PermissionMode string
    WorkingDir     string
    Source         SourceType
    StartTime      time.Time
    Status         Status
    TMuxSession    string
    SessionFile    string // path to conversation log
    MemoryMB       uint64
    GitBranch      string
    TokensIn       int64
    TokensOut      int64
    EstCostUSD     float64
    TeamName       string
    TaskID         string
    TaskSubject    string
    LastActivity   time.Time
}
```

Note: Added `Name`, `ProviderName`, and `SessionFile` fields. `Name` is derived from `WorkingDir` in `ShortProject()` — keep `ShortProject()` for now but also populate `Name` during discovery.

**Step 3: Update agent_test.go**

- Change `package model` → `package agent`
- Rename `Instance{` → `Agent{` in all test constructors

**Step 4: Update all imports**

Every file that imports `github.com/zanetworker/aimux/internal/model` must change to `github.com/zanetworker/aimux/internal/agent`. Every reference to `model.Instance` becomes `agent.Agent`, `model.Status*` becomes `agent.Status*`, etc.

Files to update:
- `internal/discovery/orchestrator.go`
- `internal/discovery/process.go`
- `internal/discovery/process_test.go`
- `internal/tui/app.go`
- `internal/tui/views/instances.go`
- `internal/tui/views/costs.go`

Run:
```bash
find . -name '*.go' -exec sed -i '' 's|/internal/model"|/internal/agent"|g' {} +
find . -name '*.go' -exec sed -i '' 's|model\.Instance|agent.Agent|g' {} +
find . -name '*.go' -exec sed -i '' 's|model\.Status|agent.Status|g' {} +
find . -name '*.go' -exec sed -i '' 's|model\.Source|agent.Source|g' {} +
find . -name '*.go' -exec sed -i '' 's|"github.com/zanetworker/aimux/internal/model"|"github.com/zanetworker/aimux/internal/agent"|g' {} +
```

Manually verify each file compiles — sed may miss edge cases.

**Step 5: Verify build and tests**

Run:
```bash
go build ./cmd/aimux && go test ./... -timeout 30s
```

Expected: Clean build, all tests pass.

**Step 6: Commit**

```bash
git add -A
git commit -m "refactor: rename Instance to Agent, model to agent package"
```

---

### Task 3: Provider Interface

**Files:**
- Create: `internal/provider/provider.go`
- Create: `internal/provider/provider_test.go`

**Step 1: Write the test**

```go
// internal/provider/provider_test.go
package provider

import (
    "testing"
    "time"
)

func TestRoleString(t *testing.T) {
    tests := []struct {
        role Role
        want string
    }{
        {RoleUser, "User"},
        {RoleAssistant, "Assistant"},
        {RoleTool, "Tool"},
        {RoleSystem, "System"},
        {Role(99), "Unknown"},
    }
    for _, tt := range tests {
        if got := tt.role.String(); got != tt.want {
            t.Errorf("Role(%d).String() = %q, want %q", tt.role, got, tt.want)
        }
    }
}

func TestSegmentIsEmpty(t *testing.T) {
    empty := Segment{}
    if empty.Content != "" {
        t.Error("zero-value Segment should have empty Content")
    }

    filled := Segment{
        Time:    time.Now(),
        Role:    RoleUser,
        Content: "hello",
    }
    if filled.Content == "" {
        t.Error("filled Segment should have non-empty Content")
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/ -v`
Expected: FAIL — package does not exist.

**Step 3: Write the implementation**

```go
// internal/provider/provider.go
package provider

import (
    "os/exec"
    "time"

    "github.com/zanetworker/aimux/internal/agent"
)

// Provider discovers and manages AI CLI agents of a specific type.
type Provider interface {
    // Name returns the provider identifier (e.g., "claude", "codex", "gemini").
    Name() string

    // Discover finds running agents of this type.
    // Returns nil slice (not error) when none are found.
    Discover() ([]agent.Agent, error)

    // ResumeCommand builds an exec.Cmd to resume/attach to a session.
    // Returns nil if the agent doesn't support resuming.
    ResumeCommand(a agent.Agent) *exec.Cmd

    // ParseConversation reads a session file and returns conversation segments.
    ParseConversation(sessionPath string) ([]Segment, error)
}

// Segment is a single conversation turn, provider-agnostic.
type Segment struct {
    Time    time.Time
    Role    Role
    Content string
    Tool    string // tool name if Role==RoleTool, empty otherwise
    Detail  string // e.g., file path, command snippet
}

// Role identifies who produced a conversation segment.
type Role int

const (
    RoleUser Role = iota
    RoleAssistant
    RoleTool
    RoleSystem
)

// String returns a human-readable role name.
func (r Role) String() string {
    switch r {
    case RoleUser:
        return "User"
    case RoleAssistant:
        return "Assistant"
    case RoleTool:
        return "Tool"
    case RoleSystem:
        return "System"
    default:
        return "Unknown"
    }
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/provider/
git commit -m "feat: add provider interface and segment types"
```

---

### Task 4: Claude Provider

**Files:**
- Create: `internal/provider/claude.go`
- Create: `internal/provider/claude_test.go`
- Modify: `internal/discovery/orchestrator.go` — refactor to use providers

**Step 1: Write the test**

```go
// internal/provider/claude_test.go
package provider

import (
    "os/exec"
    "testing"

    "github.com/zanetworker/aimux/internal/agent"
)

func TestClaudeName(t *testing.T) {
    p := &Claude{}
    if got := p.Name(); got != "claude" {
        t.Errorf("Name() = %q, want %q", got, "claude")
    }
}

func TestClaudeResumeCommandWithSessionID(t *testing.T) {
    p := &Claude{}
    a := agent.Agent{SessionID: "sess-abc", WorkingDir: "/tmp/proj"}
    cmd := p.ResumeCommand(a)
    if cmd == nil {
        t.Fatal("expected non-nil cmd")
    }
    args := cmd.Args
    if len(args) < 3 || args[1] != "--resume" || args[2] != "sess-abc" {
        t.Errorf("unexpected args: %v", args)
    }
    if cmd.Dir != "/tmp/proj" {
        t.Errorf("Dir = %q, want %q", cmd.Dir, "/tmp/proj")
    }
}

func TestClaudeResumeCommandContinue(t *testing.T) {
    p := &Claude{}
    a := agent.Agent{WorkingDir: "/tmp/proj"}
    cmd := p.ResumeCommand(a)
    if cmd == nil {
        t.Fatal("expected non-nil cmd")
    }
    found := false
    for _, arg := range cmd.Args {
        if arg == "--continue" {
            found = true
        }
    }
    if !found {
        t.Errorf("expected --continue in args: %v", cmd.Args)
    }
}

func TestClaudeResumeCommandNil(t *testing.T) {
    p := &Claude{}
    a := agent.Agent{}
    cmd := p.ResumeCommand(a)
    if cmd != nil {
        t.Error("expected nil cmd for agent with no session or dir")
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/ -v -run TestClaude`
Expected: FAIL — Claude type not defined.

**Step 3: Write the implementation**

```go
// internal/provider/claude.go
package provider

import (
    "os/exec"

    "github.com/zanetworker/aimux/internal/agent"
    "github.com/zanetworker/aimux/internal/discovery"
)

// Claude discovers and manages Claude Code CLI agents.
type Claude struct{}

func (c *Claude) Name() string { return "claude" }

func (c *Claude) Discover() ([]agent.Agent, error) {
    orch := discovery.NewOrchestrator()
    agents, err := orch.Discover()
    if err != nil {
        return nil, err
    }
    for i := range agents {
        agents[i].ProviderName = "claude"
        if agents[i].Name == "" {
            agents[i].Name = agents[i].ShortProject()
        }
    }
    return agents, nil
}

func (c *Claude) ResumeCommand(a agent.Agent) *exec.Cmd {
    bin := findBinary("claude")
    var cmd *exec.Cmd
    if a.SessionID != "" {
        cmd = exec.Command(bin, "--resume", a.SessionID)
    } else if a.WorkingDir != "" {
        cmd = exec.Command(bin, "--continue")
    } else {
        return nil
    }
    if a.WorkingDir != "" {
        cmd.Dir = a.WorkingDir
    }
    return cmd
}

func (c *Claude) ParseConversation(sessionPath string) ([]Segment, error) {
    // Delegates to existing JSONL parsing, converting to []Segment.
    // This will be implemented in Task 5 when we refactor the logs view.
    return nil, nil
}

// findBinary returns the path to a binary, falling back to the bare name.
func findBinary(name string) string {
    path, err := exec.LookPath(name)
    if err != nil {
        return name
    }
    return path
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/ -v -run TestClaude`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/provider/claude.go internal/provider/claude_test.go
git commit -m "feat: add Claude provider implementation"
```

---

### Task 5: Codex and Gemini Stubs

**Files:**
- Create: `internal/provider/codex.go`
- Create: `internal/provider/gemini.go`
- Create: `internal/provider/stubs_test.go`

**Step 1: Write the test**

```go
// internal/provider/stubs_test.go
package provider

import (
    "testing"

    "github.com/zanetworker/aimux/internal/agent"
)

func TestCodexStub(t *testing.T) {
    p := &Codex{}
    if p.Name() != "codex" {
        t.Errorf("Name() = %q, want %q", p.Name(), "codex")
    }
    agents, err := p.Discover()
    if err != nil {
        t.Fatalf("Discover() error: %v", err)
    }
    if len(agents) != 0 {
        t.Errorf("Discover() returned %d agents, want 0", len(agents))
    }
    cmd := p.ResumeCommand(agent.Agent{SessionID: "test"})
    if cmd != nil {
        t.Error("ResumeCommand() should return nil for stub")
    }
    segs, err := p.ParseConversation("/fake/path")
    if err != nil {
        t.Fatalf("ParseConversation() error: %v", err)
    }
    if len(segs) != 0 {
        t.Errorf("ParseConversation() returned %d segments, want 0", len(segs))
    }
}

func TestGeminiStub(t *testing.T) {
    p := &Gemini{}
    if p.Name() != "gemini" {
        t.Errorf("Name() = %q, want %q", p.Name(), "gemini")
    }
    agents, err := p.Discover()
    if err != nil {
        t.Fatalf("Discover() error: %v", err)
    }
    if len(agents) != 0 {
        t.Errorf("Discover() returned %d agents, want 0", len(agents))
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/ -v -run "TestCodex|TestGemini"`
Expected: FAIL.

**Step 3: Write the implementations**

```go
// internal/provider/codex.go
package provider

import (
    "os/exec"

    "github.com/zanetworker/aimux/internal/agent"
)

// Codex discovers OpenAI Codex CLI agents.
// Stub implementation — returns empty results until real support is built.
type Codex struct{}

func (c *Codex) Name() string                                       { return "codex" }
func (c *Codex) Discover() ([]agent.Agent, error)                   { return nil, nil }
func (c *Codex) ResumeCommand(a agent.Agent) *exec.Cmd              { return nil }
func (c *Codex) ParseConversation(sessionPath string) ([]Segment, error) { return nil, nil }
```

```go
// internal/provider/gemini.go
package provider

import (
    "os/exec"

    "github.com/zanetworker/aimux/internal/agent"
)

// Gemini discovers Google Gemini CLI agents.
// Stub implementation — returns empty results until real support is built.
type Gemini struct{}

func (g *Gemini) Name() string                                       { return "gemini" }
func (g *Gemini) Discover() ([]agent.Agent, error)                   { return nil, nil }
func (g *Gemini) ResumeCommand(a agent.Agent) *exec.Cmd              { return nil }
func (g *Gemini) ParseConversation(sessionPath string) ([]Segment, error) { return nil, nil }
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/ -v`
Expected: All PASS.

**Step 5: Commit**

```bash
git add internal/provider/codex.go internal/provider/gemini.go internal/provider/stubs_test.go
git commit -m "feat: add Codex and Gemini provider stubs"
```

---

### Task 6: Refactor Discovery Orchestrator to Use Providers

**Files:**
- Modify: `internal/discovery/orchestrator.go`
- Modify: `cmd/aimux/main.go`
- Modify: `internal/tui/app.go`

**Step 1: Refactor orchestrator to accept providers**

The orchestrator should no longer hardcode Claude-specific discovery. Instead, it iterates over a list of providers.

```go
// internal/discovery/orchestrator.go
package discovery

import (
    "os"
    "path/filepath"
    "time"

    "github.com/zanetworker/aimux/internal/agent"
    "github.com/zanetworker/aimux/internal/cost"
    "github.com/zanetworker/aimux/internal/provider"
)

// Orchestrator coordinates all providers to produce enriched agents.
type Orchestrator struct {
    providers   []provider.Provider
    projectsDir string
}

// NewOrchestrator creates an orchestrator with the given providers.
func NewOrchestrator(providers ...provider.Provider) *Orchestrator {
    home, _ := os.UserHomeDir()
    return &Orchestrator{
        providers:   providers,
        projectsDir: filepath.Join(home, ".claude", "projects"),
    }
}

// Discover finds all agents across all providers.
func (o *Orchestrator) Discover() ([]agent.Agent, error) {
    var all []agent.Agent
    for _, p := range o.providers {
        agents, err := p.Discover()
        if err != nil {
            continue // don't let one provider failure break everything
        }
        all = append(all, agents...)
    }
    return all, nil
}

// ProviderFor returns the provider matching the given name, or nil.
func (o *Orchestrator) ProviderFor(name string) provider.Provider {
    for _, p := range o.providers {
        if p.Name() == name {
            return p
        }
    }
    return nil
}
```

Note: The Claude provider's `Discover()` method (from Task 4) will internally call the existing `ScanProcesses()` and enrichment logic. The orchestrator just iterates providers.

**Step 2: Update app.go to pass providers to orchestrator**

In `internal/tui/app.go`, update `NewApp()`:
```go
func NewApp() App {
    providers := []provider.Provider{
        &provider.Claude{},
        &provider.Codex{},
        &provider.Gemini{},
    }
    return App{
        currentView:   viewInstances,
        instancesView: views.NewAgentsView(),
        costsView:     views.NewCostsView(),
        teamsView:     views.NewTeamsView(),
        helpView:      views.NewHelpView(),
        orchestrator:  discovery.NewOrchestrator(providers...),
        breadcrumbs:   []string{"Agents"},
    }
}
```

**Step 3: Verify build and tests**

Run:
```bash
go build ./cmd/aimux && go test ./... -timeout 30s
```

Expected: Clean build, all tests pass.

**Step 4: Commit**

```bash
git add -A
git commit -m "refactor: orchestrator uses provider interface"
```

---

### Task 7: Header View

**Files:**
- Create: `internal/tui/views/header.go`
- Modify: `internal/tui/app.go` — use new header
- Modify: `internal/tui/styles.go` — add color palette

**Step 1: Update styles.go with palette**

```go
package tui

import "github.com/charmbracelet/lipgloss"

// color palette.
const (
    colorLogo      = lipgloss.Color("#5F87FF") // Blue logo
    colorActive    = lipgloss.Color("#22C55E")  // Green
    colorIdle      = lipgloss.Color("#6B7280")  // Gray
    colorWaiting   = lipgloss.Color("#F59E0B")  // Amber
    colorError     = lipgloss.Color("#EF4444")  // Red
    colorBorder    = lipgloss.Color("#374151")  // Dark gray
    colorHeader    = lipgloss.Color("#E5E7EB")  // Light gray
    colorMuted     = lipgloss.Color("#9CA3AF")  // Medium gray
    colorCost      = lipgloss.Color("#34D399")  // Emerald
    colorTableHead = lipgloss.Color("#5F87FF")  // Blue table headers
    colorSelected  = lipgloss.Color("#1E3A5F")  // Dark blue selection
    colorInfoBox   = lipgloss.Color("#1C1C2E")  // Dark bg for info boxes
    colorInfoBorder = lipgloss.Color("#3B3B5C") // Border for info boxes
)

// StatusStyle returns a lipgloss style colored for the given status string.
func StatusStyle(status string) lipgloss.Style {
    switch status {
    case "Active":
        return lipgloss.NewStyle().Foreground(colorActive)
    case "Idle":
        return lipgloss.NewStyle().Foreground(colorIdle)
    case "Waiting":
        return lipgloss.NewStyle().Foreground(colorWaiting)
    default:
        return lipgloss.NewStyle().Foreground(colorMuted)
    }
}
```

**Step 2: Create header.go**

```go
// internal/tui/views/header.go
package views

import (
    "fmt"
    "strings"

    "github.com/charmbracelet/lipgloss"
    "github.com/zanetworker/aimux/internal/agent"
)

var (
    logoStyle = lipgloss.NewStyle().
        Foreground(lipgloss.Color("#5F87FF")).
        Bold(true)

    infoBoxStyle = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(lipgloss.Color("#3B3B5C")).
        Padding(0, 1)

    infoLabelStyle = lipgloss.NewStyle().
        Foreground(lipgloss.Color("#9CA3AF"))

    infoValueStyle = lipgloss.NewStyle().
        Foreground(lipgloss.Color("#E5E7EB")).
        Bold(true)

    crumbStyle = lipgloss.NewStyle().
        Foreground(lipgloss.Color("#5F87FF")).
        Bold(true)
)

// logo is a small ASCII art logo for aimux.
var logo = []string{
    " █████╗ ██╗███╗   ███╗██╗   ██╗██╗  ██╗",
    "██╔══██╗██║████╗ ████║██║   ██║╚██╗██╔╝",
    "███████║██║██╔████╔██║██║   ██║ ╚███╔╝ ",
    "██╔══██║██║██║╚██╔╝██║██║   ██║ ██╔██╗ ",
    "██║  ██║██║██║ ╚═╝ ██║╚██████╔╝██╔╝ ██╗",
    "╚═╝  ╚═╝╚═╝╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═╝",
}

// HeaderView renders the header with info boxes and logo.
type HeaderView struct {
    agents     []agent.Agent
    crumbs     []string
    width      int
}

// NewHeaderView creates a new header view.
func NewHeaderView() *HeaderView {
    return &HeaderView{
        crumbs: []string{"Agents"},
    }
}

// SetAgents updates the agent list for stats.
func (h *HeaderView) SetAgents(agents []agent.Agent) {
    h.agents = agents
}

// SetCrumbs updates the breadcrumb trail.
func (h *HeaderView) SetCrumbs(crumbs []string) {
    h.crumbs = crumbs
}

// SetWidth updates the available width.
func (h *HeaderView) SetWidth(w int) {
    h.width = w
}

// View renders the header.
func (h *HeaderView) View() string {
    active, idle, waiting := 0, 0, 0
    var totalCost float64
    providers := map[string]int{}
    for _, a := range h.agents {
        switch a.Status {
        case agent.StatusActive:
            active++
        case agent.StatusIdle:
            idle++
        case agent.StatusWaitingPermission:
            waiting++
        }
        totalCost += a.EstCostUSD
        providers[a.ProviderName]++
    }

    // Info boxes
    agentsBox := infoBoxStyle.Render(
        infoLabelStyle.Render("Agents: ") +
            infoValueStyle.Render(fmt.Sprintf("%d", len(h.agents))) +
            "  " +
            lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render(fmt.Sprintf("●%d", active)) +
            " " +
            lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render(fmt.Sprintf("◐%d", waiting)) +
            " " +
            lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render(fmt.Sprintf("○%d", idle)),
    )

    costBox := infoBoxStyle.Render(
        infoLabelStyle.Render("Cost: ") +
            lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Bold(true).Render(fmt.Sprintf("$%.2f", totalCost)),
    )

    var providerParts []string
    for name, count := range providers {
        if count > 0 {
            providerParts = append(providerParts, fmt.Sprintf("%s:%d", name, count))
        }
    }
    providerStr := strings.Join(providerParts, " ")
    if providerStr == "" {
        providerStr = "none"
    }
    providerBox := infoBoxStyle.Render(
        infoLabelStyle.Render("Providers: ") +
            infoValueStyle.Render(providerStr),
    )

    infoRow := lipgloss.JoinHorizontal(lipgloss.Top, agentsBox, " ", costBox, " ", providerBox)

    // Logo (right-aligned)
    logoStr := logoStyle.Render(strings.Join(logo, "\n"))

    // Combine info (left) + logo (right)
    infoWidth := lipgloss.Width(infoRow)
    logoWidth := lipgloss.Width(logoStr)
    gap := h.width - infoWidth - logoWidth
    if gap < 2 {
        gap = 2
    }

    // Stack info boxes with logo to the right
    topRow := lipgloss.JoinHorizontal(lipgloss.Top,
        infoRow,
        strings.Repeat(" ", gap),
        logoStr,
    )

    // Breadcrumb trail
    crumbTrail := crumbStyle.Render(" " + strings.Join(h.crumbs, " > "))

    return topRow + "\n" + crumbTrail
}
```

**Step 3: Wire header into app.go**

Replace the existing `renderHeader()` method in `app.go` with usage of the new `HeaderView`. Update the `View()` method to use `headerView.View()` instead of `a.renderHeader()`.

**Step 4: Verify build**

Run:
```bash
go build ./cmd/aimux
```

Expected: Clean build.

**Step 5: Commit**

```bash
git add -A
git commit -m "feat: header with info boxes and ASCII logo"
```

---

### Task 8: Rename Instances View → Agents View (Table Style)

**Files:**
- Rename: `internal/tui/views/instances.go` → `internal/tui/views/agents.go`
- Modify: `internal/tui/app.go` — update references
- Modify: `internal/tui/views/agents.go` — table styling, agent-centric columns

**Step 1: Rename file**

Run:
```bash
mv internal/tui/views/instances.go internal/tui/views/agents.go
```

**Step 2: Rewrite the view with columns**

The table should show: NAME, AGENT, MODEL, MODE, AGE, COST (not PID, MEM, PERM).

Key changes:
- Rename `InstancesView` → `AgentsView`
- Rename `SetInstances` → `SetAgents`
- Rename `Selected()` → return `*agent.Agent`
- Column headers: colored with blue background
- Selected row: dark blue highlight
- Status icon inline with name
- AGE column showing duration since StartTime

Update column definitions:
```go
const (
    colName  = 22
    colAgent = 10
    colModel = 14
    colMode  = 14
    colAge   = 8
    colCost  = 8
)
```

Header rendering:
```go
headerBg := lipgloss.NewStyle().
    Background(lipgloss.Color("#1E3A5F")).
    Foreground(lipgloss.Color("#5F87FF")).
    Bold(true)
```

Row format:
```
▸● claudetopus      claude   opus-4.6    dangerously   14m    $0.82
 ● trustyai-oper    claude   sonnet-4.5  plan           8m    $0.31
 ○ llama-stack      claude   haiku-3.5   default        2h    $0.11
```

**Step 3: Add FormatAge helper to agent**

In `internal/agent/agent.go`, add:
```go
// FormatAge returns a human-friendly duration since StartTime.
func (a Agent) FormatAge() string {
    if a.StartTime.IsZero() {
        if a.LastActivity.IsZero() {
            return "-"
        }
        return formatDuration(time.Since(a.LastActivity))
    }
    return formatDuration(time.Since(a.StartTime))
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

**Step 4: Update all references in app.go**

Change `instancesView` → `agentsView`, `SetInstances` → `SetAgents`, etc.

**Step 5: Verify build and tests**

Run:
```bash
go build ./cmd/aimux && go test ./... -timeout 30s
```

**Step 6: Commit**

```bash
git add -A
git commit -m "feat: agents table with colored headers"
```

---

### Task 9: Split Pane Layout Manager

**Files:**
- Create: `internal/tui/layout.go`
- Create: `internal/tui/layout_test.go`

**Step 1: Write the test**

```go
// internal/tui/layout_test.go
package tui

import "testing"

func TestLayoutSplit(t *testing.T) {
    l := NewLayout(120, 40)

    left, right := l.SplitVertical(35)
    if left != 42 { // 35% of 120
        t.Errorf("left = %d, want 42", left)
    }
    if right != 78 { // 120 - 42
        t.Errorf("right = %d, want 78", right)
    }
}

func TestLayoutContentHeight(t *testing.T) {
    l := NewLayout(120, 40)
    // Header takes ~8 lines, status bar takes 1, crumb takes 1
    h := l.ContentHeight(8)
    if h != 31 { // 40 - 8 - 1
        t.Errorf("ContentHeight = %d, want 31", h)
    }
}

func TestLayoutZoomed(t *testing.T) {
    l := NewLayout(120, 40)
    l.SetZoomed(true)
    if !l.IsZoomed() {
        t.Error("expected zoomed")
    }
    l.SetZoomed(false)
    if l.IsZoomed() {
        t.Error("expected not zoomed")
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -v -run TestLayout`
Expected: FAIL.

**Step 3: Write the implementation**

```go
// internal/tui/layout.go
package tui

// Layout manages pane geometry for the TUI.
type Layout struct {
    width  int
    height int
    zoomed bool
}

// NewLayout creates a layout with the given terminal dimensions.
func NewLayout(w, h int) *Layout {
    return &Layout{width: w, height: h}
}

// SetSize updates the terminal dimensions.
func (l *Layout) SetSize(w, h int) {
    l.width = w
    l.height = h
}

// SplitVertical returns left and right widths for a vertical split.
// percent is the left pane's share (0-100).
func (l *Layout) SplitVertical(percent int) (left, right int) {
    left = l.width * percent / 100
    right = l.width - left
    return
}

// ContentHeight returns the available height for content,
// subtracting the header height and status bar (1 line).
func (l *Layout) ContentHeight(headerHeight int) int {
    h := l.height - headerHeight - 1
    if h < 1 {
        return 1
    }
    return h
}

// SetZoomed toggles zoom state.
func (l *Layout) SetZoomed(z bool) { l.zoomed = z }

// IsZoomed returns whether the preview pane is zoomed.
func (l *Layout) IsZoomed() bool { return l.zoomed }

// Width returns the total terminal width.
func (l *Layout) Width() int { return l.width }

// Height returns the total terminal height.
func (l *Layout) Height() int { return l.height }
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -v -run TestLayout`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/tui/layout.go internal/tui/layout_test.go
git commit -m "feat: add split pane layout manager"
```

---

### Task 10: Preview Pane View

**Files:**
- Create: `internal/tui/views/preview.go`
- Modify: `internal/tui/app.go` — wire preview into split layout

The preview pane shows the live conversation of the selected agent in read-only mode, parsed from the session JSONL file (using the existing `TraceLine` parsing from `logs.go`).

**Step 1: Create preview.go**

```go
// internal/tui/views/preview.go
package views

import (
    "fmt"
    "strings"

    "github.com/charmbracelet/lipgloss"
    "github.com/zanetworker/aimux/internal/agent"
)

var (
    previewTitleStyle = lipgloss.NewStyle().
        Bold(true).
        Foreground(lipgloss.Color("#5F87FF"))

    previewMetaStyle = lipgloss.NewStyle().
        Foreground(lipgloss.Color("#9CA3AF"))

    previewBorderStyle = lipgloss.NewStyle().
        Border(lipgloss.NormalBorder(), false, false, false, true).
        BorderForeground(lipgloss.Color("#374151"))
)

// PreviewPane shows a read-only conversation preview for the selected agent.
type PreviewPane struct {
    agent    *agent.Agent
    lines    []TraceLine
    width    int
    height   int
}

// NewPreviewPane creates a new preview pane.
func NewPreviewPane() *PreviewPane {
    return &PreviewPane{}
}

// SetAgent updates the previewed agent and reloads its conversation.
func (p *PreviewPane) SetAgent(a *agent.Agent) {
    if a == nil {
        p.agent = nil
        p.lines = nil
        return
    }
    // Only reload if the agent changed
    if p.agent != nil && p.agent.PID == a.PID && p.agent.SessionFile == a.SessionFile {
        return
    }
    p.agent = a
    p.Reload()
}

// Reload re-reads the conversation from the session file.
func (p *PreviewPane) Reload() {
    if p.agent == nil || p.agent.SessionFile == "" {
        p.lines = nil
        return
    }
    // Reuse the LogsView JSONL parsing
    lv := NewLogsView(p.agent.PID, p.agent.SessionFile)
    p.lines = lv.lines
}

// SetSize sets the available width and height.
func (p *PreviewPane) SetSize(w, h int) {
    p.width = w
    p.height = h
}

// View renders the preview pane.
func (p *PreviewPane) View() string {
    if p.agent == nil {
        return previewBorderStyle.Width(p.width).Render(
            previewMetaStyle.Render("  Select an agent to preview"),
        )
    }

    var b strings.Builder

    // Agent header
    icon := p.agent.Status.Icon()
    statusStyle := lipgloss.NewStyle()
    switch p.agent.Status {
    case agent.StatusActive:
        statusStyle = statusStyle.Foreground(lipgloss.Color("#22C55E"))
    case agent.StatusWaitingPermission:
        statusStyle = statusStyle.Foreground(lipgloss.Color("#F59E0B"))
    default:
        statusStyle = statusStyle.Foreground(lipgloss.Color("#6B7280"))
    }

    title := fmt.Sprintf(" %s %s", statusStyle.Render(icon), previewTitleStyle.Render(p.agent.ShortProject()))
    meta := previewMetaStyle.Render(fmt.Sprintf("  %s · %s · %s",
        p.agent.ProviderName, p.agent.ShortModel(), p.agent.PermissionMode))

    b.WriteString(title + "\n")
    b.WriteString(meta + "\n")
    b.WriteString(previewMetaStyle.Render(strings.Repeat("─", p.width-2)) + "\n")

    if len(p.lines) == 0 {
        b.WriteString(previewMetaStyle.Render("  No conversation data"))
        return previewBorderStyle.Width(p.width).Render(b.String())
    }

    // Show the last N lines that fit
    availableLines := p.height - 4 // header + meta + separator + bottom
    if availableLines < 1 {
        availableLines = 1
    }
    start := len(p.lines) - availableLines
    if start < 0 {
        start = 0
    }

    for i := start; i < len(p.lines); i++ {
        line := p.lines[i]
        ts := ""
        if !line.Timestamp.IsZero() {
            ts = timestampStyle.Render(line.Timestamp.Format("15:04")) + " "
        }

        content := line.Content
        maxContent := p.width - 12
        if maxContent > 0 && len(content) > maxContent {
            content = content[:maxContent-3] + "..."
        }

        b.WriteString(fmt.Sprintf(" %s%s %s\n", ts, line.Label, content))
    }

    return previewBorderStyle.Width(p.width).Render(b.String())
}
```

**Step 2: Wire into app.go**

Update `App` struct to include `previewPane`, `headerView`, and `layout`. In the `View()` method, when not zoomed:
- Render header at top
- Split remaining space: agents table (left) + preview pane (right)
- Render status bar at bottom

When the selected agent changes (cursor moves), call `previewPane.SetAgent()`.

**Step 3: Verify build**

Run: `go build ./cmd/aimux`

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: add live conversation preview pane"
```

---

### Task 11: PTY Embedding — Terminal Emulator View

**Files:**
- Create: `internal/terminal/embed.go`
- Create: `internal/terminal/embed_test.go`
- Create: `internal/terminal/view.go`

This is the core of the in-TUI session feature. We embed a real PTY inside the Bubble Tea view.

**Step 1: Add dependencies**

Run:
```bash
go get github.com/creack/pty@latest
go get github.com/charmbracelet/x/vt@latest
```

**Step 2: Create embed.go — PTY manager**

```go
// internal/terminal/embed.go
package terminal

import (
    "io"
    "os"
    "os/exec"
    "sync"

    "github.com/creack/pty"
)

// Session manages an embedded PTY for a CLI agent.
type Session struct {
    cmd    *exec.Cmd
    ptmx   *os.File
    mu     sync.Mutex
    closed bool
}

// Start spawns a PTY running the given command.
func Start(cmd *exec.Cmd) (*Session, error) {
    ptmx, err := pty.Start(cmd)
    if err != nil {
        return nil, err
    }
    return &Session{cmd: cmd, ptmx: ptmx}, nil
}

// Read reads from the PTY output. Returns io.EOF when the process exits.
func (s *Session) Read(buf []byte) (int, error) {
    s.mu.Lock()
    if s.closed {
        s.mu.Unlock()
        return 0, io.EOF
    }
    ptmx := s.ptmx
    s.mu.Unlock()
    return ptmx.Read(buf)
}

// Write sends input to the PTY (keystrokes from the user).
func (s *Session) Write(data []byte) (int, error) {
    s.mu.Lock()
    if s.closed {
        s.mu.Unlock()
        return 0, io.EOF
    }
    ptmx := s.ptmx
    s.mu.Unlock()
    return ptmx.Write(data)
}

// Resize updates the PTY window size.
func (s *Session) Resize(cols, rows int) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed {
        return nil
    }
    return pty.Setsize(s.ptmx, &pty.Winsize{
        Cols: uint16(cols),
        Rows: uint16(rows),
    })
}

// Close terminates the session and cleans up.
func (s *Session) Close() error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed {
        return nil
    }
    s.closed = true
    s.cmd.Process.Signal(os.Interrupt)
    s.ptmx.Close()
    s.cmd.Wait()
    return nil
}

// Alive returns true if the PTY process is still running.
func (s *Session) Alive() bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    return !s.closed && s.cmd.ProcessState == nil
}
```

**Step 3: Create embed_test.go**

```go
// internal/terminal/embed_test.go
package terminal

import (
    "os/exec"
    "testing"
)

func TestStartAndClose(t *testing.T) {
    cmd := exec.Command("echo", "hello")
    sess, err := Start(cmd)
    if err != nil {
        t.Fatalf("Start() error: %v", err)
    }
    defer sess.Close()

    buf := make([]byte, 1024)
    n, _ := sess.Read(buf)
    if n == 0 {
        t.Error("expected to read output from echo")
    }
    got := string(buf[:n])
    if got != "hello\r\n" && got != "hello\n" {
        t.Errorf("Read() = %q, want %q", got, "hello\r\n")
    }
}

func TestResize(t *testing.T) {
    cmd := exec.Command("cat") // cat stays alive until stdin closes
    sess, err := Start(cmd)
    if err != nil {
        t.Fatalf("Start() error: %v", err)
    }
    defer sess.Close()

    if err := sess.Resize(120, 40); err != nil {
        t.Errorf("Resize() error: %v", err)
    }
}

func TestDoubleClose(t *testing.T) {
    cmd := exec.Command("echo", "hi")
    sess, err := Start(cmd)
    if err != nil {
        t.Fatalf("Start() error: %v", err)
    }

    if err := sess.Close(); err != nil {
        t.Errorf("first Close() error: %v", err)
    }
    if err := sess.Close(); err != nil {
        t.Errorf("second Close() should not error: %v", err)
    }
}
```

**Step 4: Run tests**

Run: `go test ./internal/terminal/ -v -timeout 10s`
Expected: PASS.

**Step 5: Create view.go — VT → Bubble Tea rendering**

This file reads PTY output through a VT emulator and converts the cell grid to a lipgloss-rendered string. The exact rendering depends on `charmbracelet/x/vt` API — a minimal version that captures output as text:

```go
// internal/terminal/view.go
package terminal

import (
    "strings"
    "sync"

    "github.com/charmbracelet/x/vt"
)

// TermView wraps a VT emulator to render PTY output as a string.
type TermView struct {
    term   *vt.Terminal
    mu     sync.Mutex
    width  int
    height int
}

// NewTermView creates a terminal view with the given dimensions.
func NewTermView(cols, rows int) *TermView {
    t := vt.NewTerminal(cols, rows)
    return &TermView{
        term:   t,
        width:  cols,
        height: rows,
    }
}

// Write feeds raw PTY output into the VT emulator.
func (tv *TermView) Write(data []byte) {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    tv.term.Write(data)
}

// Resize updates the terminal dimensions.
func (tv *TermView) Resize(cols, rows int) {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    tv.width = cols
    tv.height = rows
    tv.term.Resize(cols, rows)
}

// Render returns the current terminal screen as a string.
func (tv *TermView) Render() string {
    tv.mu.Lock()
    defer tv.mu.Unlock()
    var b strings.Builder
    b.WriteString(tv.term.String())
    return b.String()
}
```

**Step 6: Run all tests**

Run: `go test ./... -timeout 30s`
Expected: All pass.

**Step 7: Commit**

```bash
git add internal/terminal/
git commit -m "feat: PTY embedding with VT terminal emulator"
```

---

### Task 12: Session View — Zoomed Interactive Mode

**Files:**
- Create: `internal/tui/views/session.go`
- Modify: `internal/tui/app.go` — zoom state machine, key routing

**Step 1: Create session.go**

The session view wraps the PTY session and terminal view. When zoomed, all keystrokes (except Ctrl+]) are forwarded to the PTY.

```go
// internal/tui/views/session.go
package views

import (
    "fmt"
    "os/exec"
    "strings"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"
    "github.com/zanetworker/aimux/internal/agent"
    "github.com/zanetworker/aimux/internal/terminal"
)

// PTYOutputMsg carries raw PTY output for the Bubble Tea update loop.
type PTYOutputMsg struct {
    Data []byte
}

// PTYExitMsg signals the PTY process has exited.
type PTYExitMsg struct{}

var (
    sessionHeaderStyle = lipgloss.NewStyle().
        Background(lipgloss.Color("#1E3A5F")).
        Foreground(lipgloss.Color("#E5E7EB")).
        Bold(true)

    sessionHintStyle = lipgloss.NewStyle().
        Foreground(lipgloss.Color("#9CA3AF"))
)

// SessionView manages an interactive embedded terminal session.
type SessionView struct {
    agent    *agent.Agent
    session  *terminal.Session
    termView *terminal.TermView
    width    int
    height   int
    active   bool
}

// NewSessionView creates a session view (inactive until Open is called).
func NewSessionView() *SessionView {
    return &SessionView{}
}

// Open spawns a PTY for the given agent using the provided command.
func (sv *SessionView) Open(a *agent.Agent, cmd *exec.Cmd) (tea.Cmd, error) {
    // Close any existing session
    sv.Close()

    contentHeight := sv.height - 2 // header + status bar
    if contentHeight < 10 {
        contentHeight = 10
    }
    contentWidth := sv.width
    if contentWidth < 40 {
        contentWidth = 80
    }

    sess, err := terminal.Start(cmd)
    if err != nil {
        return nil, fmt.Errorf("starting session: %w", err)
    }

    sess.Resize(contentWidth, contentHeight)

    sv.agent = a
    sv.session = sess
    sv.termView = terminal.NewTermView(contentWidth, contentHeight)
    sv.active = true

    // Start reading PTY output in background
    return sv.readLoop(), nil
}

// readLoop returns a tea.Cmd that reads PTY output and sends it as messages.
func (sv *SessionView) readLoop() tea.Cmd {
    return func() tea.Msg {
        buf := make([]byte, 4096)
        n, err := sv.session.Read(buf)
        if err != nil {
            return PTYExitMsg{}
        }
        return PTYOutputMsg{Data: buf[:n]}
    }
}

// HandleOutput processes PTY output data.
func (sv *SessionView) HandleOutput(data []byte) tea.Cmd {
    if sv.termView != nil {
        sv.termView.Write(data)
    }
    if sv.session != nil && sv.session.Alive() {
        return sv.readLoop()
    }
    return nil
}

// SendKey forwards a keystroke to the PTY.
func (sv *SessionView) SendKey(key string) {
    if sv.session == nil || !sv.active {
        return
    }
    sv.session.Write([]byte(key))
}

// SetSize updates dimensions and resizes the PTY.
func (sv *SessionView) SetSize(w, h int) {
    sv.width = w
    sv.height = h
    contentHeight := h - 2
    if contentHeight < 1 {
        contentHeight = 1
    }
    if sv.session != nil {
        sv.session.Resize(w, contentHeight)
    }
    if sv.termView != nil {
        sv.termView.Resize(w, contentHeight)
    }
}

// Close terminates the PTY session.
func (sv *SessionView) Close() {
    if sv.session != nil {
        sv.session.Close()
    }
    sv.session = nil
    sv.termView = nil
    sv.active = false
    sv.agent = nil
}

// Active returns true if a session is open.
func (sv *SessionView) Active() bool { return sv.active }

// View renders the interactive terminal.
func (sv *SessionView) View() string {
    if !sv.active || sv.agent == nil {
        return ""
    }

    var b strings.Builder

    // Header bar
    title := fmt.Sprintf(" aimux  ▸ %s  %s · %s · %s ",
        sv.agent.ShortProject(),
        sv.agent.ProviderName,
        sv.agent.ShortModel(),
        sv.agent.PermissionMode,
    )
    hint := "Ctrl+] to zoom out"
    gap := sv.width - lipgloss.Width(title) - len(hint) - 1
    if gap < 0 {
        gap = 0
    }
    headerLine := sessionHeaderStyle.Width(sv.width).Render(
        title + strings.Repeat(" ", gap) + sessionHintStyle.Render(hint),
    )
    b.WriteString(headerLine + "\n")

    // Terminal content
    if sv.termView != nil {
        b.WriteString(sv.termView.Render())
    }

    // Status bar
    status := sessionHeaderStyle.Width(sv.width).Render(
        " INTERACTIVE  " + sessionHintStyle.Render("Type to send input") +
            strings.Repeat(" ", max(0, sv.width-50)) +
            sessionHintStyle.Render("Ctrl+] zoom out"),
    )
    b.WriteString("\n" + status)

    return b.String()
}
```

**Step 2: Wire zoom state into app.go**

Add to `App` struct:
```go
sessionView *views.SessionView
zoomed      bool
```

In `handleKey`:
- When `zoomed` is true, only intercept `ctrl+]` (zoom out). All other keys go to `sessionView.SendKey()`.
- `Enter` on agent list: if provider has `ResumeCommand`, spawn session and zoom in.

In `Update`:
- Handle `PTYOutputMsg`: call `sessionView.HandleOutput()`.
- Handle `PTYExitMsg`: set `zoomed = false`, clear session.

In `View`:
- If `zoomed`: render only `sessionView.View()`
- If not `zoomed`: render split layout (agents + preview)

**Step 3: Verify build**

Run: `go build ./cmd/aimux`

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: zoomed interactive session view with PTY"
```

---

### Task 13: Wire Everything Together — App State Machine

**Files:**
- Modify: `internal/tui/app.go` — complete rewrite of View/Update for split+zoom

This is the integration task. The app now has three layout states:
1. **Split view** (default): agents table left + preview right
2. **Zoomed session**: full-screen interactive PTY
3. **Sub-views**: costs, teams, help (full-screen, non-interactive)

**Step 1: Rewrite app.go**

Key changes:
- Add `layout *Layout`, `headerView *views.HeaderView`, `previewPane *views.PreviewPane`, `sessionView *views.SessionView`
- Remove old `renderHeader()` — use `headerView.View()`
- In `View()`: use `layout` to calculate pane widths
- When cursor moves in agent list, update `previewPane.SetAgent()`
- `Enter` key: spawn PTY via provider, zoom in
- `Ctrl+]`: zoom out
- Tick: re-read preview pane for live updates

**Step 2: Update key routing**

```go
func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    if a.zoomed {
        // Only Ctrl+] exits zoom mode
        if msg.String() == "ctrl+]" {
            a.zoomed = false
            return a, nil
        }
        // Everything else goes to the PTY
        a.sessionView.SendKey(msg.String())
        return a, nil
    }
    // ... existing key handling ...
}
```

**Step 3: Update View rendering**

```go
func (a App) View() string {
    if a.width == 0 {
        return "Loading..."
    }

    if a.zoomed && a.sessionView.Active() {
        return a.sessionView.View()
    }

    header := a.headerView.View()
    headerHeight := strings.Count(header, "\n") + 1
    contentHeight := a.layout.ContentHeight(headerHeight)

    var content string
    switch a.currentView {
    case viewAgents:
        leftW, rightW := a.layout.SplitVertical(35)
        a.agentsView.SetSize(leftW, contentHeight)
        a.previewPane.SetSize(rightW, contentHeight)
        content = lipgloss.JoinHorizontal(lipgloss.Top,
            a.agentsView.View(),
            a.previewPane.View(),
        )
    case viewCosts:
        content = a.costsView.View()
    // ... other views ...
    }

    statusBar := a.renderStatusBar()
    return header + "\n" + content + "\n" + statusBar
}
```

**Step 4: Verify build and run**

Run:
```bash
go build ./cmd/aimux && ./aimux
```

Expected: TUI launches with split pane layout. If Claude sessions are running, they appear in the left pane. Selecting one shows the conversation preview on the right.

**Step 5: Commit**

```bash
git add -A
git commit -m "feat: wire split pane layout with zoom state machine"
```

---

### Task 14: Update Costs View for Agent Model

**Files:**
- Modify: `internal/tui/views/costs.go`

**Step 1: Update to use agent.Agent instead of model.Instance**

Change `SetInstances` → `SetAgents`, update imports, and add the AGENT column to the costs table to show which provider the cost came from.

**Step 2: Verify build and tests**

Run: `go build ./cmd/aimux && go test ./... -timeout 30s`

**Step 3: Commit**

```bash
git add -A
git commit -m "refactor: update costs view for agent model"
```

---

### Task 15: Update Help View

**Files:**
- Modify: `internal/tui/views/help.go`

**Step 1: Update keybinding descriptions**

- Change "instances" references to "agents"
- Add `Ctrl+]` — Zoom out from session
- Update command descriptions
- Add note about provider support

**Step 2: Commit**

```bash
git add internal/tui/views/help.go
git commit -m "docs: update help view for aimux"
```

---

### Task 16: Update README

**Files:**
- Modify: `README.md`

**Step 1: Complete rewrite**

- Update name, description, install instructions
- New ASCII mockups showing split pane + zoom
- Document provider system (Claude real, Codex/Gemini stubs)
- Update key bindings table
- Update architecture section

**Step 2: Commit**

```bash
git add README.md
git commit -m "docs: update README for aimux rebrand"
```

---

### Task 17: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Update project guide**

- Change all "claudetopus" references to "aimux"
- Update project structure section
- Add provider and terminal packages
- Update build/test commands

**Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md for aimux"
```

---

### Task 18: Integration Test and Polish

**Step 1: Build and run**

Run:
```bash
go build ./cmd/aimux && go test ./... -timeout 30s
```

Expected: Clean build, all tests pass.

**Step 2: Run the TUI**

Run: `./aimux`

Verify:
- [ ] Header shows info boxes and ASCII logo
- [ ] Agent table shows NAME, AGENT, MODEL, MODE, AGE, COST
- [ ] Split pane: agent list on left, preview on right
- [ ] Selecting different agents updates the preview
- [ ] Enter zooms into interactive session (if provider supports resume)
- [ ] Ctrl+] zooms back out
- [ ] `:costs`, `:teams`, `:help` commands work
- [ ] `q` quits from agent list
- [ ] `Esc` goes back from sub-views

**Step 3: Fix any issues found during testing**

**Step 4: Final commit**

```bash
git add -A
git commit -m "fix: integration polish for aimux"
```

---

## Summary

| Task | Component | What it builds |
|------|-----------|---------------|
| 1 | Rename | claudetopus → aimux (module, binary, imports) |
| 2 | Data Model | Instance → Agent, model → agent package |
| 3 | Provider Interface | Provider, Segment, Role types |
| 4 | Claude Provider | Real Claude discovery + resume |
| 5 | Provider Stubs | Codex + Gemini empty implementations |
| 6 | Orchestrator | Refactored to iterate providers |
| 7 | Header | info boxes + ASCII logo |
| 8 | Agents Table | colored table, agent-centric columns |
| 9 | Layout | Split pane geometry manager |
| 10 | Preview | Live conversation preview pane |
| 11 | PTY Embedding | creack/pty + charmbracelet/x/vt |
| 12 | Session View | Zoomed interactive terminal |
| 13 | App Wiring | Split + zoom state machine |
| 14 | Costs | Updated for agent model |
| 15 | Help | Updated keybindings |
| 16 | README | Full rewrite |
| 17 | CLAUDE.md | Updated project guide |
| 18 | Integration | End-to-end verification |
