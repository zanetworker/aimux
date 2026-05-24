# Regression Test Suite Spec

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete regression test coverage so that upstream format changes, pricing updates, or refactors are caught before they ship. Every feature area has a fixture-based integration test using real data, no mocks.

**Architecture:** All tests go through `controller/` functions (the shared API that all three frontends use). Provider-specific tests use real parser implementations with fixture files in `testdata/`.

---

## Current State

| Feature | Unit tests | Integration test | Fixture file | Gap |
|---------|-----------|-----------------|-------------|-----|
| Claude trace parsing | Yes | Yes | `testdata/sample_session.jsonl` | OK |
| Codex trace parsing | Yes | **No** | **Missing** | Need fixture + integration test |
| Gemini trace parsing | Yes | **No** | **Missing** | Need fixture + integration test |
| Diff extraction | Yes | **No** | **No fixture with tool calls** | Need fixture with Edit/Write |
| Cost calculation | Yes | **No** | -- | Need end-to-end cost verification |
| Session history | Yes | **No** | -- | Need discover + metadata roundtrip |
| OTEL export | Yes (mock) | **No** | -- | Need roundtrip with mock receiver |
| Container runtime | Unit only | **No** | -- | Need podman (integration tag) |
| Config 4-axis | Yes | Yes | -- | OK |
| Badges | Yes | **No** | -- | Need badge eval from fixture project |
| Web API smoke | Yes (34 endpoints) | -- | -- | OK (10 blind spots now 0) |
| CLI E2E | Yes (11 commands) | -- | -- | OK |

---

## Task 1: Provider Fixture Files [P]

Create realistic fixture files for each provider format.

**Files:**
- Create: `testdata/codex_session.jsonl`
- Create: `testdata/gemini_session.json`
- Enhance: `testdata/sample_session.jsonl` (add tool call entries with Edit/Write)

### Codex fixture (`testdata/codex_session.jsonl`)

Codex uses a different JSONL format than Claude. Read `internal/provider/codex.go` `ParseTrace` to understand the exact schema. Create a 5-turn fixture with:
- 2 user prompts
- 2 assistant responses with tool calls (file edit, shell command)
- 1 assistant response without tool calls
- Realistic token counts

### Gemini fixture (`testdata/gemini_session.json`)

Gemini uses per-session JSON files (not JSONL). Read `internal/provider/gemini.go` `ParseTrace` to understand the schema. Create a fixture with:
- 3 user prompts
- No assistant responses (Gemini traces are user-only in the current parser)

### Claude fixture enhancement

Add entries to `testdata/sample_session.jsonl` with:
- A tool_use block for Edit (with old_string/new_string)
- A tool_use block for Write (with file_path/content)
- A tool_result block for each

---

## Task 2: Provider Integration Tests [P]

Test each provider's ParseTrace with its fixture file.

**Files:**
- Create: `internal/provider/integration_test.go`

```go
func TestIntegration_ClaudeParseTrace(t *testing.T) {
    p := &Claude{}
    turns, err := p.ParseTrace("../../testdata/sample_session.jsonl")
    // Verify: turn count, user text content, assistant text, tool calls, token counts
}

func TestIntegration_CodexParseTrace(t *testing.T) {
    p := &Codex{}
    turns, err := p.ParseTrace("../../testdata/codex_session.jsonl")
    // Verify: turn count, user text, tool calls
}

func TestIntegration_GeminiParseTrace(t *testing.T) {
    p := &Gemini{}
    turns, err := p.ParseTrace("../../testdata/gemini_session.json")
    // Verify: turn count, user text only (no assistant responses)
}
```

---

## Task 3: Diff Extraction Integration Test

Parse a fixture with tool calls, extract diffs, verify file names and line counts.

**Files:**
- Create: `internal/sessiondiff/integration_test.go`

```go
func TestIntegration_ExtractDiffsFromFixture(t *testing.T) {
    p := &provider.Claude{}
    turns, _ := p.ParseTrace("../../testdata/sample_session.jsonl")
    diffs := Extract(turns)
    // Verify: at least 1 diff, file paths present, added/removed counts > 0
}
```

---

## Task 4: Cost Calculation Integration Test

Parse a fixture, calculate cost, verify USD amount matches expected.

**Files:**
- Add to: `internal/controller/integration_test.go`

```go
func TestIntegration_CostFromFixture(t *testing.T) {
    p := &provider.Claude{}
    turns, _ := p.ParseTrace("../../testdata/sample_session.jsonl")
    totalCost := 0.0
    for _, turn := range turns {
        totalCost += cost.Calculate("claude-opus-4-6", turn.TokensIn, turn.TokensOut)
    }
    if totalCost <= 0 {
        t.Error("expected positive cost from fixture with token data")
    }
    // Log the cost so changes in pricing are visible in test output
    t.Logf("Total cost: $%.4f", totalCost)
}
```

---

## Task 5: Session History Integration Test

Create temp session files, discover them, verify metadata.

**Files:**
- Add to: `internal/controller/integration_test.go`

```go
func TestIntegration_HistoryDiscover(t *testing.T) {
    // Create temp dir mimicking ~/.claude/projects/test/sessions/
    // Copy fixture JSONL into it
    // Call history.Discover() scoped to the temp dir
    // Verify: session found, ID extracted, turn count > 0
    // Clean up
}
```

---

## Task 6: Badge Evaluation Integration Test

Create a temp project dir with real files, evaluate badges.

**Files:**
- Add to: `internal/controller/integration_test.go`

```go
func TestIntegration_BadgeEvaluation(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test-app","version":"1.0"}`), 0o600)
    os.WriteFile(filepath.Join(dir, ".python-version"), []byte("3.11\n"), 0o600)

    rules := []badge.Rule{
        {Path: "package.json", JSONPath: "name", Label: "pkg"},
        {Path: ".python-version", Label: "py"},
    }
    badges := badge.Evaluate(dir, rules)
    // Verify: 2 badges, correct values
}
```

---

## Task 7: Web API Integration Test (real pipeline)

Replace stub parser in smoke test with real Claude parser and fixture.

**Files:**
- Modify: `internal/frontend/web/smoke_test.go`

Change `stubParser` to use real `Claude.ParseTrace` with the fixture file:

```go
s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
    return &provider.Claude{}
})
```

Then add a test that hits `/api/trace?file=<fixture>` and verifies real turns come back:

```go
func TestAPISmoke_RealTraceEndpoint(t *testing.T) {
    // Hit /api/trace?file=../../testdata/sample_session.jsonl
    // Verify: 200, JSON with turns array, turn count matches fixture
}
```

---

## Task 8: Container Runtime Integration Test (tagged)

Requires podman. Behind `//go:build integration` tag.

**Files:**
- Create: `internal/runtime/container_integration_test.go`

```go
//go:build integration

func TestIntegration_PodmanRoundtrip(t *testing.T) {
    // Skip if podman not in PATH
    backend := NewPodmanBackend("podman")
    name := "aimux-test-" + randomSuffix()
    err := backend.Create(name, BackendCreateOpts{Image: "alpine:3.19"})
    if err != nil { t.Fatal(err) }
    defer backend.Delete(name)

    state, _ := backend.Status(name)
    if state != StateRunning { t.Error("container should be running") }

    backend.Stop(name)
    state, _ = backend.Status(name)
    if state != StateStopped { t.Error("container should be stopped") }
}
```

---

## Execution Order

Tasks 1-2 first (fixtures + provider tests). Then 3-6 in parallel (all independent). Task 7 after 1-2 (needs fixtures). Task 8 last (needs podman, optional).

## Verification

```bash
# Unit + integration (no external deps)
go test ./... -timeout 60s

# E2E CLI
go test -tags e2e ./internal/e2e/ -timeout 60s

# Container integration (needs podman)
go test -tags integration ./internal/runtime/ -timeout 60s

# Full smoke
go test ./internal/frontend/web/ -run Smoke -timeout 60s -v
```

## What This Catches

| Regression | Which test catches it |
|---|---|
| Claude changes JSONL format | Task 2: ClaudeParseTrace fixture test |
| Codex changes JSONL format | Task 2: CodexParseTrace fixture test |
| Gemini changes JSON format | Task 2: GeminiParseTrace fixture test |
| Edit/Write tool call extraction breaks | Task 3: diff extraction from fixture |
| Pricing changes or token extraction breaks | Task 4: cost from fixture |
| Session file scanning breaks | Task 5: history discover |
| Badge JSON path extraction breaks | Task 6: badge evaluation |
| Web API wiring breaks | Task 7: real trace through API |
| Container lifecycle breaks | Task 8: podman roundtrip |
| Any controller function breaks | Existing: controller integration_test.go |
| Any API endpoint returns 500 | Existing: smoke test (34 endpoints) |
| Any CLI command fails | Existing: E2E tests (11 commands) |

---

## Appendix: Launcher Consolidation + Six Config Axes (TODO)

### Problem

The `:new` flow has TWO overlays that overlap in responsibilities:
1. **New Picker** (`views/newpicker.go`) — "Where: Local / Hybrid / Remote" + Provider
2. **Launcher** (`views/launcher.go`) — Dir + Model/Mode/Runtime/OTEL

Labels don't match the architecture. Shell, session manager, execution mode,
and permissions aren't surfaced. Users can't opt out of tmux.

### Six Config Axes

The system has six orthogonal axes. Each is independently configurable:

```
Axis             Config key          Values                          Default
Provider         (per-launch)        claude | codex | gemini         claude
Runtime          runtime             local | container | k8s        local
Execution        execution           local | hybrid | remote        local
Shell            shell               /bin/zsh | /bin/bash | ...     $SHELL
Session Manager  session_manager     tmux | direct                  tmux
Permissions      (per-launch)        default | bypass | plan | ...  default
```

**Runtime vs Execution (the key distinction):**

```
              Agent process    Tool calls (edit, shell, test)
local:        laptop           laptop
container:    container        container
k8s:          K8s pod          K8s pod
hybrid:       laptop           remote (K8s pod / SSH host)
```

- **Runtime** = WHERE the agent CLI binary runs
- **Execution** = WHERE the agent's tool calls execute

Hybrid means: agent runs locally, but tool calls (Edit, Bash, Write) are
forwarded to a remote machine. The session is on your laptop; the work
happens on a K8s pod or SSH host.

**Session Manager:**
- `tmux` = session persists after disconnect (default, most users)
- `direct` = agent runs in foreground, dies when terminal closes

**Permissions** = what the agent is allowed to do (currently labeled "Mode"
in the launcher, which is confusing since claude has --mode flags):
- `default` = ask for permission on writes
- `bypass` = --dangerously-skip-permissions
- `plan` = plan mode only
- `acceptEdits` = accept file edits but ask for other tools

### Target Config

```yaml
# ~/.aimux/config.yaml

# --- Six axes ---
runtime: local              # WHERE agent runs: local | container | k8s
execution: local            # WHERE tools execute: local | hybrid | remote
shell: /bin/zsh             # WHICH shell
session_manager: tmux       # HOW sessions persist: tmux | direct
# provider: per-launch      # WHAT agent
# permissions: per-launch   # WHAT agent can do

# --- Runtime profiles ---
runtimes:
  dev:
    type: container
    engine: podman
    image: fedora:41
  cloud:
    type: k8s
    namespace: agents
    image: my-agent:latest
```

### Target Launcher UX (single overlay, no Picker)

```
:new
  Provider:     [claude]  codex  gemini
  Directory:    ~/projects/aimux
  Model:        [default]  opus  sonnet  haiku
  Permissions:  [default]  bypass  plan  acceptEdits
  Runtime:      [local]  container  k8s
  Execution:    [local]  hybrid  remote
  Shell:        [/bin/zsh]  /bin/bash
  Session:      [tmux]  direct
  OTEL:         ON
```

### Implementation Plan

1. Add `execution` field to Config (default: "local")
2. Rename "Mode" to "Permissions" in launcher labels
3. Add "direct" as session_manager option (runs agent in foreground)
4. Add "k8s" as runtime option (delegates to K8sBackend)
5. Add "hybrid" execution option (local agent + remote tool forwarding)
6. Consolidate New Picker into Launcher (eliminate newpicker.go)
7. Update Web API launch endpoint to accept all six axes
8. Update CLI spawn command to accept --execution, --session flags

### What "Hybrid" Needs (architecture)

Hybrid execution requires an MCP bridge: the local agent calls tools
that forward to a remote host. This is how Claude Code works with the
K8s MCP server today. The agent runs locally but tool calls go to a
pod. This needs:

- A remote tool executor (SSH exec or kubectl exec)
- An MCP server running in the pod that receives tool calls
- Environment variable wiring to point the agent at the remote MCP

This is the most complex axis and should be the last to implement.
