# Remove Gemini Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Completely remove Gemini as a supported provider from all code, configuration, tests, docs, and deploy manifests.

**Architecture:** Gemini is one of three concrete `Provider` implementations alongside Claude and Codex. Removing it means deleting `internal/provider/gemini.go`, removing every registration site, cleaning up config defaults and cost tables, removing K8s deploy manifests, and updating all documentation. No new abstractions needed — this is pure deletion.

**Tech Stack:** Go 1.26, Bubble Tea TUI, React/TypeScript web, Astro docs-site

## Global Constraints

- Branch: create `chore/remove-gemini` from `main`
- All existing tests must pass after each task (`go test ./... -timeout 30s`)
- Frontend build must pass (`npm run build --prefix web`)
- No Gemini string should remain in any `.go`, `.ts`, `.tsx`, `.md`, `.mdx`, `.yaml`, `.yml` file (except historical plan docs under `docs/superpowers/plans/` — leave those as-is)
- Do NOT remove the `Provider` interface or change its shape — Codex must keep working

---

### Task 1: Create branch and delete the Gemini provider package

**Files:**
- Delete: `internal/provider/gemini.go`
- Modify: `internal/provider/provider.go` (remove any Gemini-specific mention in comments)

**Interfaces:**
- Produces: a repo that no longer compiles (Gemini is still registered in main.go) — intentional, fixed in Task 2

- [ ] **Step 1: Create the branch**

```bash
git checkout main && git pull
git checkout -b chore/remove-gemini
```

- [ ] **Step 2: Delete the Gemini provider file**

```bash
git rm internal/provider/gemini.go
```

- [ ] **Step 3: Remove Gemini from provider.go comments (if any)**

Read `internal/provider/provider.go`. Remove any line that says `// Gemini` or references `gemini.go`.

- [ ] **Step 4: Verify it no longer compiles (expected)**

```bash
go build ./... 2>&1 | grep -c "Gemini" # should be > 0 — expected failures
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "remove: delete internal/provider/gemini.go"
```

---

### Task 2: Remove Gemini registrations from main.go and cmd/

**Files:**
- Modify: `cmd/aimux/main.go`
- Modify: `cmd/aimux/cmd/agents.go`
- Modify: `cmd/aimux/cmd/profile.go`

**Interfaces:**
- Consumes: Task 1 (gemini.go deleted)
- Produces: compilable binary with only Claude + Codex

- [ ] **Step 1: Edit cmd/aimux/main.go**

Find and remove every line referencing `provider.Gemini{}` and `"gemini"`. There are 7 hits across these patterns:
- `&provider.Gemini{}` in provider slices → remove the entry
- `"gemini": (&provider.Gemini{}).ParseTrace` in the trace parser map → remove the entry
- `case "claude", "gemini":` → change to `case "claude":`
- `Providers: []string{"claude", "codex", "gemini"}` → remove `"gemini"`

After editing, `go build ./cmd/aimux` should compile.

- [ ] **Step 2: Edit cmd/aimux/cmd/agents.go**

Find `Long: "Discover and list all running AI coding agent sessions (Claude, Codex, Gemini)"` → change to `"(Claude, Codex)"`.

- [ ] **Step 3: Edit cmd/aimux/cmd/profile.go**

Find `"Provider (claude, codex, gemini)"` → change to `"Provider (claude, codex)"`.

- [ ] **Step 4: Verify build passes**

```bash
go build ./... 2>&1
```

Expected: zero errors.

- [ ] **Step 5: Run tests**

```bash
go test ./cmd/... -timeout 30s
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/aimux/main.go cmd/aimux/cmd/agents.go cmd/aimux/cmd/profile.go
git commit -m "remove: deregister Gemini from main.go and cmd/"
```

---

### Task 3: Remove Gemini from config, cost tracker, and discovery

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/cost/tracker.go`
- Modify: `internal/discovery/tmux.go`
- Modify: `internal/spawn/spawn.go`
- Modify: `internal/terminal/tmux.go`

**Interfaces:**
- Consumes: Task 2 (binary compiles)

- [ ] **Step 1: Edit internal/config/config.go**

Find `"gemini": {Enabled: true}` in the default providers map → remove that entry.

- [ ] **Step 2: Edit internal/cost/tracker.go**

Remove all Gemini model pricing entries:
- `"gemini-2.5-pro"`, `"gemini-2.5-flash"`, `"gemini-3-pro"`, `"gemini-3.1-flash"` blocks (and any others)

- [ ] **Step 3: Edit internal/discovery/tmux.go**

Find `"aimux-gemini-"` in `MatchTmuxSession` targets list → remove that entry.

- [ ] **Step 4: Edit internal/spawn/spawn.go**

Find any `case "gemini":` or `"gemini"` switch arms in `TmuxSessionName` or `LaunchInTmux` → remove.

- [ ] **Step 5: Edit internal/terminal/tmux.go**

Find any Gemini-specific tmux session handling → remove.

- [ ] **Step 6: Verify and test**

```bash
go build ./...
go test ./internal/config/ ./internal/cost/ ./internal/discovery/ ./internal/spawn/ ./internal/terminal/ -timeout 30s
```

Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/cost/tracker.go \
        internal/discovery/tmux.go internal/spawn/spawn.go internal/terminal/tmux.go
git commit -m "remove: strip Gemini from config, cost, discovery, spawn, terminal"
```

---

### Task 4: Remove Gemini from TUI frontend

**Files:**
- Modify: `internal/frontend/tui/app.go`
- Modify: `internal/frontend/tui/views/newpicker.go`
- Modify: `internal/frontend/tui/views/help.go`
- Modify: `internal/frontend/tui/views/logs.go`

**Interfaces:**
- Consumes: Task 3

- [ ] **Step 1: Edit internal/frontend/tui/app.go**

Find all 10 Gemini references. Remove:
- `"gemini"` from any provider list/slice
- `&provider.Gemini{}` if still present (should be gone from Task 2 but verify)
- Any `case "gemini":` switch arms
- Any Gemini-specific spawning or session handling

- [ ] **Step 2: Edit internal/frontend/tui/views/newpicker.go**

Remove `"gemini"` from the provider options list (4 hits).

- [ ] **Step 3: Edit internal/frontend/tui/views/help.go**

Remove Gemini from any provider list in help text (1 hit).

- [ ] **Step 4: Edit internal/frontend/tui/views/logs.go**

Remove Gemini from any provider reference (1 hit).

- [ ] **Step 5: Build and test**

```bash
go build ./...
go test ./internal/frontend/tui/... -timeout 30s
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/frontend/tui/
git commit -m "remove: strip Gemini from TUI frontend"
```

---

### Task 5: Remove Gemini from web frontend and OTEL packages

**Files:**
- Modify: `internal/frontend/web/terminal.go`
- Modify: `internal/frontend/web/handlers.go`
- Modify: `internal/otel/receiver.go`
- Modify: `internal/otel/converter.go`
- Modify: `internal/otel/exporter.go`
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/insight/insight.go`
- Modify: `internal/history/titler.go`
- Modify: `internal/history/roi.go`
- Modify: `internal/provider/helpers.go`
- Modify: `internal/provider/k8s.go`

**Interfaces:**
- Consumes: Task 4

- [ ] **Step 1: Remove Gemini from terminal.go**

Find Gemini in the provider allowlist (where we validate `provider` query param). Remove `"gemini"` from the allowed values. The switch/case should only allow `"claude"` and `"codex"`.

- [ ] **Step 2: Remove Gemini from handlers.go**

Find and remove any `"gemini"` in provider lists or switch cases (1 hit).

- [ ] **Step 3: Remove Gemini from OTEL packages**

In `receiver.go`, `converter.go`, `exporter.go`: remove any `"gemini"` string comparisons or provider-specific handling.

- [ ] **Step 4: Remove Gemini from mcpserver/server.go**

Find 2 Gemini references — remove from any provider allowlists.

- [ ] **Step 5: Remove Gemini from insight, history packages**

In `insight/insight.go` (14 hits), `history/titler.go`, `history/roi.go`: remove Gemini model name lists, Gemini process discovery paths (e.g. `filepath.Join(home, ".gemini")`), Gemini-specific cost/token logic.

- [ ] **Step 6: Remove Gemini from provider/helpers.go and provider/k8s.go**

Remove any Gemini-specific helper logic (1 hit each).

- [ ] **Step 7: Build and test**

```bash
go build ./...
go test ./internal/frontend/web/ ./internal/otel/ ./internal/mcpserver/ \
        ./internal/insight/ ./internal/history/ ./internal/provider/ -timeout 30s
```

Expected: all PASS

- [ ] **Step 8: Commit**

```bash
git add internal/frontend/web/ internal/otel/ internal/mcpserver/ \
        internal/insight/ internal/history/ internal/provider/
git commit -m "remove: strip Gemini from web, OTEL, mcpserver, insight, history, provider"
```

---

### Task 6: Remove Gemini from web React/TypeScript frontend

**Files:**
- Modify: `web/src/components/LaunchDialog.tsx`
- Modify: `web/src/components/AgentCard.tsx`
- Modify: `web/src/components/CardGrid.tsx`
- Modify: `web/src/components/FilterBar.tsx`
- Modify: `web/src/App.tsx` (if any)

**Interfaces:**
- Consumes: Task 5

- [ ] **Step 1: Edit LaunchDialog.tsx**

Find `['claude', 'codex', 'gemini']` provider array → change to `['claude', 'codex']`.
Find Gemini-specific model list (`['default', 'gemini-2.5-pro', 'gemini-2.5-flash']`) → remove the entire `provider === 'gemini'` branch.
Find Gemini-specific mode list → remove the `provider === 'gemini'` branch.
Find `providerColors.gemini` entry → remove.

- [ ] **Step 2: Edit AgentCard.tsx**

Find any `gemini` in provider color map or badge styling → remove.

- [ ] **Step 3: Edit CardGrid.tsx and FilterBar.tsx**

Find `'gemini'` in provider filter lists → remove.

- [ ] **Step 4: Build the frontend**

```bash
npm run build --prefix web
```

Expected: zero TypeScript errors, clean build.

- [ ] **Step 5: Commit**

```bash
git add web/src/
git commit -m "remove: strip Gemini from React web frontend"
```

---

### Task 7: Remove K8s deploy manifests and update docs

**Files:**
- Delete: `deploy/k8s/agent-gemini-coder.yaml`
- Delete: `deploy/k8s/agent-gemini-researcher.yaml`
- Modify: `deploy/k8s/README.md`
- Delete: `runtime/agents/gemini/` (if exists)
- Modify: `docs-site/src/content/docs/getting-started.mdx`
- Modify: `docs-site/src/content/docs/configuration.mdx`
- Modify: `docs-site/src/content/docs/guides/launch-modes.mdx`
- Modify: `docs-site/src/content/docs/guides/web-dashboard.mdx`
- Modify: `docs-site/src/content/docs/guides/agent-usage.mdx`
- Modify: `docs-site/src/content/docs/guides/cost-tracking.mdx`
- Modify: `docs-site/src/content/docs/guides/tracing.mdx`
- Modify: `docs-site/src/content/docs/guides/mlflow-integration.mdx`
- Modify: `docs-site/src/content/docs/guides/remote-sandboxes.mdx`
- Modify: `docs-site/src/content/docs/advanced/adding-a-provider.mdx`
- Modify: `docs-site/src/content/docs/advanced/k8s-quickstart.mdx`
- Modify: `docs-site/src/content/docs/index.mdx`
- Modify: `docs/remote-agents.md`
- Modify: `docs/DESIGN.md`
- Modify: `docs/adr/0001-provider-interface-design.md`

**Interfaces:**
- Consumes: Tasks 1–6

- [ ] **Step 1: Delete K8s Gemini manifests**

```bash
git rm deploy/k8s/agent-gemini-coder.yaml deploy/k8s/agent-gemini-researcher.yaml
```

Check if `runtime/agents/gemini/` exists: `ls runtime/agents/`. If it does, `git rm -r runtime/agents/gemini/`.

- [ ] **Step 2: Update deploy/k8s/README.md**

Remove the two Gemini rows from the manifests table. Update any "Claude, Codex, Gemini" prose to "Claude, Codex".

- [ ] **Step 3: Update docs-site pages**

For each file listed above, do a global find-and-replace:
- `Claude, Codex, Gemini` → `Claude, Codex`
- `Claude, Codex, or Gemini` → `Claude, Codex`
- `claude, codex, gemini` → `claude, codex`
- `claude, codex, or gemini` → `claude, codex`
- Remove Gemini model rows from cost-tracking.mdx (`gemini-2.5-pro`, `gemini-2.5-flash`, etc.)
- Remove `gemini:` block from configuration.mdx provider config example
- Remove `gemini-2.5-pro` from launch-modes.mdx model axis table
- Remove Gemini-specific code blocks from adding-a-provider.mdx (the `gemini.go` copy instruction)
- Remove Gemini worker image build steps from k8s-quickstart.mdx
- Update provider filter description in web-dashboard.mdx: remove `Gemini`
- Update agent-usage.mdx valid provider list

- [ ] **Step 4: Build docs-site**

```bash
cd docs-site && npm run build
```

Expected: clean build, no broken references.

- [ ] **Step 5: Run full test suite**

```bash
go test ./... -timeout 60s
```

Expected: all PASS. Verify no remaining Gemini string in Go/TS/YAML files:

```bash
grep -rn -i "gemini" --include="*.go" --include="*.ts" --include="*.tsx" \
  --include="*.yaml" --include="*.yml" --include="*.mdx" \
  . | grep -v "node_modules\|docs/superpowers/plans\|go.sum\|\.git"
```

Expected: zero hits.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "remove: delete Gemini K8s manifests, update all docs"
```

---

### Task 8: Open PR, verify CI, merge

- [ ] **Step 1: Push branch**

```bash
git push -u origin chore/remove-gemini
```

- [ ] **Step 2: Open PR**

```bash
gh pr create --title "remove: drop Gemini provider" --body "$(cat <<'EOF'
## Summary

- Removes `internal/provider/gemini.go` and all Gemini registrations
- Cleans config defaults, cost table, discovery, spawn, OTEL, insight, history
- Removes TUI and web frontend Gemini options
- Deletes K8s deploy manifests for Gemini agents
- Updates all docs-site pages and internal docs

## Test plan
- [ ] `go test ./... -timeout 60s` — all pass
- [ ] `npm run build --prefix web` — clean
- [ ] `cd docs-site && npm run build` — clean
- [ ] Zero `gemini` hits: `grep -ri gemini --include="*.go" --include="*.ts" --include="*.tsx" . | grep -v node_modules`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Wait for CI to pass, then merge**

```bash
gh pr merge --squash --repo zanetworker/aimux
git checkout main && git pull
```
