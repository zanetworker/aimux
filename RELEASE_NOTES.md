## Unreleased

### Phone clients
- **Device login** (on by default): only devices you paired reach the remote API, and they log in with Face ID or Touch ID. **After upgrading, update the iPhone app (build 14 or later) and reload the web client, then pair each device once with `aimux remote pair --name <name>`** and type the code on it; until then the clients show the pairing screen, and older app builds only show login errors. The code (`XXXX-XXXX`) works once, for 5 minutes, and five wrong tries burn it. The iPhone app pairs a Secure Enclave key that never leaves the phone; the web client pairs a passkey, which syncs with your passkey provider (iCloud Keychain by default). A login ends after 1 hour without use, after 12 hours, on Lock, on revoke or when `remote serve` restarts. `aimux remote devices` lists paired devices and `aimux remote revoke <name>` cuts one off within a second or two. Pairings, logins and every changing action go to `~/.aimux/logs/remote-audit.log` (owner-only), never with message text, codes, challenges, tokens or keys. New endpoints `POST /v1/auth/pair`, `/v1/auth/challenge`, `/v1/auth/login` and `/v1/auth/logout`, error `login_required`, capability `device_login` and `session` (`live` or `none`) on health. `remote_access.device_login: false` restores tailnet-only access, with a warning at start. The iPhone app is at version 0.3 (14)
- The iPhone app moved into the repo as `clients/ios` and the phone web client to `clients/web` (Go package `webclient`, was `internal/remoteclient`); both follow `api/remote-v1.openapi.yaml`
- New **Now** and **All** screens in both clients. Now shows approvals you can answer on the card, failed turns with Retry, what just finished, what is working and what sits open on your Mac; All lists every session by day with project and sort choices. The iPhone app is at version 0.2 with a search tab
- **Chat and session info** in both clients: open any session to follow it live, send a message, stop a turn and answer approvals; the info sheet shows cost, tokens, model, timing, branch and folder. The iPhone app is now at version 0.3. Both build the chat timeline from the same fixtures in `api/testdata/clients/chat/`
- `GET /v1/sessions/{id}` returns optional `stats` (cost, tokens, turns, duration, model, branch)
- Sessions show a short display title instead of a raw id, with more space between rows
- Both clients rank sessions by the same fixtures in `api/testdata/clients/`, so they cannot drift apart
- CI runs the clients' tests when `clients/**` or `api/**` change
- A message typed into a Claude Code session while it was busy now appears in the session history as its own turn with its answer (Claude Code saves it as a `queued_command` attachment, which was skipped before)
- **Type into terminal sessions** from both clients: a Claude Code session open in iTerm2 on the Mac gets a composer, and its permission prompt shows as a card you answer on the phone. Off by default; turn it on with `remote_access.terminal_input.enabled` and run `aimux remote terminal-input check` once to grant macOS Automation access to iTerm2. aimux types only when the pane runs that session's `claude`, its input box shows and the Mac has been idle for 120 s (`allow_while_in_use` skips the idle check), refuses text with control characters, never retries a send, and logs each attempt without its text to `~/.aimux/logs/terminal-input.log`. New endpoints `POST /v1/sessions/{id}/terminal/messages` and `/terminal/keys`, an optional `terminal` field on the session detail and the `terminal_input` capability. The iPhone app is at version 0.3 (8)
- **Formatted replies** in both clients: Claude's replies render headings, lists, task lists, quotes, tables, code blocks with a Copy button and links. Issue numbers, commits, file paths and Jira keys become links to the session's repository (new `repo` on `GET /v1/sessions/{id}`) and your Jira (a self-hosted GitLab served over `http` or on its own port keeps that scheme and port); set `remote_access.links.jira_base` and `jira_projects` to link Jira keys (new `links` on health). A reply line that is only a YouTube, Vimeo, video, image or audio link becomes a card that loads nothing until tapped and names the host the tap opens (an address that hides its real host behind a backslash stays a plain link). Your own messages lose Claude Code's wrappers and show pasted images as chips. Both clients parse with the shared fixtures in `api/testdata/clients/`. The iPhone app is at version 0.3 (11)
- **Step details** in both clients: tap a step in the chat to read its full command, its error, or an edit's removed and added lines. History tools carry an optional `detail` (`TurnTool.detail` in `api/remote-v1.openapi.yaml`); live steps read it from the agent's tool call. Command output is not shown yet
- Fix: history replies on the phone keep their blank lines, so a table no longer runs into the paragraphs and the next table after it; on the iPhone a table's rows grow to their tallest cell, so wrapped cells no longer draw over the rows and text below (iPhone build 15)
- **Chat queue** in both clients: line up messages for a session and the Mac sends them one at a time, each after the previous answer finished, also while the phone is locked or offline. Up to 20 messages per session in `~/.aimux/remote-queue.json`; edit, reorder and delete waiting ones; a paused queue (you used the session on your Mac, the session closed, an unclear send after a restart, and others) shows its reason and waits for **Resume**. Works for sessions started from the phone and for iTerm2 sessions. New endpoints `GET` and `POST /v1/sessions/{id}/queue`, `PATCH` and `DELETE …/queue/{item}`, `POST …/queue/resume`, `queue` on the session detail, the `queue` event (`events?queue=1`) and the capability `queue`; audit actions `queue_add`, `queue_edit`, `queue_move`, `queue_delete`, `queue_resume`, `queue_send` and `queue_paused`, never with text. While a session's queue is sending (a message in flight, or running with messages waiting), a direct message is refused with `409 busy` and the clients queue it; while the next message waits for Claude, the queue shows "Waiting: Claude is working". The iPhone app is at version 0.3 (17)
- **Terminal input guard**: a session in iTerm2 is blocked only when iTerm2 is in front, it is the focused pane and the Mac was used in the last 2 minutes (before: any use of the Mac blocked every session). Checked before typing and before Enter. A hotkey window and Stage Manager are not yet verified live. A direct message to a session where Claude is working is refused with `409 busy` ("Claude is working in this session; try again when it finishes."), for iTerm2 and phone-started sessions; the clients put the message in the queue by themselves. `terminal` gains `working`
- Fix: a queued message in an iTerm2 session no longer stays "Answer in progress" after its answer: it finishes once Claude stops working (text you type into the box meanwhile no longer pauses the queue), or at the latest when your next turn at the Mac shows in the history
- Fix (iPhone): a long chat no longer goes blank, or moves what you read to another turn, when its latest turns move on while you are scrolled up (iPhone build 18)
- Fix (iPhone): a terminal chat whose read fails keeps the timeline it showed and says "Couldn't refresh" with Retry; a queue change that fails in transport says whether it timed out, the connection dropped or the request was cancelled, instead of always "Can't reach your Mac" (iPhone build 18)
- iPhone: a **Latest** button jumps back to the newest message when you scrolled up in a chat (iPhone build 18)
- Fix: both clients show a slash command you ran (`/name` in monospace, then its args), bash-mode input (`!command`) and command output (a plain monospace block, "Error output" for stderr) instead of Claude Code's `<command-name>`, `<bash-stdout>` and similar tags, drop its caveat messages, and clear a typed `/command` or `!command` from pending once it is in the history; on the iPhone the Latest button sits in its own row above the composer so it no longer covers the chat (iPhone build 19)
- **Finding sessions**: every session shows a name and a one-line description in both phone clients. Titles, first wins: yours or one aimux asked a model for, then Claude Code's `ai-title`, then the first message. Descriptions: yours, else the newer of Claude Code's away summary (written after about 3 minutes idle) and one aimux asked a model for. The model is used automatically only with `sessions.auto_title: true`, once per session, for sessions the phone's Now screen can show (Last on your Mac, Pinned, working, open in a terminal, or finished in the last 15 minutes) and the chat you open, when they have nothing else; it sends the first user message and the start of the latest reply, up to 500 characters per session (60 tokens out per session, up to 5 sessions per request). `sessions.naming_daily_limit` (default 20) caps it per day. **Update summary** in the info sheet sends the first user message and the latest turns, up to 1,200 characters, only when you tap it, and keeps text you wrote. `sessions.title_model` is now `haiku` by default (`claude-haiku-5-5`; unknown names fall back to it). Without an API key aimux runs `claude -p` in safe mode from an empty folder with no tools, MCP servers or hooks, using your Claude Code login. Three failures in a row pause automatic naming. Token use is recorded in `~/.aimux/data/usage.jsonl` under the label `naming`; a model without a price shows an unknown cost, never 0. The TUI and dashboard **Generate Titles** now keep titles you wrote and record usage. The Remote access guide ("Finding sessions") lists exactly what text goes to Anthropic
- Names, descriptions and pins live in `.meta.json` next to each transcript, written under a lock (`.meta.json.lock`); the dashboard's star and title calls now change only the fields they send. Pins are the existing star
- **Last on your Mac**: with terminal input on, aimux notes the iTerm2 session in front every 15 seconds while the Mac is in use (memory only; clients hide it after 12 hours)
- Both clients: Now shows Last on your Mac, then Pinned, then the usual sections (Needs you wins; a session appears once), groups sessions by folder, shows description lines, and searches descriptions. Pin from the web "..." menu or by long press on the iPhone; the info sheet edits the title and description and has Update summary. The iPhone app is at version 0.3 (21)
- Remote API: `description`, `pinned` on sessions, `lastOnMac` on the list, `PATCH /v1/sessions/{id}/meta`, `POST /v1/sessions/{id}/describe` (errors `owner_set`, `describe_running`, `naming_timeout`, `naming_auth`, `naming_model`, `naming_unreadable`, `naming_failed`, `naming_unavailable`), capability `describe` and `naming` on health

- **Sending into a terminal session just works**: Claude Code's grey suggested prompt is typed over (aimux types a space to tell it from a draft), and a draft left in the box is replaced (`remote_access.terminal_input.keep_drafts: true` keeps it and refuses the message with `draft_in_box`). A named session's box, whose top rule carries the name, is recognised; a send that typed but did not submit can be retried (Retry presses Enter on text aimux left there, never on a draft of yours); slow-drawing terminals get up to 1.5 s. Screen reads take the pane's last 300 lines
- **Composer**: one send arrow, like any chat; while the session works it queues the message (Stop beside it), and a message refused busy is queued by itself. The queue folds to one line, "2 queued · next: …", that opens on a tap
- **Live working line**: a terminal chat ends in "Working · 1m 20s · <latest step>" while Claude works (`terminal.workingSeconds` on the session detail)
- **Syntax colours** in code blocks (Go, Swift, JS/TS, Python, shell, JSON, YAML, diffs), light and dark, and inline code in its own colour; one set of rules pinned by `api/testdata/clients/highlight/`
- **On your Mac** on Now and All's new **Folders** view group sessions by project
- Fix: a Stop hook's feedback no longer shows as your message; it is a "Stop hook" step of the turn, and other text Claude Code injects is not shown
- Fix: naming covers sessions whose title is only an id even when Claude Code wrote an away summary, and names them from that summary
- Fix (iPhone): Now shows the saved list at launch instead of a blank screen; a long, busy chat no longer scrolls into empty space; the queue editor's Save is readable in dark mode
- Fix: a permission prompt taller than the screen aimux reads (300 lines) no longer shows a card without its command; answer it at the Mac
- Opened steps: a command reads as shell with its colours and an edit's lines take the file's language colours (from its extension) on their red and green; a command and an error carry **Copy** (the whole text)
- The live working line also shows while Claude Code's registry says busy (the header's Working), and sending on the iPhone scrolls to your message
- The iPhone app is at version 0.3 (30)

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
