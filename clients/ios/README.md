# aimux for iPhone

A native iPhone app (SwiftUI) for [aimux](https://github.com/zanetworker/aimux)'s remote API v1: see what needs you on the Mac (Now: approvals you answer on the card, failed turns you retry, what just finished, what is working, what sits open in a terminal), browse every session by day (All, filtered by project and sorted), search titles and conversation content, open any session as a chat (follow it live, send a message, stop a turn, answer approvals, see its cost and tokens; sessions that never started are read only), type into Claude Code sessions open in iTerm2 on the Mac and answer their permission prompts (when the Mac turns on terminal input; otherwise those sessions are read only too), and (coming next) start sessions and use voice.

## How it connects

The app talks only to `/v1` of `aimux remote serve` on your Mac, over Tailscale. The phone must be signed in to the same Tailscale account, the Mac allows that login, and the phone must be paired (device login, on by default).

On the Mac:

```bash
aimux remote serve --allow <your-tailscale-login>
tailscale serve --bg --https=8443 http://127.0.0.1:7382
aimux remote pair --name iphone        # prints a code; type it in the app within 5 minutes
```

Pairing makes a P-256 key in the Secure Enclave that signs only after Face ID (`.privateKeyUsage`, `.biometryCurrentSet`) and never leaves the phone. After that the app logs in with Face ID; a login ends after 1 hour without use, after 12 hours, on **Lock** (server settings), on `aimux remote revoke iphone` or when the server restarts. A change to the enrolled faces invalidates the key and the app asks to pair again. **Pair this device again** on the lock screen clears the phone only; revoke the old entry on the Mac with `aimux remote revoke`. The server cannot check that the key is in the Secure Enclave (no App Attest): it trusts the app it paired with. The session token is kept in memory only.

To type into sessions open in iTerm2, set `remote_access.terminal_input.enabled: true` in `~/.aimux/config.yaml` and run `aimux remote terminal-input check` once so macOS can ask to let aimux control iTerm2. A session is blocked only when iTerm2 is in front, it is the focused pane and the Mac was used in the last 2 minutes (`allow_while_in_use: true` skips this). While a session is working or blocked, or its queue is sending, send becomes **Add to queue**, and the Mac sends queued messages one at a time. See the remote access guide (`docs-site/src/content/docs/guides/remote-access.mdx`).

On first launch the app asks for the Mac's address, for example `your-mac.your-tailnet.ts.net:8443` (https is added; any path is dropped). Change it later with the gear button on Now.

## Build and test

```bash
xcodegen generate                      # project.yml is the source; the .xcodeproj is not committed
xcodebuild test -project Aimux.xcodeproj -scheme Aimux -destination "id=<simulator UDID>" \
  -derivedDataPath build/sim -collect-test-diagnostics never
swiftlint
```

The ranking tests read the shared fixtures in `../../api/testdata/clients/`; the web client runs the same files with `node --test "clients/web/webapp/*.test.mjs"` from the repo root.

DEBUG launch arguments for screenshots and UI tests: `-demo finding|list|twenty|error|empty|slow|chat|chat-approval|chat-readonly|chat-terminal|chat-format|chat-items|chat-queue|chat-long` (made-up sample data, no server; `list` covers every Now case, `twenty` has 20 working sessions in 7 projects, the `chat` modes fill the chats; `chat-terminal` makes demo-1 typeable on the Mac, demo-17 show a terminal prompt and demo-18 refuse because the Mac is in use; `chat-items` gives demo-2 a slash command, a bash-mode command and its output, a message typed on the Mac while Claude worked, with a paste and an image, and a thought, plan and steps with inline Markdown and paths), `chat-queue` fills demo-2 (working, three waiting), demo-3 (paused) and demo-18 (blocked on the Mac) with queues, `chat-long` makes every session a long terminal chat whose latest 30 turns move on by one each read, `-tab now|all|search`, `-openChat <id>` (start with that demo session's chat open), `-openInfo YES` (and its info sheet), `-openSteps YES` (and its folded steps and thoughts open), `-openStepDetails 2,4` (with the steps open, open those steps' details by index in their group), `-openSettings YES`, `-server <address>`, `-query <text>` (search tab), `-project <path>` (All), `-sort folders` (All in its Folders view, whose folders start closed), `-collapse <path>` (close that Working folder on Now; `-collapse mac:<path>` closes an On your Mac folder; Now's folders start open), `-editField title|description` (with `-openInfo YES`: open the info sheet in edit mode), `-scrollToEnd YES` (lists open at the bottom), `-serverAddress ""` (start with no saved address), `-deviceLogin pairing|revoked|keychanged|locked|refused|live` (with `-demo`: show the demo behind a device login in that state, with a software key).

## Code map

| Path | What |
|---|---|
| `project.yml` | XcodeGen project: app `Aimux`, framework `AimuxKit`, `AimuxKitTests`, `AimuxUITests` |
| `AimuxKit/Sources/APIClient.swift` | `/v1` client (health, sessions, session detail, search, event snapshots and pages, approvals, messages with an idempotency key, cancel, terminal messages and keys); maps failures to `APIError` |
| `AimuxKit/Sources/Models.swift` | `Session`, `SessionState`, `SessionList`, `Health`; unknown enum values are kept, not rejected |
| `AimuxKit/Sources/Events.swift` | Event page and event kinds, the ACP `SessionUpdate` payload, `Approval` and its `PermissionOption`s; unknown kinds are kept |
| `AimuxKit/Sources/SessionDetail.swift` | `SessionDetail` with its history `Turn`s, their `TurnTool`s and optional `SessionStats` (`startTime` may be absent), and optional `TerminalStatus` with its `TerminalPrompt` (a malformed one is dropped), and optional `repo` (`SessionRepo`: web, branch, root) for reference links |
| `AimuxKit/Sources/EventStream.swift` | Follows the SSE event stream (`id: <log>.<seq>`, keep-alive comments) |
| `AimuxKit/Sources/StepDetail.swift` | A step's detail (command, error, old and new text) read from history or from ACP tool content, and the diff lines of an edit (40 shown, the rest counted) |
| `AimuxKit/Sources/Timeline.swift`, `TimelineDraw.swift` | Chat timeline: history turns and events merged into display items by the shared rules |
| `AimuxKit/Sources/SavedPrompt.swift` | `unwrapSavedPrompt`, `userMessageDisplay` (a saved message without Claude Code's wrappers: text, slash commands and command output as parts, image placeholders as chips) and `savedPromptAsTyped` (the form the pending match compares) |
| `AimuxKit/Sources/ChatModel.swift` | Chat screen state: consistent load, live stream with 2-30 s reconnect backoff, send with a kept idempotency key, Stop, answers; read only for never-started sessions and for terminal sessions from a server without terminal input; reads the session again when a turn ends, and loads again on `open_in_terminal` (send) or `not_running` (Stop) |
| `AimuxKit/Sources/ChatModelTerminal.swift` | Terminal sessions (`terminal` in the detail): history read every 2 s while busy or for 2 minutes after a send, else 15 s; messages typed into the Mac's terminal (never retried automatically; a Retry keeps the key); one re-read on `not_at_input` or `not_submitted`; the terminal prompt answered by key, collapsing on `prompt_changed`/`no_prompt` |
| `AimuxKit/Sources/ChatQueue.swift`, `ChatModelQueue.swift` | The chat queue (`QueueStatus`, `QueueItem`, the composer's Send or Add to queue, rows and Resume, per `api/testdata/clients/queue/`); the model adds, edits, moves, deletes and resumes through `/v1/sessions/{id}/queue`, applies `queue` events (`events?queue=1`) and keeps a lost edit for queueing again. `App/Components/QueueList.swift` draws it under the composer (one send arrow: it queues while the session is busy, and a message refused busy is queued by itself) |
| `AimuxKit/Sources/ChatDrafts.swift` | Per-session draft and failed message, kept while the app runs so a closed chat loses neither |
| `AimuxKit/Sources/ChatPresentation.swift` | Chat helpers: the Next bar target, the header status and read-only line, info sheet formats |
| `AimuxKit/Sources/MarkdownTree.swift` | `MDBlock`/`MDInline`, the shared Markdown block tree, Codable in the fixtures' wire shape |
| `AimuxKit/Sources/MarkdownBlocks.swift` | `markdownBlocks`: Foundation's `AttributedString(markdown:)` normalized to the tree of `api/testdata/clients/markdown/` (empty cells and items put back, task markers, HTML lines as text, nesting capped at 100), `bubbleBlocks` for user messages, the 256 KiB limit |
| `AimuxKit/Sources/MarkdownInlines.swift` | Inline normalization: http(s)-only links, Foundation's autolinks undone and the README's bare URL pass, canonical emphasis nesting |
| `AimuxKit/Sources/References.swift`, `ReferenceRules.swift` | `linkReferences`: issue, commit, Jira and path links from `SessionDetail.repo` and `Health.links` (`api/testdata/clients/references/`) |
| `AimuxKit/Sources/MediaCards.swift` | `mediaCards`: a reply paragraph that is only a YouTube, Vimeo, video, image or audio link becomes `.media` (`api/testdata/clients/media/`) |
| `AimuxKit/Sources/MarkdownDisplay.swift` | `MarkdownRender` (reply and bubble blocks cached by text and context), `stepInlines` (a step target as one line), text direction |
| `AimuxKit/Sources/APIDate.swift` | Timestamp parser for the server's 0-9 fractional digits; `APIDecoder` |
| `AimuxKit/Sources/LossyList.swift` | Decodes a list row by row so one bad row does not fail it |
| `AimuxKit/Sources/DeviceKey.swift` | `DeviceKey` and `DeviceKeyStore`: the Secure Enclave key (`EnclaveKeyStore`), a software key for tests and the simulator (`SoftwareKeyStore`, DEBUG only), SPKI DER of the public key, error mapping (cancelled, invalidated) |
| `AimuxKit/Sources/BiometryState.swift` | What Face ID reports now: the enrolled faces' state hash, or the `LAError` code saying why there is none (only not enrolled counts as an enrollment change) |
| `AimuxKit/Sources/DeviceLogin.swift` | Device login: pairing code form, the signed pairing and login strings, the state machine (off, locked, live, unpaired), the session token in memory, the wait while locked and the single retry after `login_required` |
| `AimuxKit/Sources/APIError.swift` | Error codes from the contract, plus HTTP, transport and decoding errors |
| `AimuxKit/Sources/UserMessage.swift` | What the app says for each error |
| `AimuxKit/Sources/ServerAddress.swift` | Turns typed input into the base URL |
| `AimuxKit/Sources/SessionRowFormat.swift` | Project name, relative time, state badge |
| `AimuxKit/Sources/SessionCache.swift` | Last list in the caches folder, shown at launch and offline |
| `AimuxKit/Sources/SessionListModel.swift` | List screen state: cached rows, then live; errors keep the rows; "show older" up to 500 |
| `AimuxKit/Sources/SessionBrowse.swift` | Title/folder/prompt filter, project counts, snippet highlighting |
| `AimuxKit/Sources/NowModel.swift`, `NowRanking.swift`, `Activity.swift` | Now: activity from event snapshots, the shared ranking, answers and Retry |
| `AimuxKit/Sources/AllSections.swift` | All: day (or project) sections by the shared rules |
| `AimuxKit/Sources/SearchModel.swift` | Server search: checks the `search` capability once, keeps only the latest query's answer |
| `App/` | SwiftUI views: `AimuxApp` (entry point; shows pairing or unlock over the tabs), `PairingView` and `UnlockView` (device login), `RootTabs` (tab bar), `NowView.swift` (Working and On your Mac grouped into project folders, open by default), `AllView.swift` (by day, or the Folders view: one closed folder per project), `SearchView.swift`, `ChatView.swift` (the chat every row opens), `ServerSettingsView`, `Components/` (`ScreenHeader`, `StatusDot`, `ApprovalCard` with the approval row, `SessionRows` with the shared rows, `TimelineRows`, `StepDetailView` (an opened step: command, error or edit lines), `UserBubble` (your messages without Claude Code's wrappers, image chips, slash commands and command output, and the inline Markdown of thoughts, plan entries and errors), `MarkdownView`, `MarkdownTableView` and `MediaCard` (replies as Markdown; video and audio cards play in place with AVKit when tapped), `Composer`, `NextBar`, `SessionInfoSheet`); `DebugHooks` and `DemoChat` (DEBUG only: `-demo` (`chat-format` shows every Markdown block and media cards, `chat-items` wrapped messages and formatted chat items), `-tab`, `-openChat`, `-openInfo`, `-openSteps`, `-openStepDetails`, `-chatAnchor`), icon, privacy manifest |
| `AimuxKitTests/` | Swift Testing; `Fixtures/` copy live payload shapes with the text replaced |
| `UITests/` | `NowAllUITests.swift`: answers an approval on Now and opens its session, opens All and Search; busy Now shows approvals as rows that open in place; the server sheet. `ChatUITests.swift`: a Now row opens the chat with steps and info; Allow collapses the approval card; a read-only chat has no composer; the Next bar follows Now after an answer; titles read without isolate marks; a terminal chat types into the Mac and its prompt card answers |
| `docs/brief.md` | Starting brief: endpoints, events, loading rules from the web client, voice |

## Backlog

Not shown in the chat yet: what a command printed (aimux does not store command output).

1. New session in a recent project folder, redesigned like the chat.
2. Attachments in the composer.
3. Faster server search (1.7-4 s per query today; keyword only).
4. Voice: on-device dictation and spoken replies; approvals stay tap only.
5. Lock-screen approvals once aimux sends push notifications (server work).
