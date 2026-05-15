# Agentic CLI Tier 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate aimux from hand-rolled arg parsing to cobra and add Tier 1 agentic CLI features: structured JSON output on all commands, exit code taxonomy, error messages with valid values, TTY detection, --dry-run, --fields, and two new commands (agents, spawn).

**Architecture:** Command-per-file layout under `cmd/aimux/cmd/`. Shared `output.go` provides exit codes, JSON/table output writer, and structured error formatting. Each command file uses cobra and calls core packages (history, discovery, spawn, provider) for business logic.

**Tech Stack:** Go, cobra (github.com/spf13/cobra), existing core packages (history, discovery, spawn, provider, agent, config, cost)

---

## File Structure

```
cmd/aimux/
  main.go              # slim: calls cmd.Execute() (REWRITE)
  cmd/
    root.go            # root cobra command, persistent --json flag, TTY detection
    output.go          # exit codes, OutputWriter, structured errors
    output_test.go     # tests for output infrastructure
    sessions.go        # aimux sessions (list/search/export)
    sessions_test.go   # tests for sessions command
    resume.go          # aimux resume <id>
    resume_test.go     # tests for resume command
    agents.go          # aimux agents (list running agents)
    agents_test.go     # tests for agents command
    spawn.go           # aimux spawn <provider>
    spawn_test.go      # tests for spawn command
    web.go             # aimux web [--port]
    version.go         # aimux version
    version_test.go    # tests for version command
```

No changes to core packages. All new code lives in `cmd/aimux/cmd/`.

---

### Task 1: Add cobra dependency and create output infrastructure

**Files:**
- Create: `cmd/aimux/cmd/output.go`
- Create: `cmd/aimux/cmd/output_test.go`

- [ ] **Step 1: Add cobra dependency**

Run: `go get github.com/spf13/cobra`

Expected: cobra added to go.mod and go.sum

- [ ] **Step 2: Write failing tests for exit codes and OutputWriter**

```go
// cmd/aimux/cmd/output_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestExitCodeConstants(t *testing.T) {
	if ExitSuccess != 0 {
		t.Errorf("ExitSuccess = %d, want 0", ExitSuccess)
	}
	if ExitError != 1 {
		t.Errorf("ExitError = %d, want 1", ExitError)
	}
	if ExitUsage != 2 {
		t.Errorf("ExitUsage = %d, want 2", ExitUsage)
	}
	if ExitNotFound != 3 {
		t.Errorf("ExitNotFound = %d, want 3", ExitNotFound)
	}
	if ExitConfig != 4 {
		t.Errorf("ExitConfig = %d, want 4", ExitConfig)
	}
}

func TestOutputWriter_JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w := &OutputWriter{JSON: true, Stdout: &stdout, Stderr: &stderr}

	data := map[string]string{"key": "value"}
	code := w.WriteResult(data)
	if code != ExitSuccess {
		t.Errorf("WriteResult returned %d, want %d", code, ExitSuccess)
	}

	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("got key=%q, want %q", got["key"], "value")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderr.String())
	}
}

func TestOutputWriter_WriteError_JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w := &OutputWriter{JSON: true, Stdout: &stdout, Stderr: &stderr}

	code := w.WriteError("invalid provider", ExitUsage, map[string]any{
		"valid_values": []string{"claude", "codex", "gemini"},
	})
	if code != ExitUsage {
		t.Errorf("WriteError returned %d, want %d", code, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty for errors, got %q", stdout.String())
	}

	var errObj map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &errObj); err != nil {
		t.Fatalf("stderr is not valid JSON: %v", err)
	}
	if errObj["error"] != "invalid provider" {
		t.Errorf("error=%q, want %q", errObj["error"], "invalid provider")
	}
	if errObj["code"].(float64) != float64(ExitUsage) {
		t.Errorf("code=%v, want %d", errObj["code"], ExitUsage)
	}
}

func TestOutputWriter_WriteError_Text(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w := &OutputWriter{JSON: false, Stdout: &stdout, Stderr: &stderr}

	code := w.WriteError("invalid provider \"gpt\"", ExitUsage, map[string]any{
		"valid_values": []string{"claude", "codex", "gemini"},
	})
	if code != ExitUsage {
		t.Errorf("WriteError returned %d, want %d", code, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty for errors")
	}
	got := stderr.String()
	if !bytes.Contains(stderr.Bytes(), []byte("invalid provider")) {
		t.Errorf("stderr missing error message, got %q", got)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("claude, codex, gemini")) {
		t.Errorf("stderr missing valid values, got %q", got)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestExitCode -v`
Expected: FAIL — `ExitSuccess` not defined

- [ ] **Step 4: Implement output.go**

```go
// cmd/aimux/cmd/output.go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ExitSuccess  = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitNotFound = 3
	ExitConfig   = 4
)

type OutputWriter struct {
	JSON   bool
	Stdout io.Writer
	Stderr io.Writer
}

func NewOutputWriter(jsonMode bool) *OutputWriter {
	return &OutputWriter{
		JSON:   jsonMode,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

func (w *OutputWriter) WriteResult(data any) int {
	if w.JSON {
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return w.WriteError(fmt.Sprintf("failed to marshal result: %v", err), ExitError, nil)
		}
		fmt.Fprintln(w.Stdout, string(b))
	} else {
		fmt.Fprintln(w.Stdout, data)
	}
	return ExitSuccess
}

func (w *OutputWriter) WriteError(msg string, code int, extra map[string]any) int {
	if w.JSON {
		errObj := map[string]any{"error": msg, "code": code}
		for k, v := range extra {
			errObj[k] = v
		}
		b, _ := json.Marshal(errObj)
		fmt.Fprintln(w.Stderr, string(b))
	} else {
		fmt.Fprintf(w.Stderr, "Error: %s", msg)
		if vals, ok := extra["valid_values"]; ok {
			if sv, ok := vals.([]string); ok {
				fmt.Fprintf(w.Stderr, " (must be one of: %s)", strings.Join(sv, ", "))
			}
		}
		fmt.Fprintln(w.Stderr)
	}
	return code
}

func IsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/aimux/cmd/output.go cmd/aimux/cmd/output_test.go go.mod go.sum
git commit -m "feat(cli): add output infrastructure with exit codes and structured errors"
```

---

### Task 2: Create root cobra command

**Files:**
- Create: `cmd/aimux/cmd/root.go`

- [ ] **Step 1: Implement root.go**

```go
// cmd/aimux/cmd/root.go
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var jsonOutput bool

var rootCmd = &cobra.Command{
	Use:   "aimux",
	Short: "AI agent multiplexer",
	Long:  "aimux — TUI dashboard for managing multiple AI coding agent sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		webFlag, _ := cmd.Flags().GetBool("web")
		if webFlag {
			return runBothFn(cmd, args)
		}
		return runTUIFn(cmd, args)
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	runTUIFn  func(cmd *cobra.Command, args []string) error
	runBothFn func(cmd *cobra.Command, args []string) error
)

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.Flags().Bool("web", false, "Launch TUI + web dashboard")
}

func Execute(version string) {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate("aimux {{.Version}}\n")
	if err := rootCmd.Execute(); err != nil {
		out := NewOutputWriter(jsonOutput)
		code := out.WriteError(err.Error(), ExitError, nil)
		os.Exit(code)
	}
}

func SetRunTUI(fn func(cmd *cobra.Command, args []string) error) {
	runTUIFn = fn
}

func SetRunBoth(fn func(cmd *cobra.Command, args []string) error) {
	runBothFn = fn
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./cmd/aimux/cmd/`
Expected: compiles with no errors

- [ ] **Step 3: Commit**

```bash
git add cmd/aimux/cmd/root.go
git commit -m "feat(cli): add root cobra command with --json and --web flags"
```

---

### Task 3: Create version command

**Files:**
- Create: `cmd/aimux/cmd/version.go`
- Create: `cmd/aimux/cmd/version_test.go`

- [ ] **Step 1: Write failing tests**

```go
// cmd/aimux/cmd/version_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"runtime"
	"testing"
)

func TestVersionCmd_Text(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := newVersionCmd()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"version"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("aimux")) {
		t.Errorf("output missing 'aimux', got %q", stdout.String())
	}
}

func TestVersionCmd_JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := newVersionCmd()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"version", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if got["os"] != runtime.GOOS {
		t.Errorf("os=%q, want %q", got["os"], runtime.GOOS)
	}
	if got["arch"] != runtime.GOARCH {
		t.Errorf("arch=%q, want %q", got["arch"], runtime.GOARCH)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestVersionCmd -v`
Expected: FAIL — `newVersionCmd` not defined

- [ ] **Step 3: Implement version.go**

```go
// cmd/aimux/cmd/version.go
package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show aimux version",
		RunE: func(cmd *cobra.Command, args []string) error {
			ver := rootCmd.Version
			if ver == "" {
				ver = "dev"
			}
			if jsonOutput {
				data := map[string]string{
					"version": ver,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				}
				b, _ := json.MarshalIndent(data, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "aimux %s\n", ver)
			}
			return nil
		},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestVersionCmd -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/cmd/version.go cmd/aimux/cmd/version_test.go
git commit -m "feat(cli): add version subcommand with --json support"
```

---

### Task 4: Create agents command

**Files:**
- Create: `cmd/aimux/cmd/agents.go`
- Create: `cmd/aimux/cmd/agents_test.go`

This command uses `discovery.Orchestrator` to list running agents.

- [ ] **Step 1: Write failing tests**

```go
// cmd/aimux/cmd/agents_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

type mockProvider struct {
	agents []agent.Agent
}

func (m *mockProvider) Name() string                          { return "mock" }
func (m *mockProvider) Discover() ([]agent.Agent, error)      { return m.agents, nil }

func TestAgentsCmd_JSON_Empty(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := newAgentsCmd(func() ([]agent.Agent, error) { return nil, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"agents", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	// Should succeed but with empty list
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["count"].(float64) != 0 {
		t.Errorf("count=%v, want 0", result["count"])
	}
}

func TestAgentsCmd_JSON_WithAgents(t *testing.T) {
	var stdout, stderr bytes.Buffer
	agents := []agent.Agent{
		{PID: 1234, ProviderName: "claude", Name: "aimux", Status: agent.StatusActive, WorkingDir: "/tmp/test"},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"agents", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []struct {
			PID      int    `json:"pid"`
			Provider string `json:"provider"`
			Status   string `json:"status"`
		} `json:"agents"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("count=%d, want 1", result.Count)
	}
	if result.Agents[0].Provider != "claude" {
		t.Errorf("provider=%q, want %q", result.Agents[0].Provider, "claude")
	}
}

func TestAgentsCmd_Limit(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1, ProviderName: "claude", Name: "a", Status: agent.StatusActive},
		{PID: 2, ProviderName: "codex", Name: "b", Status: agent.StatusActive},
		{PID: 3, ProviderName: "gemini", Name: "c", Status: agent.StatusActive},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agents", "--json", "--limit", "2"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []any `json:"agents"`
		Count  int   `json:"count"`
		Total  int   `json:"total"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2", result.Count)
	}
	if result.Total != 3 {
		t.Errorf("total=%d, want 3", result.Total)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestAgentsCmd -v`
Expected: FAIL — `newAgentsCmd` not defined

- [ ] **Step 3: Implement agents.go**

```go
// cmd/aimux/cmd/agents.go
package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/agent"
)

type agentJSON struct {
	PID         int    `json:"pid"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	Project     string `json:"project"`
	DisplayName string `json:"display_name"`
	SessionID   string `json:"session_id,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
	Model       string `json:"model,omitempty"`
	WorkingDir  string `json:"working_dir"`
}

type discoverFunc func() ([]agent.Agent, error)

func newAgentsCmd(discover discoverFunc) *cobra.Command {
	var limit int
	var fields string

	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List running AI agents",
		Long:  "Discover and list all running AI coding agent sessions (Claude, Codex, Gemini)",
		RunE: func(cmd *cobra.Command, args []string) error {
			agents, err := discover()
			if err != nil {
				return fmt.Errorf("discovery failed: %w", err)
			}

			total := len(agents)
			truncated := false
			if limit > 0 && len(agents) > limit {
				agents = agents[:limit]
				truncated = true
			}

			if jsonOutput {
				items := make([]agentJSON, len(agents))
				for i, a := range agents {
					items[i] = agentJSON{
						PID:         a.PID,
						Provider:    a.ProviderName,
						Status:      a.Status.String(),
						Project:     a.Name,
						DisplayName: a.ProviderName + ":" + a.Name,
						SessionID:   a.SessionID,
						TmuxSession: a.TMuxSession,
						Model:       a.ShortModel(),
						WorkingDir:  a.WorkingDir,
					}
				}
				result := map[string]any{
					"agents": items,
					"count":  len(items),
				}
				if truncated {
					result["total"] = total
					result["truncated"] = true
					result["hint"] = "use --limit to control result count"
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				if len(agents) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No agents running.")
					return nil
				}
				selectedFields := parseFields(fields)
				printAgentsTable(cmd, agents, selectedFields)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of agents to show")
	cmd.Flags().StringVar(&fields, "fields", "", "Comma-separated fields: pid,provider,status,project,model,session_id,tmux_session,working_dir")
	return cmd
}

func printAgentsTable(cmd *cobra.Command, agents []agent.Agent, fields []string) {
	// Default: show all core fields
	if len(fields) == 0 {
		fields = []string{"pid", "provider", "status", "project", "model"}
	}
	// Header
	var headers []string
	for _, f := range fields {
		headers = append(headers, strings.ToUpper(f))
	}
	fmt.Fprintln(cmd.OutOrStdout(), strings.Join(headers, "\t"))

	for _, a := range agents {
		var vals []string
		for _, f := range fields {
			switch f {
			case "pid":
				vals = append(vals, fmt.Sprintf("%d", a.PID))
			case "provider":
				vals = append(vals, a.ProviderName)
			case "status":
				vals = append(vals, a.Status.String())
			case "project":
				vals = append(vals, a.Name)
			case "model":
				vals = append(vals, a.ShortModel())
			case "session_id":
				vals = append(vals, a.SessionID)
			case "tmux_session":
				vals = append(vals, a.TMuxSession)
			case "working_dir":
				vals = append(vals, a.WorkingDir)
			default:
				vals = append(vals, "")
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(vals, "\t"))
	}
}

func parseFields(s string) []string {
	if s == "" {
		return nil
	}
	var fields []string
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestAgentsCmd -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/cmd/agents.go cmd/aimux/cmd/agents_test.go
git commit -m "feat(cli): add agents subcommand with --json, --limit, --fields"
```

---

### Task 5: Create sessions command

**Files:**
- Create: `cmd/aimux/cmd/sessions.go`
- Create: `cmd/aimux/cmd/sessions_test.go`

This is the largest command. It migrates the existing `runSessions()` from `main.go` into cobra.

- [ ] **Step 1: Write failing tests**

```go
// cmd/aimux/cmd/sessions_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

func fakeSessions() []history.Session {
	return []history.Session{
		{
			ID:          "sess-001",
			Provider:    "claude",
			Project:     "/home/user/project-a",
			TurnCount:   15,
			CostUSD:     1.23,
			LastActive:  time.Now().Add(-1 * time.Hour),
			FirstPrompt: "fix the tests",
			Title:       "Test fixes",
		},
		{
			ID:          "sess-002",
			Provider:    "codex",
			Project:     "/home/user/project-b",
			TurnCount:   8,
			CostUSD:     0.45,
			LastActive:  time.Now().Add(-2 * time.Hour),
			FirstPrompt: "add logging",
			Title:       "Logging feature",
		},
	}
}

func TestSessionsCmd_JSON(t *testing.T) {
	var stdout bytes.Buffer
	sessions := fakeSessions()
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) { return sessions, nil },
		nil, nil, nil,
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "--list", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2", result.Count)
	}
}

func TestSessionsCmd_Limit(t *testing.T) {
	var stdout bytes.Buffer
	sessions := fakeSessions()
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) { return sessions, nil },
		nil, nil, nil,
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "--list", "--json", "--limit", "1"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Sessions  []any `json:"sessions"`
		Count     int   `json:"count"`
		Total     int   `json:"total"`
		Truncated bool  `json:"truncated"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("count=%d, want 1", result.Count)
	}
	if result.Total != 2 {
		t.Errorf("total=%d, want 2", result.Total)
	}
	if !result.Truncated {
		t.Error("expected truncated=true")
	}
}

func TestSessionsCmd_Export(t *testing.T) {
	var stdout bytes.Buffer
	sessions := fakeSessions()
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) { return sessions, nil },
		nil, nil, nil,
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "--export"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Errorf("expected 2 JSONL lines, got %d", len(lines))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestSessionsCmd -v`
Expected: FAIL — `newSessionsCmd` not defined

- [ ] **Step 3: Implement sessions.go**

The `newSessionsCmd` function takes injectable dependencies for testability:
- `discoverFn`: replaces `history.Discover`
- `searchFn`: replaces content search
- `pickerFn`: replaces interactive picker
- `resumeFn`: replaces session resumption

```go
// cmd/aimux/cmd/sessions.go
package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/history"
)

type sessionsDiscoverFn func(opts history.DiscoverOpts, dir string) ([]history.Session, error)
type sessionsSearchFn func(query, dir string) ([]history.ContentMatch, error)
type sessionsPickerFn func(sessions []history.Session) (*history.Session, error)
type sessionsResumeFn func(sessionID string, danger bool)

// ContentMatch mirrors history.ContentMatch for the search callback.
type ContentMatch = history.ContentMatch

func newSessionsCmd(discover sessionsDiscoverFn, search sessionsSearchFn, picker sessionsPickerFn, resume sessionsResumeFn) *cobra.Command {
	var dir string
	var listMode, exportMode, danger bool
	var limit int
	var fields string

	cmd := &cobra.Command{
		Use:   "sessions [query]",
		Short: "Browse and search past sessions",
		Long:  "List, search, and resume past AI agent sessions. Without --list, launches interactive picker (TTY only).",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := history.DiscoverOpts{Dir: dir, Limit: 0}
			allSessions, err := discover(opts, "")
			if err != nil {
				return fmt.Errorf("session discovery failed: %w", err)
			}

			// Filter out near-empty and subagent sessions
			var filtered []history.Session
			for _, s := range allSessions {
				if s.TurnCount <= 5 && s.CostUSD == 0 {
					continue
				}
				if s.LastActive.IsZero() {
					continue
				}
				if s.IsSubagent {
					continue
				}
				filtered = append(filtered, s)
			}

			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			if query != "" && search != nil {
				filtered = searchSessionsFiltered(filtered, query, search)
				if len(filtered) == 0 {
					out := NewOutputWriter(jsonOutput)
					out.Stdout = cmd.OutOrStdout()
					out.Stderr = cmd.ErrOrStderr()
					out.WriteError(fmt.Sprintf("no sessions matching %q", query), ExitNotFound, nil)
					return fmt.Errorf("no sessions matching %q", query)
				}
			}

			total := len(filtered)
			truncated := false
			if limit > 0 && len(filtered) > limit {
				filtered = filtered[:limit]
				truncated = true
			}

			if exportMode {
				for _, s := range filtered {
					data, _ := json.Marshal(s)
					fmt.Fprintln(cmd.OutOrStdout(), string(data))
				}
				return nil
			}

			if listMode || !IsInteractive() {
				if jsonOutput {
					result := map[string]any{
						"sessions": filtered,
						"count":    len(filtered),
					}
					if truncated {
						result["total"] = total
						result["truncated"] = true
						result["hint"] = "use --limit to control result count"
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					printSessionsTableCobra(cmd, filtered, parseFields(fields))
				}
				return nil
			}

			// Interactive mode
			if picker == nil {
				return fmt.Errorf("interactive mode not available")
			}
			selected, err := picker(filtered)
			if err != nil {
				return err
			}
			if resume != nil {
				resume(selected.ID, danger)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Scope to a specific directory")
	cmd.Flags().BoolVarP(&listMode, "list", "l", false, "Table output (scriptable)")
	cmd.Flags().BoolVar(&exportMode, "export", false, "JSONL output for eval pipelines")
	cmd.Flags().BoolVarP(&danger, "danger", "d", false, "Resume with --dangerously-skip-permissions")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max sessions to show (0 = all)")
	cmd.Flags().StringVar(&fields, "fields", "", "Comma-separated fields: id,provider,project,age,turns,cost,annotation,prompt,tags")
	return cmd
}

func searchSessionsFiltered(allSessions []history.Session, query string, searchFn sessionsSearchFn) []history.Session {
	matched := history.FilterByPrompt(allSessions, query)
	if len(matched) >= 3 {
		return matched
	}
	if searchFn == nil {
		return matched
	}
	contentMatches, err := searchFn(query, "")
	if err != nil {
		return matched
	}
	seen := make(map[string]bool)
	for _, s := range matched {
		seen[s.ID] = true
	}
	sessionByID := make(map[string]history.Session)
	for _, s := range allSessions {
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

func printSessionsTableCobra(cmd *cobra.Command, sessions []history.Session, fields []string) {
	if len(sessions) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
		return
	}
	if len(fields) == 0 {
		fields = []string{"id", "project", "age", "turns", "cost", "prompt"}
	}
	var headers []string
	for _, f := range fields {
		headers = append(headers, strings.ToUpper(f))
	}
	fmt.Fprintln(cmd.OutOrStdout(), strings.Join(headers, "\t"))

	for _, s := range sessions {
		var vals []string
		for _, f := range fields {
			switch f {
			case "id":
				vals = append(vals, s.ID)
			case "provider":
				vals = append(vals, s.Provider)
			case "project":
				vals = append(vals, shortProject(s.Project))
			case "age":
				vals = append(vals, shortAgeFmt(s.LastActive))
			case "turns":
				vals = append(vals, fmt.Sprintf("%d", s.TurnCount))
			case "cost":
				vals = append(vals, fmt.Sprintf("$%.2f", s.CostUSD))
			case "annotation":
				a := s.Annotation
				if a == "" {
					a = "-"
				}
				vals = append(vals, a)
			case "prompt":
				p := s.Title
				if p == "" {
					p = s.FirstPrompt
				}
				if len(p) > 40 {
					p = p[:37] + "..."
				}
				if p == "" {
					p = "-"
				}
				vals = append(vals, p)
			case "tags":
				if len(s.Tags) > 0 {
					vals = append(vals, strings.Join(s.Tags, ","))
				} else {
					vals = append(vals, "-")
				}
			default:
				vals = append(vals, "")
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(vals, "\t"))
	}
}

func shortProject(path string) string {
	if path == "" {
		return "(unknown)"
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) > 0 && parts[len(parts)-1] != "" {
		return parts[len(parts)-1]
	}
	return path
}

func shortAgeFmt(t time.Time) string {
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestSessionsCmd -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/cmd/sessions.go cmd/aimux/cmd/sessions_test.go
git commit -m "feat(cli): add sessions subcommand with --json, --list, --limit, --fields, --export"
```

---

### Task 6: Create resume command

**Files:**
- Create: `cmd/aimux/cmd/resume.go`
- Create: `cmd/aimux/cmd/resume_test.go`

- [ ] **Step 1: Write failing tests**

```go
// cmd/aimux/cmd/resume_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResumeCmd_DryRun_JSON(t *testing.T) {
	var stdout bytes.Buffer
	var resumed bool
	c := newResumeCmd(func(id string, danger bool) (string, string, error) {
		resumed = true
		return "claude --resume " + id, "/tmp/project", nil
	})
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"resume", "abc-123", "--dry-run", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resumed {
		t.Error("dry-run should not resume")
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if result["command"] != "claude --resume abc-123" {
		t.Errorf("command=%q, want %q", result["command"], "claude --resume abc-123")
	}
}

func TestResumeCmd_MissingID(t *testing.T) {
	var stderr bytes.Buffer
	c := newResumeCmd(nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"resume"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestResumeCmd -v`
Expected: FAIL — `newResumeCmd` not defined

- [ ] **Step 3: Implement resume.go**

The `resumeBuilderFn` returns the command string and work dir without executing, enabling dry-run.

```go
// cmd/aimux/cmd/resume.go
package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type resumeBuilderFn func(sessionID string, danger bool) (command, workDir string, err error)

func newResumeCmd(builder resumeBuilderFn) *cobra.Command {
	var danger, dryRun bool

	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a past session",
		Long:  "Resume an AI agent session by its ID. Use --dry-run to preview the command without executing.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]

			if builder == nil {
				return fmt.Errorf("resume not configured")
			}

			command, workDir, err := builder(sessionID, danger)
			if err != nil {
				return err
			}

			if dryRun {
				if jsonOutput {
					result := map[string]any{
						"command":  command,
						"work_dir": workDir,
						"dry_run":  true,
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Would run: %s\n", command)
					if workDir != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "In: %s\n", workDir)
					}
				}
				return nil
			}

			// Actual resume is handled by the caller (main.go wires the real exec logic)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&danger, "danger", "d", false, "Resume with --dangerously-skip-permissions")
	cmd.Flags().BoolVar(&danger, "force", false, "Alias for --danger")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the resume command without executing")
	return cmd
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestResumeCmd -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/cmd/resume.go cmd/aimux/cmd/resume_test.go
git commit -m "feat(cli): add resume subcommand with --dry-run, --force, --json"
```

---

### Task 7: Create spawn command

**Files:**
- Create: `cmd/aimux/cmd/spawn.go`
- Create: `cmd/aimux/cmd/spawn_test.go`

- [ ] **Step 1: Write failing tests**

```go
// cmd/aimux/cmd/spawn_test.go
package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSpawnCmd_DryRun_JSON(t *testing.T) {
	var stdout bytes.Buffer
	c := newSpawnCmd(
		[]string{"claude", "codex", "gemini"},
		func(provider, dir, model, mode, prompt string) (pid int, tmuxSession string, err error) {
			return 0, "", nil
		},
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"spawn", "claude", "--dry-run", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if result["provider"] != "claude" {
		t.Errorf("provider=%q, want %q", result["provider"], "claude")
	}
}

func TestSpawnCmd_InvalidProvider(t *testing.T) {
	var stderr bytes.Buffer
	c := newSpawnCmd(
		[]string{"claude", "codex", "gemini"},
		nil,
	)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"spawn", "gpt", "--json"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
	errStr := err.Error()
	if !bytes.Contains([]byte(errStr), []byte("claude")) {
		t.Errorf("error should list valid providers, got: %s", errStr)
	}
}

func TestSpawnCmd_MissingProvider(t *testing.T) {
	c := newSpawnCmd([]string{"claude", "codex", "gemini"}, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"spawn"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing provider arg")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestSpawnCmd -v`
Expected: FAIL — `newSpawnCmd` not defined

- [ ] **Step 3: Implement spawn.go**

```go
// cmd/aimux/cmd/spawn.go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type spawnFn func(provider, dir, model, mode, prompt string) (pid int, tmuxSession string, err error)

func newSpawnCmd(validProviders []string, spawn spawnFn) *cobra.Command {
	var dir, model, mode, prompt string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "spawn <provider>",
		Short: "Start a new AI agent session",
		Long:  fmt.Sprintf("Launch a new AI agent session. Provider must be one of: %s", strings.Join(validProviders, ", ")),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]

			valid := false
			for _, vp := range validProviders {
				if provider == vp {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Errorf("invalid provider %q (must be one of: %s)", provider, strings.Join(validProviders, ", "))
			}

			if dir == "" {
				dir, _ = os.Getwd()
			}

			if dryRun {
				if jsonOutput {
					result := map[string]any{
						"provider": provider,
						"dir":      dir,
						"model":    model,
						"mode":     mode,
						"prompt":   prompt,
						"dry_run":  true,
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Would spawn %s in %s\n", provider, dir)
					if model != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "Model: %s\n", model)
					}
				}
				return nil
			}

			if spawn == nil {
				return fmt.Errorf("spawn not configured")
			}

			pid, tmuxSession, err := spawn(provider, dir, model, mode, prompt)
			if err != nil {
				return fmt.Errorf("spawn failed: %w", err)
			}

			if jsonOutput {
				result := map[string]any{
					"provider":     provider,
					"pid":          pid,
					"tmux_session": tmuxSession,
					"dir":          dir,
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Spawned %s (tmux: %s)\n", provider, tmuxSession)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Working directory (default: current)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&mode, "mode", "", "Mode (e.g., plan, auto)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Initial prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show spawn command without executing")
	return cmd
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/aimux/cmd/ -timeout 30s -run TestSpawnCmd -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/cmd/spawn.go cmd/aimux/cmd/spawn_test.go
git commit -m "feat(cli): add spawn subcommand with --dry-run, --json, provider validation"
```

---

### Task 8: Create web command

**Files:**
- Create: `cmd/aimux/cmd/web.go`

No tests needed (thin wrapper around web.Server which has its own tests).

- [ ] **Step 1: Implement web.go**

```go
// cmd/aimux/cmd/web.go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

type webServerFn func(port int) error

func newWebCmd(startServer webServerFn) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "web",
		Short: "Launch web dashboard",
		Long:  "Start the aimux web dashboard for browser-based agent monitoring",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "aimux web dashboard: http://127.0.0.1:%d\n", port)
			if startServer == nil {
				return fmt.Errorf("web server not configured")
			}
			return startServer(port)
		},
	}

	cmd.Flags().IntVar(&port, "port", 3000, "Port to listen on")
	return cmd
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./cmd/aimux/cmd/`
Expected: compiles

- [ ] **Step 3: Commit**

```bash
git add cmd/aimux/cmd/web.go
git commit -m "feat(cli): add web subcommand with --port flag"
```

---

### Task 9: Rewrite main.go to wire cobra commands

**Files:**
- Modify: `cmd/aimux/main.go` (rewrite)

This is the integration task: main.go becomes a slim file that wires real implementations to the cobra commands.

- [ ] **Step 1: Rewrite main.go**

Replace the entire content of `cmd/aimux/main.go` with:

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/cmd/aimux/cmd"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/frontend/tui"
	"github.com/zanetworker/aimux/internal/frontend/web"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/sessions"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/tasks"
	"github.com/zanetworker/aimux/internal/trace"
)

var version = "dev"

func main() {
	// Wire TUI launcher
	cmd.SetRunTUI(func(c *cobra.Command, args []string) error {
		debuglog.Init()
		defer debuglog.Close()
		debuglog.Log("aimux starting (version %s)", version)
		app := tui.NewApp()
		p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
		_, err := p.Run()
		return err
	})

	// Wire TUI + web launcher
	cmd.SetRunBoth(func(c *cobra.Command, args []string) error {
		port := 3000
		s := createWebServer(port)
		go func() {
			fmt.Printf("aimux web dashboard: http://127.0.0.1:%d\n", port)
			if err := s.Start(); err != nil {
				debuglog.Log("web server error: %v", err)
			}
		}()
		debuglog.Init()
		defer debuglog.Close()
		app := tui.NewApp()
		p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
		_, err := p.Run()
		return err
	})

	cmd.Execute(version)
}

// createWebServer sets up the full web server with all dependencies wired.
// Preserved from the original main.go — no logic changes.
func createWebServer(port int) *web.Server {
	cfg, _ := config.Load(config.DefaultPath())
	disco := discovery.NewOrchestrator(
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
	)

	s := web.NewServer(port)
	s.SetDiscoverFunc(disco.Discover)
	s.SetLaunchFunc(func(providerName, dir, model, mode, prompt string) error {
		p := disco.ProviderFor(providerName)
		if p == nil {
			return fmt.Errorf("unknown provider: %s", providerName)
		}
		type spawner interface {
			SpawnCommand(dir, model, mode string) *exec.Cmd
		}
		sp, ok := p.(spawner)
		if !ok {
			return fmt.Errorf("provider %s does not support spawning", providerName)
		}
		c := sp.SpawnCommand(dir, model, mode)
		if c == nil {
			return fmt.Errorf("failed to build spawn command for %s", providerName)
		}
		if prompt != "" {
			switch providerName {
			case "claude", "gemini":
				c.Args = append(c.Args, prompt)
			case "codex":
				c.Args = append(c.Args, "--prompt", prompt)
			default:
				c.Args = append(c.Args, prompt)
			}
		}
		shell := cfg.ResolveShell()
		return spawn.Launch(c, providerName, dir, "tmux", shell, "")
	})
	s.SetKillFunc(func(pid int, tmuxSession string) error {
		if tmuxSession != "" {
			_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run() // #nosec G204
		}
		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
		return nil
	})
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		p := disco.ProviderFor(name)
		if p == nil {
			return &provider.Claude{}
		}
		type tracer interface {
			ParseTrace(string) ([]trace.Turn, error)
		}
		if t, ok := p.(tracer); ok {
			return t
		}
		return &provider.Claude{}
	})
	s.SetController(controller.New(cfg))
	s.SetConfig(cfg)

	allPlugins := plugin.Builtins()
	if custom, err := plugin.ScanPlugins(plugin.DefaultPluginsDir()); err == nil {
		allPlugins = append(allPlugins, custom...)
	}
	if len(allPlugins) > 0 {
		s.SetPluginExecutor(plugin.NewExecutor(allPlugins))
	}
	s.SetRecentDirsFunc(func(max int) []web.RecentDirInfo {
		type dirEntry struct {
			path     string
			lastUsed time.Time
		}
		byPath := make(map[string]*dirEntry)
		providers := []provider.Provider{&provider.Claude{}, &provider.Codex{}, &provider.Gemini{}}
		for _, p := range providers {
			for _, rd := range p.RecentDirs(max) {
				if existing, ok := byPath[rd.Path]; ok {
					if rd.LastUsed.After(existing.lastUsed) {
						existing.lastUsed = rd.LastUsed
					}
				} else {
					byPath[rd.Path] = &dirEntry{path: rd.Path, lastUsed: rd.LastUsed}
				}
			}
		}
		sorted := make([]*dirEntry, 0, len(byPath))
		for _, de := range byPath {
			sorted = append(sorted, de)
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].lastUsed.After(sorted[j].lastUsed) })
		if len(sorted) > max {
			sorted = sorted[:max]
		}
		var result []web.RecentDirInfo
		for _, de := range sorted {
			display := filepath.Base(de.path)
			if display == "" || display == "." {
				display = de.path
			}
			age := ""
			if !de.lastUsed.IsZero() {
				d := time.Since(de.lastUsed)
				switch {
				case d < time.Minute:
					age = fmt.Sprintf("%ds ago", int(d.Seconds()))
				case d < time.Hour:
					age = fmt.Sprintf("%dm ago", int(d.Minutes()))
				case d < 24*time.Hour:
					age = fmt.Sprintf("%dh ago", int(d.Hours()))
				default:
					age = fmt.Sprintf("%dd ago", int(d.Hours()/24))
				}
			}
			result = append(result, web.RecentDirInfo{Path: de.path, Display: display, Age: age})
		}
		return result
	})
	taskProvider, taskErr := tasks.NewProvider(cfg.Tasks.Backend, cfg.Tasks.MCPEndpoint)
	if taskErr != nil {
		debuglog.Log("tasks: %v (tasks panel will be unavailable)", taskErr)
	}
	if taskProvider != nil {
		s.SetTaskProvider(taskProvider)
	}
	return s
}

// init wires the real implementations to cobra commands defined in cmd/.
func init() {
	// The import of the cmd package triggers cmd.init() which sets up the root command.
	// We don't register subcommands here — that happens in cmd/register.go.
}

// These are unused but referenced by cmd/ — they live here to avoid circular imports.
var _ = sessions.PickSession
var _ = history.Discover
var _ = strings.TrimSpace
```

Wait — this approach has a problem. The `cmd` package can't import `main` (circular), and `main` needs to inject real implementations into `cmd`. The cleaner pattern is to have `cmd/register.go` accept injected dependencies via `cmd.RegisterAll(...)` called from main.

Let me revise. The approach is:

1. `cmd/root.go` — root command, `Execute()`
2. `cmd/register.go` — `RegisterAll(deps)` adds all subcommands with real deps
3. `main.go` — creates deps, calls `cmd.RegisterAll(deps)`, then `cmd.Execute(version)`

- [ ] **Step 2: Create cmd/register.go**

```go
// cmd/aimux/cmd/register.go
package cmd

import (
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

// Deps holds the real implementations injected from main.
type Deps struct {
	Discover      func() ([]agent.Agent, error)
	DiscoverSessions func(opts history.DiscoverOpts, dir string) ([]history.Session, error)
	SearchContent func(query, dir string) ([]history.ContentMatch, error)
	PickSession   func(sessions []history.Session) (*history.Session, error)
	ResumeBuilder func(sessionID string, danger bool) (command, workDir string, err error)
	ResumeExec    func(sessionID string, danger bool)
	SpawnAgent    func(provider, dir, model, mode, prompt string) (pid int, tmuxSession string, err error)
	WebServer     func(port int) error
	Providers     []string
}

func RegisterAll(d Deps) {
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newAgentsCmd(d.Discover))
	rootCmd.AddCommand(newSessionsCmd(d.DiscoverSessions, d.SearchContent, d.PickSession, d.ResumeExec))
	rootCmd.AddCommand(newResumeCmd(d.ResumeBuilder))
	rootCmd.AddCommand(newSpawnCmd(d.Providers, d.SpawnAgent))
	rootCmd.AddCommand(newWebCmd(d.WebServer))
}
```

- [ ] **Step 3: Rewrite main.go**

Replace `cmd/aimux/main.go` with the slim wiring version that constructs `Deps` and calls `RegisterAll`. Keep `createWebServer` as-is (it's the web server factory from the original). Wire the real `history.Discover`, `sessions.PickSession`, discovery orchestrator, and spawn logic into `Deps`.

The key wiring points:
- `Deps.Discover`: `discovery.NewOrchestrator(...).Discover`
- `Deps.DiscoverSessions`: `history.Discover`
- `Deps.SearchContent`: `history.SearchContent`
- `Deps.PickSession`: `sessions.PickSession`
- `Deps.ResumeBuilder`: builds the claude resume command string + work dir
- `Deps.ResumeExec`: actually exec's the resume (calls `resumeSession` from current code)
- `Deps.SpawnAgent`: wraps `provider.SpawnCommand` + `spawn.Launch`
- `Deps.Providers`: `[]string{"claude", "codex", "gemini"}`

- [ ] **Step 4: Build and verify**

Run: `go build -o aimux ./cmd/aimux`
Expected: compiles

- [ ] **Step 5: Manual smoke test**

Run:
```bash
./aimux version
./aimux version --json
./aimux agents --json
./aimux sessions --list --json --limit 5
./aimux spawn claude --dry-run --json
./aimux resume abc --dry-run --json
./aimux --help
```

Expected: all produce valid JSON where `--json` is used, help text shows all subcommands.

- [ ] **Step 6: Run full test suite**

Run: `go test ./... -timeout 30s`
Expected: ALL tests pass, no regressions

- [ ] **Step 7: Commit**

```bash
git add cmd/aimux/main.go cmd/aimux/cmd/register.go
git commit -m "feat(cli): migrate to cobra with command-per-file layout

Replaces hand-rolled os.Args parsing with cobra subcommands.
All commands support --json for structured output.
Exit codes follow taxonomy: 0=success, 1=error, 2=usage, 3=not-found, 4=config.
New commands: agents, spawn, version.
Backwards compatible: bare 'aimux' still launches TUI, --web still works."
```

---

### Task 10: Update docs-site with agent usage guide

**Files:**
- Create: `docs-site/src/content/docs/guides/agent-usage.mdx`
- Modify: `astro.config.mjs` (add sidebar entry)

- [ ] **Step 1: Create agent-usage.mdx**

```mdx
---
title: Agent Usage
description: Using aimux from AI coding agents and scripts
---

# Agent Usage

aimux supports structured output for consumption by AI coding agents (Claude Code, Codex, Gemini CLI) and scripts.

## Structured Output

Add `--json` to any command for machine-parseable JSON output:

```bash
aimux agents --json
aimux sessions --list --json
aimux version --json
aimux spawn claude --dry-run --json
aimux resume <id> --dry-run --json
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Usage error (bad flags, missing args) |
| 3 | Not found (no sessions, no agents) |
| 4 | Config error |

## Non-Interactive Mode

When stdin is not a TTY (e.g., piped into another command or called by an agent), `aimux sessions` automatically behaves as `aimux sessions --list` instead of launching the interactive picker.

## Field Selection

Use `--fields` to select specific output columns:

```bash
aimux sessions --list --json --fields id,project,cost
aimux agents --json --fields pid,provider,status
```

## Bounded Output

Use `--limit` to control result count. When results are truncated, JSON output includes a hint:

```json
{"sessions": [...], "count": 10, "total": 142, "truncated": true, "hint": "use --limit to control result count"}
```

## Dry Run

Preview commands before executing:

```bash
aimux spawn claude --dir ./myproject --dry-run --json
aimux resume <id> --dry-run --json
```
```

- [ ] **Step 2: Add sidebar entry in astro.config.mjs**

Find the `guides` section in the sidebar config and add:
```
{ label: 'Agent Usage', slug: 'guides/agent-usage' },
```

- [ ] **Step 3: Build docs to verify**

Run: `cd docs-site && npm run build`
Expected: builds without errors

- [ ] **Step 4: Commit**

```bash
git add docs-site/src/content/docs/guides/agent-usage.mdx astro.config.mjs
git commit -m "docs: add agent usage guide for structured CLI output"
```

---

### Task 11: Final integration test

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -timeout 30s`
Expected: ALL tests pass

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 3: Build**

Run: `go build -o aimux ./cmd/aimux`
Expected: compiles

- [ ] **Step 4: End-to-end smoke test**

```bash
# All commands produce valid output
./aimux --help
./aimux version
./aimux version --json
./aimux agents --json
./aimux sessions --list --json --limit 3
./aimux sessions --list --fields id,cost
./aimux sessions --export | head -2
./aimux spawn claude --dry-run --json
./aimux spawn gpt 2>&1  # should show "must be one of: claude, codex, gemini"
./aimux resume test-123 --dry-run --json
./aimux web --port 0 &  # starts and binds (kill after)

# JSON validation
./aimux agents --json | python3 -m json.tool > /dev/null
./aimux sessions --list --json | python3 -m json.tool > /dev/null
./aimux version --json | python3 -m json.tool > /dev/null
```

Expected: all commands work, JSON is valid, error messages include valid values

- [ ] **Step 5: Commit any fixes from smoke testing**
