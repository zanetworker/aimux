# Adding a New Provider to aimux

## Overview

A **provider** is how aimux learns about a type of AI coding agent. Each provider implements the `provider.Provider` interface, which gives aimux the ability to:

- **Discover** running agent processes and recent sessions automatically
- **Display** agents in the dashboard with status, model, cost, and metadata
- **Resume** sessions by zooming into an embedded PTY or jumping to an external terminal
- **Show traces** by resolving session/conversation log files
- **Launch** new agent sessions from the `:new` launcher with model and mode selection
- **Track costs** via token-based pricing in the cost dashboard

Once you implement the interface and register your provider, all of these features work without touching any view or TUI code.

## Quick Start (5 minutes)

Copy `internal/provider/gemini.go`, rename it, and adjust. This gets you a compilable stub that shows up in the launcher.

**1. Create the file:**

```go
// internal/provider/aider.go
package provider

import (
	"os/exec"

	"github.com/zanetworker/aimux/internal/agent"
)

type Aider struct{}

func (a *Aider) Name() string                         { return "aider" }
func (a *Aider) Discover() ([]agent.Agent, error)     { return nil, nil }
func (a *Aider) ResumeCommand(ag agent.Agent) *exec.Cmd { return nil }
func (a *Aider) CanEmbed() bool                        { return false }
func (a *Aider) FindSessionFile(ag agent.Agent) string { return "" }
func (a *Aider) RecentDirs(max int) []RecentDir        { return nil }

func (a *Aider) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("aider")
	cmd := exec.Command(bin)
	cmd.Dir = dir
	return cmd
}

func (a *Aider) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default"},
		Modes:  []string{"default"},
	}
}
```

**2. Register in `internal/tui/app.go` -- add to the `allProviders` slice in `NewApp()`:**

```go
allProviders := []provider.Provider{
	&provider.Claude{},
	&provider.Codex{},
	&provider.Gemini{},
	&provider.Aider{},  // <-- add this
}
```

**3. Add to `internal/config/config.go` -- add to `Default()` providers map:**

```go
Providers: map[string]ProviderConfig{
	"claude": {Enabled: true},
	"codex":  {Enabled: true},
	"gemini": {Enabled: true},
	"aider":  {Enabled: true},  // <-- add this
},
```

**4. Add tests (see Testing Checklist below).**

**5. Build and run:**

```bash
go build -o aimux ./cmd/aimux
go test ./... -timeout 30s
```

Your provider now appears in the `:new` launcher. Flesh out methods as you learn how the agent stores its data.

## The Provider Interface

Defined in `internal/provider/provider.go`:

```go
type Provider interface {
	Name() string
	Discover() ([]agent.Agent, error)
	ResumeCommand(a agent.Agent) *exec.Cmd
	CanEmbed() bool
	FindSessionFile(a agent.Agent) string
	RecentDirs(max int) []RecentDir
	SpawnCommand(dir, model, mode string) *exec.Cmd
	SpawnArgs() SpawnArgs
}
```

### `Name() string`

Returns a unique lowercase identifier for this provider. Used as a key everywhere: config lookup, provider matching, display labels.

**When called:** On every discovery cycle (every 2 seconds), during provider registration, and when resolving which provider owns an agent.

**Rules:**
- Must be unique across all providers
- Must be lowercase, no spaces
- Must match the key you add to `config.go` `Default()`

```go
func (a *Aider) Name() string { return "aider" }
```

### `Discover() ([]agent.Agent, error)`

Scans for running agent processes and recent session files. Returns a slice of `agent.Agent` structs. This is the core discovery mechanism -- it runs every 2 seconds via the orchestrator.

**When called:** Every tick (2 seconds) by `discovery.Orchestrator.Discover()`, which calls all providers in parallel.

**What to do:**
1. Scan `ps aux` output for processes matching your agent's binary name
2. Resolve each process's working directory (via `lsof` or `/proc`)
3. Find and parse session/trace files for metadata (model, tokens, status)
4. Optionally discover recent idle sessions (no running process but recent trace files)
5. Set `ProviderName` on every returned agent

**Return nil, nil for a stub.** That is valid and means "no agents found."

**Reference:** See `claude.go` `Discover()` for the full pattern (process scan + session enrichment + idle session discovery + deduplication). See `codex.go` `Discover()` for a mid-complexity version.

```go
func (a *Aider) Discover() ([]agent.Agent, error) {
	// Minimal: scan ps for "aider" processes
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return nil, fmt.Errorf("ps aux: %w", err)
	}

	var agents []agent.Agent
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "aider") {
			continue
		}
		// Parse PID, build agent.Agent, etc.
		// ...
	}
	return agents, nil
}
```

**Key `agent.Agent` fields to populate:**

| Field | Required | Description |
|-------|----------|-------------|
| `PID` | Yes (for running) | Process ID. Use 0 for idle/session-only entries. |
| `ProviderName` | Yes | Must equal your `Name()` return value. |
| `Status` | Yes | `agent.StatusActive`, `StatusIdle`, or `StatusUnknown`. |
| `Source` | Yes | `agent.SourceCLI`, `SourceVSCode`, or `SourceSDK`. |
| `WorkingDir` | Recommended | Absolute path to the project directory. |
| `SessionID` | If available | Unique session identifier for resumption. |
| `SessionFile` | If available | Path to the conversation trace file. |
| `Model` | If available | Model name string (e.g. `"gpt-4o"`, `"claude-opus-4-6"`). |
| `Name` | Recommended | Display name -- usually `filepath.Base(WorkingDir)`. |
| `TokensIn` / `TokensOut` | If available | For cost calculation. |
| `EstCostUSD` | If available | Pre-calculated via `cost.Calculate()`. |
| `GroupCount` | Default 1 | Set to 1 for single agents. |
| `GroupPIDs` | Default `[]int{}` | List of PIDs if grouping multiple processes. |

### `ResumeCommand(a agent.Agent) *exec.Cmd`

Builds the `exec.Cmd` that resumes an existing session. The TUI runs this command inside a PTY when the user presses Enter on an agent.

**When called:** When the user selects an agent and presses Enter (zoom in) or J (jump out).

**Return nil** if the agent cannot be resumed (no session ID, no working directory). The TUI handles nil gracefully by showing a status hint.

```go
func (a *Aider) ResumeCommand(ag agent.Agent) *exec.Cmd {
	bin := findBinary("aider")
	if ag.WorkingDir == "" {
		return nil
	}
	cmd := exec.Command(bin)
	cmd.Dir = ag.WorkingDir
	return cmd
}
```

**Reference:** Claude uses `--resume <sessionID>` or `--continue`. Codex uses `resume --no-alt-screen <sessionID>`.

### `CanEmbed() bool`

Reports whether this agent's TUI can run inside aimux's embedded PTY. If true, pressing Enter opens a split view (trace on left, live session on right). If false, pressing Enter opens the trace-only view, and the user presses J to jump out to a tmux or iTerm split pane.

**When called:** When the user presses Enter on an agent, to decide the layout mode.

**Guidelines:**
- Return `true` if the agent uses a standard terminal (no alternate screen buffer fighting, no raw mode conflicts). Claude works.
- Return `false` if the agent has its own TUI framework that conflicts with Bubble Tea's alternate screen. Codex and Gemini return false.
- When in doubt, return `false`. It is the safer option.

```go
func (a *Aider) CanEmbed() bool { return true }
```

### `FindSessionFile(a agent.Agent) string`

Resolves the path to the session's trace/conversation file. Each provider knows its own storage layout (Claude uses `~/.claude/projects/`, Codex uses `~/.codex/sessions/`).

**When called:** When opening the trace viewer (l key or Enter on a non-embeddable provider), and during preview pane updates.

**Return `""`** if no session file exists. The TUI shows "No trace data yet" to the user.

```go
func (a *Aider) FindSessionFile(ag agent.Agent) string {
	if ag.WorkingDir == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Aider stores chat history in .aider.chat.history.md in the project dir
	candidate := filepath.Join(ag.WorkingDir, ".aider.chat.history.md")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
```

### `RecentDirs(max int) []RecentDir`

Returns recently-used project directories from this provider's session history, sorted most-recent first, capped at `max`. These populate the directory picker in the `:new` launcher.

**When called:** When the user opens the launcher (`:new` command). All providers' recent dirs are merged and deduplicated.

**Return nil** if you have no session history to scan. The launcher still works -- it just won't show directories from your provider.

```go
type RecentDir struct {
	Path     string
	LastUsed time.Time
}
```

```go
func (a *Aider) RecentDirs(max int) []RecentDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	// Scan ~/.aider/ for recent session dirs
	// ...
	return nil // stub
}
```

### `SpawnCommand(dir, model, mode string) *exec.Cmd`

Builds the `exec.Cmd` to launch a brand-new agent session. Called from the launcher overlay after the user picks a directory, model, and mode.

**When called:** When the user completes the launcher flow (`:new` -> pick dir -> pick provider -> pick model -> pick mode).

**Parameters:**
- `dir` -- absolute path to the project directory. Always set `cmd.Dir = dir`.
- `model` -- the model string from `SpawnArgs().Models`. May be `""` or `"default"` -- skip adding a `--model` flag in that case.
- `mode` -- the mode string from `SpawnArgs().Modes`. May be `""` or `"default"`.

```go
func (a *Aider) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("aider")
	var args []string

	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}

	switch mode {
	case "architect":
		args = append(args, "--architect")
	case "ask":
		args = append(args, "--ask")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}
```

### `SpawnArgs() SpawnArgs`

Returns the available models and modes for the launcher UI. These populate the model and mode selection dropdowns.

**When called:** When building the launcher overlay. The first entry in each slice is the default selection.

```go
type SpawnArgs struct {
	Models []string // e.g., ["default", "gpt-4o", "claude-sonnet"]
	Modes  []string // e.g., ["default", "architect", "ask"]
}
```

```go
func (a *Aider) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "gpt-4o", "claude-3.5-sonnet", "deepseek-r1"},
		Modes:  []string{"default", "architect", "ask"},
	}
}
```

## Registration

Two files need a one-line change each:

### 1. `internal/tui/app.go` -- `NewApp()`

Add your provider to the `allProviders` slice:

```go
allProviders := []provider.Provider{
	&provider.Claude{},
	&provider.Codex{},
	&provider.Gemini{},
	&provider.Aider{},  // <-- add here
}
```

The orchestrator and all views pick it up automatically from this single registration point.

### 2. `internal/config/config.go` -- `Default()`

Add your provider to the default config so it is enabled out of the box:

```go
Providers: map[string]ProviderConfig{
	"claude": {Enabled: true},
	"codex":  {Enabled: true},
	"gemini": {Enabled: true},
	"aider":  {Enabled: true},  // <-- add here
},
```

Users can disable it in `~/.aimux/config.yaml`:

```yaml
providers:
  aider:
    enabled: false
```

Note: providers not listed in the config map are enabled by default (`IsProviderEnabled` returns true for unknown names), so the config entry is not strictly required for functionality. But adding it makes the provider visible in the default config and documents its existence.

## Cost Tracking

To show cost estimates for your provider's agents, add model pricing to `internal/cost/tracker.go`.

### Add to the `pricing` map:

```go
var pricing = map[string]ModelPricing{
	// ... existing entries ...

	// Aider-supported models (example)
	"gpt-4o": {
		Input:  2.50,
		Output: 10.00,
	},
	"deepseek-r1": {
		Input:  0.55,
		Output: 2.19,
	},
}
```

### Add short aliases (optional):

```go
var aliases = map[string]string{
	// ... existing entries ...
	"4o": "gpt-4o",
}
```

**How it works:** During `Discover()`, when you set `agent.TokensIn` and `agent.TokensOut`, call `cost.Calculate()` to populate `agent.EstCostUSD`:

```go
import "github.com/zanetworker/aimux/internal/cost"

a.EstCostUSD = cost.Calculate(
	a.Model,        // e.g. "gpt-4o"
	info.tokensIn,
	info.tokensOut,
	info.cacheRead,  // 0 if not applicable
	info.cacheWrite, // 0 if not applicable
)
```

The pricing map uses per-million-token rates in USD. `Calculate()` normalizes model names (strips version suffixes, resolves aliases) before lookup. Unknown models return `$0.00`.

## Testing Checklist

Every provider needs these tests. See `internal/provider/stubs_test.go` and `internal/provider/claude_test.go` for the patterns.

### Required Tests

**1. Compile-time interface check** (in your test file or stubs_test.go):

```go
var _ Provider = (*Aider)(nil)
```

This fails at compile time if `Aider` is missing any interface methods. No runtime cost.

**2. Name returns the correct string:**

```go
func TestAiderName(t *testing.T) {
	a := &Aider{}
	if got := a.Name(); got != "aider" {
		t.Errorf("Aider.Name() = %q, want %q", got, "aider")
	}
}
```

**3. CanEmbed returns the expected value:**

```go
func TestAiderCanEmbed(t *testing.T) {
	a := &Aider{}
	if !a.CanEmbed() {
		t.Error("Aider.CanEmbed() = false, want true")
	}
}
```

**4. SpawnArgs returns valid, non-empty slices:**

```go
func TestAiderSpawnArgs(t *testing.T) {
	a := &Aider{}
	sa := a.SpawnArgs()

	if len(sa.Models) == 0 {
		t.Fatal("SpawnArgs.Models is empty")
	}
	if sa.Models[0] != "default" {
		t.Errorf("SpawnArgs.Models[0] = %q, want %q", sa.Models[0], "default")
	}
	if len(sa.Modes) == 0 {
		t.Fatal("SpawnArgs.Modes is empty")
	}
	if sa.Modes[0] != "default" {
		t.Errorf("SpawnArgs.Modes[0] = %q, want %q", sa.Modes[0], "default")
	}
}
```

**5. SpawnCommand with various model/mode combinations:**

```go
func TestAiderSpawnCommand_Default(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
}

func TestAiderSpawnCommand_WithModel(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "gpt-4o", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gpt-4o")
}

func TestAiderSpawnCommand_DefaultModelSkipped(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "default", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgAbsent(t, cmd.Args, "--model")
}

func TestAiderSpawnCommand_ArchitectMode(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "", "architect")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--architect")
}
```

**6. FindSessionFile with empty agent does not panic:**

```go
func TestAiderFindSessionFile_Empty(t *testing.T) {
	a := &Aider{}
	if got := a.FindSessionFile(agent.Agent{}); got != "" {
		t.Errorf("FindSessionFile(empty) = %q, want empty", got)
	}
}
```

**7. RecentDirs does not panic:**

```go
func TestAiderRecentDirs(t *testing.T) {
	a := &Aider{}
	_ = a.RecentDirs(5) // must not panic
}
```

**8. Discover does not error on a clean system:**

```go
func TestAiderDiscover(t *testing.T) {
	a := &Aider{}
	_, err := a.Discover()
	if err != nil {
		t.Errorf("Aider.Discover() error = %v, want nil", err)
	}
}
```

### Test helpers

The test helpers `assertArgPresent`, `assertArgAbsent`, and `assertArgsContain` are defined in `internal/provider/claude_test.go` and available to all test files in the package.

### Run tests:

```bash
go test ./internal/provider/ -v -timeout 30s
go test ./... -timeout 30s
```

## Complete Example: Adding an "aider" Provider

This walkthrough adds a hypothetical [aider](https://aider.chat/) provider with process discovery, session file resolution, and cost tracking.

### Step 1: Create `internal/provider/aider.go`

```go
package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/cost"
)

// Aider is a Provider implementation for the aider AI pair programming tool.
type Aider struct{}

func (a *Aider) Name() string { return "aider" }

// Discover finds running aider processes.
func (a *Aider) Discover() ([]agent.Agent, error) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return nil, fmt.Errorf("ps aux: %w", err)
	}

	var agents []agent.Agent
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !a.isAiderProcess(line) {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		ag := agent.Agent{
			PID:          pid,
			ProviderName: "aider",
			Status:       agent.StatusUnknown,
			Source:        agent.SourceCLI,
			LastActivity:  time.Now(),
			GroupCount:    1,
			GroupPIDs:     []int{pid},
		}

		// Resolve working directory
		if cwd, err := exec.Command("lsof", "-a", "-p",
			strconv.Itoa(pid), "-d", "cwd", "-Fn").Output(); err == nil {
			for _, l := range strings.Split(string(cwd), "\n") {
				if strings.HasPrefix(l, "n/") {
					ag.WorkingDir = l[1:]
					break
				}
			}
		}

		if ag.WorkingDir != "" {
			ag.Name = filepath.Base(ag.WorkingDir)
		} else {
			ag.Name = fmt.Sprintf("aider-%d", pid)
		}

		// Extract model from command line
		cmd := strings.Join(fields[10:], " ")
		if m := extractFlag(cmd, "--model"); m != "" {
			ag.Model = m
		}

		agents = append(agents, ag)
	}

	return agents, nil
}

func (a *Aider) isAiderProcess(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return false
	}
	binary := fields[10]
	cmd := strings.Join(fields[10:], " ")

	if strings.Contains(cmd, "grep") {
		return false
	}
	if strings.Contains(cmd, "aimux") {
		return false
	}

	return strings.HasSuffix(binary, "/aider") || binary == "aider" ||
		(strings.Contains(binary, "python") && strings.Contains(cmd, "aider"))
}

func (a *Aider) ResumeCommand(ag agent.Agent) *exec.Cmd {
	bin := findBinary("aider")
	if ag.WorkingDir == "" {
		return nil
	}
	// aider auto-resumes from .aider.chat.history.md in the project dir
	cmd := exec.Command(bin)
	cmd.Dir = ag.WorkingDir
	return cmd
}

// CanEmbed returns true because aider uses a standard terminal interface.
func (a *Aider) CanEmbed() bool { return true }

// FindSessionFile looks for aider's chat history in the project directory.
func (a *Aider) FindSessionFile(ag agent.Agent) string {
	if ag.WorkingDir == "" {
		return ""
	}
	candidate := filepath.Join(ag.WorkingDir, ".aider.chat.history.md")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// RecentDirs scans for directories containing .aider.chat.history.md files.
func (a *Aider) RecentDirs(max int) []RecentDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// Check common project locations
	searchDirs := []string{
		filepath.Join(home, "projects"),
		filepath.Join(home, "src"),
		filepath.Join(home, "go", "src"),
	}

	seen := make(map[string]bool)
	var dirs []RecentDir

	for _, searchDir := range searchDirs {
		_ = filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return filepath.SkipDir
			}
			if info.IsDir() {
				return nil
			}
			if info.Name() != ".aider.chat.history.md" {
				return nil
			}
			dir := filepath.Dir(path)
			if seen[dir] {
				return nil
			}
			seen[dir] = true
			dirs = append(dirs, RecentDir{
				Path:     dir,
				LastUsed: info.ModTime(),
			})
			return nil
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].LastUsed.After(dirs[j].LastUsed)
	})

	if max > 0 && len(dirs) > max {
		dirs = dirs[:max]
	}
	return dirs
}

// SpawnCommand builds the exec.Cmd to launch a new aider session.
func (a *Aider) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("aider")
	var args []string

	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}

	switch mode {
	case "architect":
		args = append(args, "--architect")
	case "ask":
		args = append(args, "--ask")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns available models and modes for the launcher.
func (a *Aider) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default", "gpt-4o", "claude-3.5-sonnet", "deepseek-r1"},
		Modes:  []string{"default", "architect", "ask"},
	}
}

// extractFlag extracts the value following a CLI flag from a command string.
func extractFlag(args, flag string) string {
	fields := strings.Fields(args)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
```

### Step 2: Register in `internal/tui/app.go`

In `NewApp()`, add to the `allProviders` slice:

```go
allProviders := []provider.Provider{
	&provider.Claude{},
	&provider.Codex{},
	&provider.Gemini{},
	&provider.Aider{},
}
```

### Step 3: Register in `internal/config/config.go`

In `Default()`, add to the providers map:

```go
"aider": {Enabled: true},
```

### Step 4: Add cost tracking in `internal/cost/tracker.go`

Add any models aider uses that are not already in the pricing map:

```go
// In the pricing map:
"gpt-4o": {
	Input:  2.50,
	Output: 10.00,
},
"deepseek-r1": {
	Input:  0.55,
	Output: 2.19,
},

// In the aliases map:
"4o": "gpt-4o",
```

### Step 5: Add tests in `internal/provider/aider_test.go`

```go
package provider

import (
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

// Compile-time interface check.
var _ Provider = (*Aider)(nil)

func TestAiderName(t *testing.T) {
	a := &Aider{}
	if got := a.Name(); got != "aider" {
		t.Errorf("Aider.Name() = %q, want %q", got, "aider")
	}
}

func TestAiderCanEmbed(t *testing.T) {
	a := &Aider{}
	if !a.CanEmbed() {
		t.Error("Aider.CanEmbed() = false, want true")
	}
}

func TestAiderDiscover(t *testing.T) {
	a := &Aider{}
	_, err := a.Discover()
	if err != nil {
		t.Errorf("Aider.Discover() error = %v, want nil", err)
	}
}

func TestAiderResumeCommand_NoWorkingDir(t *testing.T) {
	a := &Aider{}
	cmd := a.ResumeCommand(agent.Agent{})
	if cmd != nil {
		t.Errorf("ResumeCommand(empty) = %v, want nil", cmd)
	}
}

func TestAiderResumeCommand_WithWorkingDir(t *testing.T) {
	a := &Aider{}
	cmd := a.ResumeCommand(agent.Agent{WorkingDir: "/tmp/project"})
	if cmd == nil {
		t.Skip("aider binary not found")
	}
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestAiderFindSessionFile_Empty(t *testing.T) {
	a := &Aider{}
	if got := a.FindSessionFile(agent.Agent{}); got != "" {
		t.Errorf("FindSessionFile(empty) = %q, want empty", got)
	}
}

func TestAiderRecentDirs(t *testing.T) {
	a := &Aider{}
	_ = a.RecentDirs(5) // must not panic
}

func TestAiderSpawnCommand_Default(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	if base := filepath.Base(cmd.Args[0]); base != "aider" {
		t.Errorf("binary = %q, want %q", base, "aider")
	}
}

func TestAiderSpawnCommand_DefaultModelSkipped(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "default", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgAbsent(t, cmd.Args, "--model")
}

func TestAiderSpawnCommand_WithModel(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "gpt-4o", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gpt-4o")
}

func TestAiderSpawnCommand_ArchitectMode(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "", "architect")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--architect")
}

func TestAiderSpawnCommand_AskMode(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "", "ask")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--ask")
}

func TestAiderSpawnCommand_ModelAndMode(t *testing.T) {
	a := &Aider{}
	cmd := a.SpawnCommand("/tmp/myproject", "gpt-4o", "architect")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gpt-4o")
	assertArgPresent(t, cmd.Args, "--architect")
}

func TestAiderSpawnArgs(t *testing.T) {
	a := &Aider{}
	sa := a.SpawnArgs()

	if len(sa.Models) == 0 {
		t.Fatal("SpawnArgs.Models is empty")
	}
	if sa.Models[0] != "default" {
		t.Errorf("SpawnArgs.Models[0] = %q, want %q", sa.Models[0], "default")
	}
	if len(sa.Modes) == 0 {
		t.Fatal("SpawnArgs.Modes is empty")
	}
	if sa.Modes[0] != "default" {
		t.Errorf("SpawnArgs.Modes[0] = %q, want %q", sa.Modes[0], "default")
	}
}
```

### Step 6: Build and verify

```bash
go test ./internal/provider/ -v -timeout 30s
go test ./... -timeout 30s
go build -o aimux ./cmd/aimux
./aimux  # verify aider appears in :new launcher
```

That is everything. The agent list, preview pane, trace viewer, cost dashboard, and launcher all pick up your provider automatically from the interface contract and registration.
