# aimux

Multi-agent TUI dashboard. See `.claude/CLAUDE.md` for full project guide.

## Quick Commands

```bash
go build -o aimux ./cmd/aimux
go test ./... -timeout 30s
```

## Single-File Verification

```bash
gofmt internal/agent/agent.go
go vet ./internal/agent/
go test ./internal/agent/ -timeout 30s -run TestName
```

## Pattern References

- New provider: follow the pattern in `internal/provider/claude.go`
- New TUI view: use `internal/tui/views/agents.go` as a template
- New API endpoint: based on `internal/frontend/web/` existing handlers
- New keybinding: see `internal/tui/app.go` for examples

## Learned rules (from feedback)


### Controller first

Always implement "Controller First, Frontend Second" per the aimux CLAUDE.md. Every user-visible operation goes in controller/ as a pure function first, then TUI/Web/CLI wire it as thin adapters.

**How to apply:** Before writing any feature code in a frontend file (app.go, handlers.go, cmd/*.go), first create the controller function in internal/controller/ with its own tests. Then wire it from each frontend. Check the operations table in CLAUDE.md to see if a controller function already exists.

### Durable worktrees

Create git worktrees under `~/go/src/github.com/zanetworker/aimux-worktrees/<topic>`, never under `/tmp` or `/private/tmp`. Remove a worktree only once its work is merged or explicitly abandoned, and only after checking `git status` shows nothing uncommitted.

**How to apply:** when a skill or plan suggests a temp worktree (e.g. superpowers:using-git-worktrees), override the path. Commit or copy uncommitted work out before removing any worktree. Related: project-backend-architecture

### No mock tests

No mock tests. Always test against real implementations.

**How to apply:** Use real Local environments in dispatcher tests (actually run `sh -c`), real SQLite databases (not in-memory fakes), real subprocess I/O. The only exception is external services that cost money per call (e.g., Claude API) — use a real test binary (like mock-sdk) that implements the protocol faithfully, but never a Go interface mock that just records calls. Related: always-write-real-tests

### Rebuild after merge

After merging anything to aimux `main`, rebuild and install the local `aimux` binary in the same turn, without being asked.

**How to apply:**
- The binary on PATH is `~/go/bin/aimux` (first in PATH; `/usr/local/bin` and `/opt/homebrew/bin` copies are shadowed, so `make install` alone does NOT update what runs).
- Build from `origin/main`, not whatever branch is checked out (another session may have a feature branch checked out). A detached worktree works; copy `internal/frontend/web/dist` into it first (gitignored, embed fails without it).
- `go build -ldflags "-X main.version=$(git describe --tags --always)"`, then `mv` (not `cp`) over `~/go/bin/aimux`: a long-running `aimux collect` uses that file, and overwriting in place can get it killed on macOS.
- **First check what is installed:** run `~/go/bin/aimux version`. If it is ahead of `main` (a feature-branch build, e.g. `feat/clients` with the proxy install and phone server), do NOT replace it: tell the user and the session that owns that branch instead. On 2026-11-04 a main rebuild after PR #83 replaced the `feat/clients` build, which removed `mcp proxy install` and the phone server features that another session depended on.
- Verify with `aimux version` showing the merge commit.
