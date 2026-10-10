# Sessions MCP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `aimux mcp serve` a session-first MCP server: session search and resume tools from aimux's search index are always on, and the remote-agent tools become an opt-in group. The Python session-search-mcp can then be retired, and a missing cluster can no longer take the server down.

**Architecture:** `aimux mcp serve` builds one mcp-go server from tool groups. A new `internal/sessionmcp` package registers five session tools (always on); the existing `internal/mcpserver` tools register only with `--agents`, and a failure to build them goes to stderr instead of stopping the server. Later groups (eval, MLflow) plug in the same way. Every session tool goes through `search.Service`, which refreshes the index (changed files only) before reading, so there is no ingest step and live sessions are always current. Transcript text comes from the index's per-exchange `prose`, not a second JSONL parser.

**Tech Stack:** Go, `github.com/mark3labs/mcp-go v0.57.0`, cobra, SQLite FTS5 index in `internal/search`.

**Spec:** No separate doc. Requirements below were decided on 2026-10-09/10. Behavior to port: `zanetworker/session-search-mcp` `src/session_search/server.py` at commit `8d4ae4d`.

## Requirements

1. `aimux mcp serve` with no flags serves the session tools only and needs no Kubernetes, Redis or OpenShell. `--agents` adds the 11 remote-agent tools with today's flags and config; if building them fails, the session tools still serve and the error goes to stderr. `internal/sessionmcp` must not import `internal/mcpserver`, `k8s.io/...` or Redis.
2. Tools: `search_sessions`, `get_session`, `list_sessions`, `continue_session`, `create_virtual_session`. `ingest` and `index_live_session` are dropped: every call refreshes the index.
3. Session IDs accept a full UUID or a unique prefix. An ambiguous prefix returns the candidates, never an arbitrary pick.
4. Automated sessions are hidden from search and list unless `include_automated: true`. An explicit ID always resolves, automated or not.
5. `continue_session` output includes `cd <cwd> && claude --resume <full-id>` and is capped at 100,000 characters, keeping the newest exchanges.
6. Tool descriptions keep the routing guidance from the Python server: a bare pasted UUID (with or without "resume this session") goes to `continue_session`, and the agent must never build `~/.claude/projects/...jsonl` paths itself.

## Global Constraints

- No new module dependencies.
- stdout carries only MCP JSON-RPC. Service notes go to stderr (`search.DefaultService(os.Stderr)`).
- Output caps: `maxOutputChars = 100_000`; each exchange's prose is cut to `maxExchangeChars = 20_000` with a `…[truncated]` marker.
- Virtual sessions: a session with at most `fullExchanges = 20` exchanges is shown whole; longer ones show the last `num_exchanges` (default 10).
- Tests: `go test ./internal/search/ ./internal/sessionmcp/ ./internal/mcpserver/ ./cmd/aimux/cmd/`; the repo pre-commit lint hook must pass.

## Review Focus

1. Anything written to stdout besides JSON-RPC (a note, a log line) breaks the client. Expect: with no `OPENAI_API_KEY`, stdout holds only valid JSON-RPC and the keyword-only note appears on stderr. Pinned in Task 5.
2. A short prefix such as `7` matching several sessions. Expect an error listing the candidates (ID, cwd, title), not the first match. Pinned in Task 1.
3. A session file created after the index was last refreshed, such as a live session. Expect `get_session` to find it on the first call. Pinned in Task 2.
4. A 900-exchange session with one 60k-character exchange. Expect output of at most 100k characters, the newest exchanges kept, a "last N of M exchanges" header, and the giant exchange truncated rather than dropping everything else. Pinned in Task 3.
5. An automated session requested by explicit ID. Expect it returned with `automated` shown in the header, even though search and list hide it. Pinned in Task 4.

---

### Task 1: Resolve IDs and read exchanges from the index

**Files:**
- Modify: `internal/search/browse.go`
- Test: `internal/search/browse_test.go`

**Interfaces:**
- Produces:
  - `type Exchange struct { Seq int; Prompt string; Prose string }`
  - `func (ix *Index) Resolve(idOrPrefix string) (string, error)`: exact ID match first, then a unique `id LIKE prefix%`. Empty input or no match returns an error containing `no session matches`. More than one match returns `*AmbiguousError`.
  - `type AmbiguousError struct { Prefix string; Candidates []Result }` with `Error()` listing each candidate's ID, CWD and Title on its own line (at most 10).
  - `func (ix *Index) Exchanges(sessionID string) ([]Exchange, error)`: every chunk of the session ordered by `CAST(seq AS INTEGER)`.

- [ ] **Step 1: Write the failing tests** (they reuse `newProjects`/`put` from `index_test.go`)
  - `TestResolve_ExactAndPrefix`: after `ix.Update(root, DefaultExtractOpts())`, `Resolve(sidDeck)` and `Resolve("bbbbbbbb")` both return `sidDeck`.
  - `TestResolve_Ambiguous`: add a session `bbbbbbbb-9999-0000-0000-000000000009` with `put`; `Resolve("bbbbbbbb")` returns an error where `errors.As(err, &amb)` holds and `len(amb.Candidates) == 2`, and `err.Error()` contains both IDs.
  - `TestResolve_NoMatch`: `Resolve("zzzz")` and `Resolve("")` return errors containing `no session matches`.
  - `TestResolve_AutomatedByID`: `Resolve(sidAuto)` returns `sidAuto`.
  - `TestExchanges_Ordered`: `Exchanges(sidDeck)` has length 1, `Seq == 0`, `Prompt` contains `hypotheses proposals`, `Prose` contains `summary slide`.
- [ ] **Step 2: Run** `go test ./internal/search/ -run 'TestResolve|TestExchanges' -v`. Expected: FAIL (undefined `Resolve`, `Exchanges`).
- [ ] **Step 3: Implement** `Resolve`, `AmbiguousError` and `Exchanges` in `browse.go`. Candidates come from `SELECT id, cwd, title FROM sessions WHERE id LIKE ? LIMIT 11`.
- [ ] **Step 4: Run** the same command. Expected: PASS, then `go test ./internal/search/` all PASS.
- [ ] **Step 5: Commit** `feat(search): resolve session ID prefixes and read exchanges`

### Task 2: Service methods that refresh before reading

**Files:**
- Modify: `internal/search/service.go`
- Test: `internal/search/service_test.go`

**Interfaces:**
- Consumes: `Index.Resolve`, `Index.Exchanges`, `Index.Detail`, `Index.Recent` (Task 1 and existing code).
- Produces:
  - `type Transcript struct { Detail Detail; Exchanges []Exchange }`
  - `func (s *Service) Session(ctx context.Context, idOrPrefix string) (Transcript, error)`: open, `s.refresh`, `Resolve`, `Detail(id, 0)`, `Exchanges(id)`. Errors from `Resolve` are returned unwrapped so `errors.As(..., *AmbiguousError)` still works.
  - `func (s *Service) Recent(ctx context.Context, o SearchOpts) ([]Result, error)`: open, `s.refresh`, `Index.Recent(o)`.

- [ ] **Step 1: Write the failing tests**
  - `TestService_SessionByPrefix`: `svc.Session(ctx, "aaaaaaaa")` returns `Detail.SessionID == sidAccounts`, `Detail.CWD == "/Users/me/OpenShell"`, one exchange.
  - `TestService_SessionSeesNewFile`: call `svc.Query` once to build the index, then `put` a new session `eeeeeeee-0000-0000-0000-000000000005` into `svc.ProjectsDir`; `svc.Session(ctx, "eeeeeeee")` succeeds without any other call.
  - `TestService_SessionAmbiguous`: with a second `bbbbbbbb-…` session added, `errors.As(err, &amb)` is true.
  - `TestService_RecentHidesAutomated`: `svc.Recent(ctx, SearchOpts{Limit: 10})` excludes `sidAuto`; with `IncludeAutomated: true` it includes it.
- [ ] **Step 2: Run** `go test ./internal/search/ -run 'TestService_Session|TestService_Recent' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** both methods in `service.go`, following `Query`'s open/close/refresh pattern.
- [ ] **Step 4: Run** `go test ./internal/search/`. Expected: all PASS.
- [ ] **Step 5: Commit** `feat(search): Service.Session and Service.Recent refresh before reading`

### Task 3: Formatting

**Files:**
- Create: `internal/sessionmcp/format.go`
- Test: `internal/sessionmcp/format_test.go`

**Interfaces:**
- Consumes: `search.Result`, `search.Transcript`, `search.Exchange`.
- Produces (all return Markdown strings):
  - `func formatResults(rs []search.Result, mode string, semantic bool) string`: numbered results with ID, title, cwd, age, snippet. The header names the ranking actually used: `semantic`/`hybrid` when `semantic` is true, else `keyword`, plus `(semantic unavailable: set OPENAI_API_KEY)` when `mode` asked for more than keyword. `No sessions found.` for an empty list.
  - `func formatList(rs []search.Result) string`: one line per session: ID, title, cwd, `YYYY-MM-DD HH:MM`.
  - `func formatSession(t search.Transcript, lastN int) string`: header (ID, title, cwd, last modified, exchange count, `automated` flag when set), then exchanges as `**[USER]** prompt` / `**[EXCHANGE n]** prose`. When `lastN > 0`, only the last `lastN`.
  - `func formatContinue(t search.Transcript) string`: header plus `**Native resume:** \`cd <cwd> && claude --resume <id>\``, then the newest exchanges that fit `maxOutputChars`, each cut to `maxExchangeChars`. Heading `## Transcript (last N of M exchanges; use get_session for earlier ones)` when cut, else `## Transcript (M exchanges)`.
  - `func formatVirtual(ts []search.Transcript, numExchanges int) string`: sessions ordered by `Detail.ModTime` ascending, `## Session i of n: <id[:8]> (<cwd>)` sections, full when `len(Exchanges) <= fullExchanges`, else the last `numExchanges`, total capped at `maxOutputChars` by shrinking the longest section first.

- [ ] **Step 1: Write the failing tests** (build `search.Transcript` values in memory)
  - `TestFormatContinue_ResumeLine`: output contains `cd /Users/me/research && claude --resume bbbbbbbb-0000-0000-0000-000000000002`.
  - `TestFormatContinue_CapKeepsNewest`: 900 exchanges of 2,000 chars plus exchange 450 at 60,000 chars; `len(out) <= maxOutputChars+2000`, contains `exchange-899`, not `exchange-0 `, contains `last` and `of 900 exchanges`.
  - `TestFormatContinue_GiantExchangeTruncated`: a single 60,000-char exchange; output contains `…[truncated]` and is under `maxExchangeChars+2000`.
  - `TestFormatSession_LastN`: 5 exchanges, `lastN=2` shows only seq 3 and 4.
  - `TestFormatSession_AutomatedFlag`: `Detail.Automated=true` puts `automated` in the header.
  - `TestFormatVirtual_FullVsTail`: one 3-exchange and one 30-exchange transcript with `numExchanges=10`; the first shows all 3, the second only its last 10; the older `ModTime` comes first.
  - `TestFormatResults_Empty`: returns `No sessions found.`
- [ ] **Step 2: Run** `go test ./internal/sessionmcp/ -v`. Expected: FAIL (package or functions missing).
- [ ] **Step 3: Implement** `format.go` with the constants from Global Constraints.
- [ ] **Step 4: Run** `go test ./internal/sessionmcp/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(sessionmcp): transcript and result formatting`

### Task 4: MCP tools and handlers

**Files:**
- Create: `internal/sessionmcp/server.go`
- Test: `internal/sessionmcp/server_test.go`

**Interfaces:**
- Consumes: `search.Service` (`Query`, `Session`, `Recent`), the formatters from Task 3.
- Produces:
  - `type Server struct { svc *search.Service }`, `func New(svc *search.Service) *Server`
  - `func (s *Server) Register(srv *server.MCPServer)`: adds the five tools to a server the caller builds (Task 5 builds it).
  - Handlers `handleSearch`, `handleGet`, `handleList`, `handleContinue`, `handleVirtual` with signature `func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)`. User-facing failures (no match, ambiguous, bad mode) return `mcp.NewToolResultError(msg)`, never a Go error.
- Tool parameters:
  - `search_sessions`: `query` (required), `mode` enum `hybrid|keyword|semantic` (default `hybrid`), `limit` (default 5), `project_filter` (maps to `QueryOpts.Dir`, a cwd prefix), `include_automated` (default false).
  - `get_session`: `session_id` (required), `last_n`.
  - `list_sessions`: `limit` (default 20), `project_filter`, `days` (default 30, filters on `ModTime`), `include_automated`.
  - `continue_session`: `session_id` (required).
  - `create_virtual_session`: `session_ids` (array, at least 2), `num_exchanges` (default 10).
- Descriptions: port the Python text for each tool, adding Requirement 6 to `get_session` and `continue_session`.

- [ ] **Step 1: Write the failing tests.** A local helper `newFixture(t)` writes three JSONL sessions (two human, one with `"entrypoint":"sdk-cli"`) into a temp projects dir and returns `New(&search.Service{DBPath: tmp/search.db, ProjectsDir: dir, Notes: &buf})`. Call handlers directly with `mcp.CallToolRequest{Params: mcp.CallToolParams{Name: ..., Arguments: map[string]any{...}}}` and read `res.Content[0].(mcp.TextContent).Text`.
  - `TestTools_Registered`: after `s.Register(srv)` on `srv := server.NewMCPServer("test", "0")`, the keys of `srv.ListTools()` (a `map[string]*server.ServerTool` in mcp-go v0.57; if absent, list tools through `client.NewInProcessClient`) are exactly the five tool names.
  - `TestTools_RoutingDescriptions` (Requirement 6): the `continue_session` description contains `UUID` and `claude --resume`; the `get_session` and `continue_session` descriptions both contain `~/.claude/projects`.
  - `TestSearch_KeywordWithoutKey`: no embedder; `search_sessions` with mode omitted returns the matching human session and the text contains `keyword`.
  - `TestSearch_HidesAutomatedUnlessAsked`: the automated session is absent by default and present with `include_automated: true`.
  - `TestGet_AutomatedByExplicitID`: `get_session` with the automated session's full ID succeeds and shows `automated`.
  - `TestGet_AmbiguousIsToolError`: `res.IsError` is true and the text lists both candidate IDs.
  - `TestContinue_HasResumeLine`: text contains `claude --resume <full-id>`.
  - `TestVirtual_NeedsTwo`: one ID gives `IsError`; two IDs give two `## Session` sections.
  - `TestList_DaysFilter`: set one fixture file's mtime 40 days back with `os.Chtimes`; `list_sessions` with default `days` omits it, `days: 60` includes it.
- [ ] **Step 2: Run** `go test ./internal/sessionmcp/ -run 'TestTools|TestSearch|TestGet|TestContinue|TestVirtual|TestList' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `server.go`.
- [ ] **Step 4: Run** `go test ./internal/sessionmcp/ ./internal/search/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(sessionmcp): session search and resume tools over MCP`

### Task 5: Session-first `aimux mcp serve` with an opt-in agents group

**Files:**
- Modify: `internal/mcpserver/server.go:160-182` (split `Serve` into `Register` + `Serve`)
- Modify: `cmd/aimux/cmd/mcp.go` (`newMCPServeCmd`)
- Test: `cmd/aimux/cmd/mcp_test.go`

**Interfaces:**
- Consumes: `sessionmcp.New(...).Register`, `search.DefaultService`.
- Produces:
  - `func (s *mcpserver.Server) Register(srv *mcplib.MCPServer)`: the warm-pool start plus the 11 `AddTool` calls now in `Serve`. `Serve()` keeps working for `cmd/mcp` by building its own server and calling `Register`.
  - `type agentsFlags struct` holding the existing serve flag variables, and `func buildAgentsServer(f agentsFlags, cfg *config.Config) (*mcpserver.Server, error)`: today's `RunE` body from backend resolution through `mcpserver.NewServer`, same behavior, including the "redis URL is required" error.
  - `func newServeMCP(agents bool, f agentsFlags, cfg *config.Config, svc *search.Service, notes io.Writer) *mcplib.MCPServer`: `mcplib.NewMCPServer("aimux", version.Version)`, then `sessionmcp.New(svc).Register(srv)`; when `agents`, `buildAgentsServer` then `Register`, or on error write `agents tools disabled: <err>` to `notes` and continue.
  - `serve` gets `--agents` (bool, default false). `RunE` loads config, calls `newServeMCP(agents, f, cfg, search.DefaultService(cmd.ErrOrStderr()), cmd.ErrOrStderr())` and runs `mcplib.ServeStdio`. The Long help says session tools are always on and `--agents` adds remote agents.

- [ ] **Step 1: Write the failing tests.** `TestMCPServeCmd_MissingRedisURL` is replaced, since a missing Redis no longer fails `serve`.
  - `TestBuildAgentsServer_MissingRedisURL`: backend `k8s`, no Redis URL, so the error contains `redis`. That's the old guarantee, now on the helper.
  - `TestMCPServeCmd_AgentsFlag`: `Flags().Lookup("agents")` exists with default `false`; the existing `TestMCPServeCmd_Flags` still passes.
  - `TestNewServeMCP_SessionsOnly`: `agents=false` with a temp-dir `search.Service`, so `ListTools()` holds exactly the five session tools.
  - `TestNewServeMCP_AgentsFailureKeepsSessions`: `agents=true`, k8s backend, no Redis, so the five session tools are there, no agent tools are, and `notes` contains `agents tools disabled`.
  - `TestSessionMCP_NoClusterDeps` (Requirement 1): `exec.Command("go", "list", "-deps", "./internal/sessionmcp/")` run from the module root prints no line matching `internal/mcpserver|k8s.io/|redis`.
- [ ] **Step 2: Run** `go test ./cmd/aimux/cmd/ -run 'TestBuildAgents|TestMCPServe|TestNewServeMCP|TestSessionMCP' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `Register` in `internal/mcpserver/server.go`, then `agentsFlags`, `buildAgentsServer`, `newServeMCP` and the new `RunE` in `mcp.go`.
- [ ] **Step 4: Run** `go test ./cmd/aimux/cmd/ ./internal/mcpserver/ ./internal/sessionmcp/ ./internal/search/`. Expected: all PASS.
- [ ] **Step 5: End-to-end stdio check** (Review Focus 1). Build with `go build -o /tmp/aimux-dev ./cmd/aimux`, then:

```bash
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}' \
 '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
 '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_sessions","arguments":{"query":"openshell service accounts","mode":"keyword"}}}' \
 | env -u OPENAI_API_KEY /tmp/aimux-dev mcp serve 2>/tmp/aimux-mcp.err | tee /tmp/aimux-mcp.out | jq -c '.id'
```

Expected: `1`, `2`, `3`, with every stdout line parsed by `jq` (no parse errors); response 2 lists exactly the five session tools; response 3 contains a real session ID. Repeat with `mcp serve --agents` and no cluster: the same five tools, and `/tmp/aimux-mcp.err` contains `agents tools disabled`.
- [ ] **Step 6: Commit** `feat(mcp): session-first aimux mcp serve; remote agents behind --agents`

### Task 6: Switch Claude Code over (needs the user's go-ahead)

Not code. Do these with the user, one at a time:

- [ ] `make install` (or `go install ./cmd/aimux`) so `aimux` on PATH has the new `serve`.
- [ ] Register at user scope: `claude mcp add -s user aimux -- aimux mcp serve`. Don't use `aimux mcp register`: it writes to `~/.claude/settings.json`, but Claude Code reads user MCP servers from `~/.claude.json`. Restart Claude Code; check `claude mcp list` shows `aimux` connected, and check `tools/list` directly (a Connected server can still have zero tools).
- [ ] In a fresh session, paste a bare session UUID and confirm the agent calls `continue_session` and gets a resume line.
- [ ] Only after that: remove the previous session-search MCP server and its ingest hook, and point any agent routing rules at the new tools. Keep the old index until its sessions are exported.

## Follow-ups (not in this plan)

- `aimux mcp register`/`unregister` target `~/.claude/settings.json`; switch them to `claude mcp add`/`remove` (or `~/.claude.json`) and register the session-first server by default.
- Next tool groups, each opt-in like `--agents`: `eval` (GOOD/BAD/WASTE labels, eval datasets) and `mlflow` (OTEL export, trace links).
