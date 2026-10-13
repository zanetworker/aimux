## aimux v0.13.0

### Highlights

**Session search, rebuilt.** `aimux sessions` uses one ranked SQLite index for the CLI, TUI and web dashboard: BM25 keyword search, optional OpenAI-embedding semantic and hybrid ranking, exact "quoted phrase" search, and match snippets. It replaces the ripgrep backend.

**Only your sessions.** Automated sessions (`claude -p`, cron jobs, SDK runs) are hidden in every list by default; `^a` or `--automated` shows them.

**Sessions over MCP.** `aimux mcp serve` exposes session search, transcript reading and resume as MCP tools; remote agents move behind `--agents`.

### Breaking
- Gemini provider removed (#41)

### Session picker
- `^l` / `--live`: only sessions running in a terminal now
- Click a row to jump to its iTerm pane or resume it
- `^y` copies `cd '<path>' && claude --resume '<id>'`
- `^s` cycles keyword and semantic modes; `^/` toggles the preview
- Rows and header size themselves to the terminal; preview sits beside the list at 140+ columns, below it otherwise

### Fixes and speed
- About half the search latency; persistent index refreshes incrementally
- Session archive dir indexed so sessions outlive Claude Code's 30-day cleanup
- Concurrent MCP calls no longer lock SQLite

### Remote agents
- Run agents on OpenShell and Kubernetes backends; the web dashboard can launch, kill and clean up sandboxes like local agents (#40)
- Web and TUI show gateway health, sandbox badges and kill feedback; launch errors appear in the dialog instead of failing silently (#42)

### Sessions and launch
- Sessions view and CLI default to the current directory; `A` (TUI) or `--all` (CLI) shows every project
- `default_mode` config sets the permission mode for spawned and resumed sessions; `Ctrl+b` toggles bypass/default in the live session view

### Under the hood
- Platform refactor: Provider / Environment / Coordination split, SDK mode design (#48)
- Dependency updates (OpenTelemetry, client-go, protobuf, go-redis, mcp-go)
