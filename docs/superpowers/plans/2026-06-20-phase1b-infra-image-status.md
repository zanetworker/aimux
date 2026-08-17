# Phase 1B: Infrastructure, Universal Image, and aimux status

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Use development-tools:crafted-code for all code implementation.

**Goal:** Build the universal worker image (Claude+Codex+Gemini CLIs), CI pipeline for amd64 builds with validate-before-publish, and the `aimux status` command for local and remote health checks.

**Architecture:** Universal Dockerfile with `node:20-slim` and agent CLIs installed via npm. The task runner (`run_task.py`) uses CLI headless modes (not Python SDKs) so each provider edits files and uses tools like a real coding agent. Each task gets a fresh workspace directory to prevent cross-task contamination. GitHub Actions builds locally, smoke-tests, then pushes only on success. Version pinning opens a PR instead of committing directly to main. `aimux status` backed by `internal/healthcheck/` with real connectivity checks.

**Tech Stack:** Docker, GitHub Actions, Go (cobra), `openshell` CLI

**Independent of:** Plan A (MCP server backend changes). Plan A is already complete.

**Review checkpoint rule:** Pause for review after every task. Never batch more than 3 files of changes without pausing. Do NOT commit; the user will decide when to commit.

**Already done:** `RemoteConfig` struct exists in `internal/config/config.go` (backend, gateway, image, warm_pool fields).

---

### Task 1: Universal worker Dockerfile

**Files:**
- Create: `runtime/agents/universal/Dockerfile`
- Create: `runtime/agents/universal/entrypoint.sh`
- Create: `runtime/agents/universal/run_task.py`

- [ ] **Step 1: Create the directory**

Run: `mkdir -p runtime/agents/universal`

- [ ] **Step 2: Create the Dockerfile**

```dockerfile
FROM --platform=$TARGETPLATFORM node:20-slim

# System dependencies + OpenShell sandbox requirements
RUN apt-get update && apt-get install -y --no-install-recommends \
    git python3 ca-certificates curl wget \
    build-essential tmux jq \
    iproute2 nftables \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# OpenShell sandbox user (required until NVIDIA/OpenShell#1959 lands)
RUN useradd -m -s /bin/bash -u 1000660000 sandbox

# Agent CLIs (pinned to tested versions)
ARG CLAUDE_CODE_VERSION=2.1.183
ARG CODEX_VERSION=0.141.0
RUN npm install -g \
    @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
    @openai/codex@${CODEX_VERSION}

# No Python SDKs. run_task.py shells out to CLI headless modes.
# codex-sdk-python is not the official SDK. google-genai is a model API,
# not the Gemini coding harness. See review finding #3.

# Agent task runner
COPY run_task.py /opt/agent/run_task.py
COPY entrypoint.sh /opt/entrypoint.sh
RUN chmod +x /opt/entrypoint.sh /opt/agent/run_task.py

ENV PYTHONUNBUFFERED=1
ENTRYPOINT ["/opt/entrypoint.sh"]
```

- [ ] **Step 3: Create entrypoint.sh**

```bash
#!/bin/bash
set -e

# Emergency version override (skip if not set)
[ -n "$CLAUDE_OVERRIDE_VERSION" ] && npm install -g @anthropic-ai/claude-code@${CLAUDE_OVERRIDE_VERSION}
[ -n "$CODEX_OVERRIDE_VERSION" ] && npm install -g @openai/codex@${CODEX_OVERRIDE_VERSION}

case "${AIMUX_MODE:-worker}" in
  worker)   exec python3 /opt/agent/run_task.py "$@" ;;
  session)  exec sleep infinity ;;
  *)        echo "Unknown AIMUX_MODE: ${AIMUX_MODE}" >&2; exit 1 ;;
esac
```

- [ ] **Step 4: Create run_task.py**

Key design decisions:
- Uses CLI headless modes via subprocess, NOT Python SDKs
- Each task gets a fresh workspace directory (`/sandbox/workspace-{task_id}`) to prevent cross-task contamination (review finding #6)
- Git credentials via standard credential helpers, never embedded in clone URLs
- Structured JSON output to stdout as the last line

```python
#!/usr/bin/env python3
"""Execute a single task via CLI headless mode.

Each provider is invoked as a subprocess in headless/pipe mode:
  - Claude: claude -p --output-format stream-json
  - Codex:  codex exec --json
  - Gemini: gemini -p --output-format stream-json

This avoids importing Python SDKs (codex-sdk-python is not official,
google-genai is a model API not a coding harness). The CLIs edit files,
use tools, and act as real coding agents.

Outputs structured JSON to stdout as the last line.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import time


def run_provider(provider, prompt, cwd=None):
    """Run a coding agent CLI in headless mode and return its output."""
    env = dict(os.environ)

    if provider == "claude":
        cmd = ["claude", "-p", "--output-format", "stream-json"]
        input_data = prompt
    elif provider == "codex":
        cmd = ["codex", "exec", "--json", prompt]
        input_data = None
    elif provider == "gemini":
        cmd = ["gemini", "-p", "--output-format", "stream-json"]
        input_data = prompt
    else:
        raise ValueError(f"Unknown provider: {provider}")

    result = subprocess.run(
        cmd,
        input=input_data,
        capture_output=True,
        text=True,
        cwd=cwd,
        env=env,
        timeout=600,
    )

    if result.returncode != 0:
        return result.stdout + result.stderr, result.returncode

    return result.stdout, 0


def extract_text(provider, raw_output):
    """Extract human-readable text from provider-specific JSON output."""
    if not raw_output.strip():
        return ""

    texts = []
    for line in raw_output.strip().split("\n"):
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
            if obj.get("type") == "assistant" and "content" in obj:
                for block in obj["content"]:
                    if isinstance(block, dict) and block.get("type") == "text":
                        texts.append(block["text"])
            elif "text" in obj:
                texts.append(obj["text"])
            elif "output" in obj:
                texts.append(obj["output"])
        except json.JSONDecodeError:
            texts.append(line)

    return "\n".join(texts) if texts else raw_output


def setup_workspace(repo, task_id):
    """Create an isolated workspace for this task.

    Each task gets its own directory under /sandbox/ to prevent
    cross-task contamination (review finding #6). If the repo was
    previously cloned to a shared cache, we use git worktree for
    speed. Otherwise we do a fresh clone.
    """
    workspace = f"/sandbox/workspace-{task_id}"
    cache = "/sandbox/repo-cache"

    if os.path.exists(workspace):
        shutil.rmtree(workspace)

    url = f"https://{repo}"

    if os.path.exists(os.path.join(cache, ".git")):
        subprocess.run(["git", "fetch", "--all"], cwd=cache, check=True)
        subprocess.run(
            ["git", "worktree", "add", workspace, "-b", f"task-{task_id}", "origin/main"],
            cwd=cache,
            check=True,
        )
    else:
        os.makedirs(cache, exist_ok=True)
        subprocess.run(["git", "clone", "--bare", url, cache], check=True)
        subprocess.run(
            ["git", "worktree", "add", workspace, "-b", f"task-{task_id}", "origin/main"],
            cwd=cache,
            check=True,
        )

    return workspace


def cleanup_workspace(task_id):
    """Remove the worktree after the task completes."""
    workspace = f"/sandbox/workspace-{task_id}"
    cache = "/sandbox/repo-cache"
    if os.path.exists(os.path.join(cache, ".git")):
        subprocess.run(
            ["git", "worktree", "remove", "--force", workspace],
            cwd=cache,
            check=False,
        )
    elif os.path.exists(workspace):
        shutil.rmtree(workspace, ignore_errors=True)


def commit_and_push(task_id, prompt, workspace):
    branch = f"task-{task_id}"
    subprocess.run(["git", "add", "-A"], cwd=workspace, check=True)
    msg = f"task-{task_id}: {prompt[:50]}"
    subprocess.run(["git", "commit", "-m", msg, "--allow-empty"], cwd=workspace, check=False)
    subprocess.run(["git", "push", "origin", branch], cwd=workspace, check=True)


def get_sha(workspace):
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=workspace, capture_output=True, text=True
    )
    return result.stdout.strip()


def count_files_changed(workspace):
    result = subprocess.run(
        ["git", "diff", "--name-only", "HEAD~1"], cwd=workspace, capture_output=True, text=True
    )
    return len([f for f in result.stdout.strip().split("\n") if f])


def main():
    parser = argparse.ArgumentParser(description="Execute a single agent task")
    parser.add_argument("--provider", default="claude", choices=["claude", "codex", "gemini"])
    parser.add_argument("--prompt", required=True)
    parser.add_argument("--repo", default="")
    parser.add_argument("--task-id", default="unknown")
    args = parser.parse_args()

    start = time.time()
    cwd = None

    if args.repo:
        cwd = setup_workspace(args.repo, args.task_id)

    try:
        raw_output, exit_code = run_provider(args.provider, args.prompt, cwd)
        result_text = extract_text(args.provider, raw_output)
        duration = int(time.time() - start)

        if exit_code != 0:
            output = {
                "type": "text",
                "summary": f"Task failed (exit {exit_code})",
                "full_text": result_text,
                "duration_seconds": duration,
                "exit_code": exit_code,
            }
            print(json.dumps(output))
            sys.exit(1)

        if args.repo:
            commit_and_push(args.task_id, args.prompt, cwd)
            sha = get_sha(cwd)
            files = count_files_changed(cwd)
            output = {
                "type": "branch",
                "branch": f"task-{args.task_id}",
                "commit": sha,
                "files_changed": files,
                "summary": result_text[:200] if result_text else "",
                "duration_seconds": duration,
            }
        else:
            output = {
                "type": "text",
                "full_text": result_text,
                "summary": result_text[:200] if result_text else "",
                "duration_seconds": duration,
            }

        print(json.dumps(output))
    finally:
        if args.repo:
            cleanup_workspace(args.task_id)


if __name__ == "__main__":
    main()
```

- [ ] **Step 5: Build and verify locally**

Run: `docker buildx build --platform linux/amd64 -t agent-worker:test -f runtime/agents/universal/Dockerfile runtime/agents/universal/ --load 2>&1 | tail -5`
Expected: Build succeeds

Run: `docker inspect agent-worker:test --format '{{.Architecture}}'`
Expected: `amd64`

- [ ] **Step 6: Verify CLIs are installed and OpenShell contract is met**

Run: `docker run --rm --entrypoint sh agent-worker:test -lc 'command -v claude && command -v codex && id sandbox && command -v ip && command -v nft'`
Expected: All paths found, sandbox user exists

- [ ] **Step 7: Verify no Python SDKs installed (intentional)**

Run: `docker run --rm --entrypoint sh agent-worker:test -lc 'python3 -c "import claude_code_sdk" 2>&1 || echo "OK: no Python SDKs, using CLI headless mode"'`
Expected: Import fails, confirming we use CLIs not SDKs

**REVIEW CHECKPOINT: Pause here. Three files created.**

---

### Task 2: GitHub Action for image builds

Validate-before-publish: build locally, smoke test, push only if tests pass. Version pinning opens a PR (review finding #4).

**Files:**
- Create: `.github/workflows/agent-images.yaml`

- [ ] **Step 1: Create the workflow**

```yaml
name: Build Agent Images

on:
  push:
    branches: [main]
    paths:
      - 'runtime/agents/universal/**'
  workflow_dispatch:
    inputs:
      claude_version:
        description: 'Claude Code version (blank = keep pinned)'
        required: false
      codex_version:
        description: 'Codex version (blank = keep pinned)'
        required: false
  schedule:
    - cron: '0 6 * * 1'  # Weekly Monday 6am UTC

jobs:
  build-and-push:
    runs-on: ubuntu-latest
    permissions:
      contents: write
      packages: write
      pull-requests: write
    steps:
      - uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Read pinned versions
        id: versions
        run: |
          CLAUDE=$(grep 'CLAUDE_CODE_VERSION=' runtime/agents/universal/Dockerfile | head -1 | cut -d= -f2)
          CODEX=$(grep 'CODEX_VERSION=' runtime/agents/universal/Dockerfile | head -1 | cut -d= -f2)
          echo "claude_pinned=$CLAUDE" >> "$GITHUB_OUTPUT"
          echo "codex_pinned=$CODEX" >> "$GITHUB_OUTPUT"

      - name: Resolve versions
        id: resolve
        run: |
          if [ "${{ github.event_name }}" = "schedule" ]; then
            CLAUDE_VER="${{ inputs.claude_version || 'latest' }}"
            CODEX_VER="${{ inputs.codex_version || 'latest' }}"
          else
            CLAUDE_VER="${{ inputs.claude_version || steps.versions.outputs.claude_pinned }}"
            CODEX_VER="${{ inputs.codex_version || steps.versions.outputs.codex_pinned }}"
          fi
          echo "claude=$CLAUDE_VER" >> "$GITHUB_OUTPUT"
          echo "codex=$CODEX_VER" >> "$GITHUB_OUTPUT"

      # BUILD LOCALLY FIRST (no push)
      - name: Build image (local only)
        uses: docker/build-push-action@v6
        with:
          context: runtime/agents/universal
          file: runtime/agents/universal/Dockerfile
          platforms: linux/amd64
          load: true
          push: false
          tags: agent-worker:ci-test
          build-args: |
            CLAUDE_CODE_VERSION=${{ steps.resolve.outputs.claude }}
            CODEX_VERSION=${{ steps.resolve.outputs.codex }}

      # VALIDATE before pushing
      - name: Smoke test (must pass before push)
        run: |
          echo "=== Checking CLIs ==="
          docker run --rm --entrypoint sh agent-worker:ci-test -lc \
            'command -v claude && command -v codex && echo "CLIs OK"'

          echo "=== Checking sandbox user ==="
          docker run --rm --entrypoint sh agent-worker:ci-test -lc \
            'id sandbox && echo "Sandbox user OK"'

          echo "=== Checking run_task.py syntax ==="
          docker run --rm --entrypoint sh agent-worker:ci-test -lc \
            'python3 -m py_compile /opt/agent/run_task.py && echo "Python OK"'

          echo "=== Checking entrypoint ==="
          docker run --rm --entrypoint sh agent-worker:ci-test -lc \
            'test -x /opt/entrypoint.sh && echo "Entrypoint OK"'

      # PUSH only after validation passes
      - name: Login to Quay.io
        uses: docker/login-action@v3
        with:
          registry: quay.io
          username: ${{ secrets.QUAY_USERNAME }}
          password: ${{ secrets.QUAY_PASSWORD }}

      - name: Tag and push (immutable version first, then latest)
        run: |
          docker tag agent-worker:ci-test quay.io/azaalouk/agent-worker:build-${{ github.run_number }}
          docker push quay.io/azaalouk/agent-worker:build-${{ github.run_number }}
          docker tag agent-worker:ci-test quay.io/azaalouk/agent-worker:latest
          docker push quay.io/azaalouk/agent-worker:latest

      # VERSION PINNING: open a PR instead of committing to main (review finding #4)
      - name: Pin resolved versions via PR (scheduled only)
        if: github.event_name == 'schedule'
        run: |
          CLAUDE_ACTUAL=$(docker run --rm --entrypoint sh agent-worker:ci-test -lc 'claude --version 2>/dev/null || echo unknown')
          CODEX_ACTUAL=$(docker run --rm --entrypoint sh agent-worker:ci-test -lc 'codex --version 2>/dev/null || echo unknown')
          echo "Resolved: Claude=$CLAUDE_ACTUAL Codex=$CODEX_ACTUAL"

          sed -i "s/CLAUDE_CODE_VERSION=.*/CLAUDE_CODE_VERSION=${CLAUDE_ACTUAL}/" runtime/agents/universal/Dockerfile
          sed -i "s/CODEX_VERSION=.*/CODEX_VERSION=${CODEX_ACTUAL}/" runtime/agents/universal/Dockerfile

          if git diff --quiet; then
            echo "No version changes"
            exit 0
          fi

          BRANCH="chore/pin-agent-versions-$(date +%Y%m%d)"
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"
          git checkout -b "$BRANCH"
          git add runtime/agents/universal/Dockerfile
          git commit -m "chore: pin agent CLI versions (Claude=$CLAUDE_ACTUAL, Codex=$CODEX_ACTUAL)"
          git push origin "$BRANCH"
          gh pr create \
            --title "chore: pin agent CLI versions (Claude=$CLAUDE_ACTUAL, Codex=$CODEX_ACTUAL)" \
            --body "Weekly CI resolved latest versions and smoke-tested them. This PR pins the exact versions." \
            --base main \
            --head "$BRANCH"
        env:
          GH_TOKEN: ${{ github.token }}
```

**REVIEW CHECKPOINT: Pause here. One file created.**

---

### Task 3: `aimux status` command

Real connectivity checks, not just config presence. Backed by `internal/healthcheck/` package.

**Files:**
- Create: `internal/healthcheck/checker.go`
- Create: `internal/healthcheck/checker_test.go`
- Create: `cmd/aimux/cmd/status.go`
- Create: `cmd/aimux/cmd/status_test.go`
- Modify: `cmd/aimux/cmd/register.go`

- [ ] **Step 1: Write the failing test for healthcheck package**

```go
package healthcheck

import "testing"

func TestLocalChecks_ContainsExpectedNames(t *testing.T) {
	results := CheckLocal()
	names := make(map[string]bool)
	for _, r := range results {
		names[r.Name] = true
	}
	for _, expected := range []string{"tmux", "claude", "codex", "gemini"} {
		if !names[expected] {
			t.Errorf("missing check for %q", expected)
		}
	}
}

func TestCheckRemote_OpenShell_ReturnsResults(t *testing.T) {
	results := CheckRemote(RemoteConfig{Backend: "openshell", Gateway: "http://fake:8090"})
	if len(results) == 0 {
		t.Error("expected at least one result for openshell backend")
	}
	var gatewayCheck *CheckResult
	for i := range results {
		if results[i].Name == "Gateway" {
			gatewayCheck = &results[i]
			break
		}
	}
	if gatewayCheck == nil {
		t.Error("missing Gateway check")
	} else if gatewayCheck.Status == StatusOK {
		t.Error("Gateway should not be OK with a fake endpoint")
	}
}

func TestCheckRemote_K8s_RedisEmpty(t *testing.T) {
	results := CheckRemote(RemoteConfig{Backend: "k8s", RedisURL: ""})
	var redisCheck *CheckResult
	for i := range results {
		if results[i].Name == "Redis" {
			redisCheck = &results[i]
			break
		}
	}
	if redisCheck == nil {
		t.Fatal("missing Redis check")
	}
	if redisCheck.Status != StatusFail {
		t.Errorf("expected FAIL for empty Redis URL, got %s", redisCheck.Status)
	}
}

func TestCheckRemote_UnknownBackend(t *testing.T) {
	results := CheckRemote(RemoteConfig{Backend: "unknown"})
	if results != nil {
		t.Errorf("expected nil for unknown backend, got %d results", len(results))
	}
}

func TestCheckRemote_EmptyBackend(t *testing.T) {
	results := CheckRemote(RemoteConfig{})
	if results != nil {
		t.Errorf("expected nil for empty backend, got %d results", len(results))
	}
}

func TestMaskURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"redis://:secret@host:6379", "redis://:***@host:6379"},
		{"redis://host:6379", "redis://host:6379"},
		{"not-a-url", "not-a-url"},
	}
	for _, tt := range tests {
		got := maskURL(tt.input)
		if got != tt.want {
			t.Errorf("maskURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/healthcheck/ -timeout 30s -v`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Create checker.go**

```go
package healthcheck

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Status int

const (
	StatusOK   Status = iota
	StatusWarn
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	}
	return "UNKNOWN"
}

type CheckResult struct {
	Name    string
	Status  Status
	Message string
	Fix     string
}

func CheckLocal() []CheckResult {
	var results []CheckResult

	if path, err := exec.LookPath("tmux"); err == nil {
		out, _ := exec.Command("tmux", "-V").Output()
		results = append(results, CheckResult{
			Name:    "tmux",
			Status:  StatusOK,
			Message: fmt.Sprintf("%s (%s)", path, strings.TrimSpace(string(out))),
		})
	} else {
		results = append(results, CheckResult{
			Name:    "tmux",
			Status:  StatusFail,
			Message: "not found in PATH",
			Fix:     "Install tmux: brew install tmux",
		})
	}

	for _, name := range []string{"claude", "codex", "gemini"} {
		if path, err := exec.LookPath(name); err == nil {
			results = append(results, CheckResult{
				Name:    name,
				Status:  StatusOK,
				Message: path,
			})
		} else {
			results = append(results, CheckResult{
				Name:    name,
				Status:  StatusWarn,
				Message: "not found in PATH",
			})
		}
	}

	return results
}

type RemoteConfig struct {
	Backend  string
	Gateway  string
	RedisURL string
}

func CheckRemote(cfg RemoteConfig) []CheckResult {
	switch cfg.Backend {
	case "openshell":
		return checkOpenShell(cfg)
	case "k8s":
		return checkK8s(cfg)
	default:
		return nil
	}
}

func checkOpenShell(cfg RemoteConfig) []CheckResult {
	var results []CheckResult

	results = append(results, CheckResult{
		Name:    "Backend",
		Status:  StatusOK,
		Message: "openshell",
	})

	out, err := exec.Command("openshell", "status").CombinedOutput()
	if err != nil {
		results = append(results, CheckResult{
			Name:    "Gateway",
			Status:  StatusFail,
			Message: fmt.Sprintf("cannot reach gateway: %s", strings.TrimSpace(string(out))),
			Fix:     "Start the gateway: openshell gateway start",
		})
	} else {
		msg := cfg.Gateway
		if msg == "" {
			msg = "connected"
		}
		results = append(results, CheckResult{
			Name:    "Gateway",
			Status:  StatusOK,
			Message: msg,
		})
	}

	return results
}

func checkK8s(cfg RemoteConfig) []CheckResult {
	var results []CheckResult

	results = append(results, CheckResult{
		Name:    "Backend",
		Status:  StatusOK,
		Message: "k8s",
	})

	if cfg.RedisURL == "" {
		results = append(results, CheckResult{
			Name:    "Redis",
			Status:  StatusFail,
			Message: "redis_url not configured",
			Fix:     "Set kubernetes.redis_url in ~/.aimux/config.yaml",
		})
	} else {
		parsed, err := url.Parse(cfg.RedisURL)
		if err != nil {
			results = append(results, CheckResult{
				Name:    "Redis",
				Status:  StatusFail,
				Message: fmt.Sprintf("invalid URL: %v", err),
			})
		} else {
			host := parsed.Host
			if host == "" {
				host = "localhost:6379"
			}
			conn, err := net.DialTimeout("tcp", host, 3*time.Second)
			if err != nil {
				results = append(results, CheckResult{
					Name:    "Redis",
					Status:  StatusFail,
					Message: fmt.Sprintf("cannot connect to %s: %v", maskURL(cfg.RedisURL), err),
					Fix:     "Check Redis is running and accessible",
				})
			} else {
				conn.Close()
				results = append(results, CheckResult{
					Name:    "Redis",
					Status:  StatusOK,
					Message: maskURL(cfg.RedisURL),
				})
			}
		}
	}

	return results
}

func CheckMCPRegistered() CheckResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return CheckResult{Name: "MCP registered", Status: StatusWarn, Message: "cannot determine home dir"}
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return CheckResult{
			Name:    "MCP registered",
			Status:  StatusWarn,
			Message: "settings.json not found",
			Fix:     "Run: aimux mcp register",
		}
	}
	if !strings.Contains(string(data), "aimux-k8s-agents") {
		return CheckResult{
			Name:    "MCP registered",
			Status:  StatusFail,
			Message: "aimux-k8s-agents not found in Claude Code settings",
			Fix:     "Run: aimux mcp register",
		}
	}
	return CheckResult{Name: "MCP registered", Status: StatusOK, Message: "registered"}
}

func maskURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if _, has := parsed.User.Password(); has {
		parsed.User = url.UserPassword(parsed.User.Username(), "***")
	}
	return parsed.String()
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/healthcheck/ -timeout 30s -v`
Expected: PASS

**REVIEW CHECKPOINT: Pause here. Two files created.**

- [ ] **Step 5: Write the cobra command test**

Create `cmd/aimux/cmd/status_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatusCmd_Runs(t *testing.T) {
	cmd := newStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "Local") {
		t.Error("output should contain 'Local' section")
	}
	if !strings.Contains(output, "tmux") {
		t.Error("output should contain tmux check")
	}
}
```

- [ ] **Step 6: Create status.go**

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/healthcheck"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show aimux health status",
		Long:  "Check local prerequisites and remote backend connectivity.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load(config.DefaultPath())
			out := cmd.OutOrStdout()

			fmt.Fprintln(out, "Local")
			for _, r := range healthcheck.CheckLocal() {
				printCheck(out, r)
			}

			remoteCfg := healthcheck.RemoteConfig{
				Backend:  cfg.Remote.Backend,
				Gateway:  cfg.Remote.Gateway,
				RedisURL: cfg.Kubernetes.RedisURL,
			}
			if remoteCfg.Backend != "" {
				fmt.Fprintln(out)
				fmt.Fprintf(out, "Remote (%s)\n", remoteCfg.Backend)
				for _, r := range healthcheck.CheckRemote(remoteCfg) {
					printCheck(out, r)
				}
				printCheck(out, healthcheck.CheckMCPRegistered())
			}

			return nil
		},
	}
}

func printCheck(out interface{ Write([]byte) (int, error) }, r healthcheck.CheckResult) {
	var icon string
	switch r.Status {
	case healthcheck.StatusOK:
		icon = "OK"
	case healthcheck.StatusWarn:
		icon = "WARN"
	case healthcheck.StatusFail:
		icon = "FAIL"
	}
	fmt.Fprintf(out, "  %-18s %s", r.Name, icon)
	if r.Message != "" {
		fmt.Fprintf(out, " %s", r.Message)
	}
	fmt.Fprintln(out)
	if r.Fix != "" {
		fmt.Fprintf(out, "  %-18s Fix: %s\n", "", r.Fix)
	}
}
```

- [ ] **Step 7: Register in register.go**

Add `rootCmd.AddCommand(newStatusCmd())` in `RegisterAll()`.

- [ ] **Step 8: Run tests and build**

Run: `go test ./internal/healthcheck/ ./cmd/aimux/cmd/ -timeout 30s -v && go build ./cmd/aimux/`
Expected: All pass, binary compiles

- [ ] **Step 9: Verify**

Run: `./aimux status`
Expected: Shows local checks with OK/WARN for each provider

**REVIEW CHECKPOINT: Pause here.**
