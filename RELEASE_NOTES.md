## aimux v0.13.0

### Highlights

**Session search, rebuilt.** `aimux sessions` uses one ranked SQLite index for the CLI, TUI and web dashboard: BM25 keyword search, optional OpenAI-embedding semantic and hybrid ranking, exact "quoted phrase" search, and match snippets. It replaces the ripgrep backend.

**Only your sessions.** Automated sessions (`claude -p`, cron jobs, SDK runs) are hidden in every list by default; `^a` or `--automated` shows them.

**Sessions over MCP.** `aimux mcp serve` exposes session search, transcript reading and resume as MCP tools; remote agents move behind `--agents`.

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
