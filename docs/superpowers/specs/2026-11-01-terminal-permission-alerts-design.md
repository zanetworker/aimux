# Permission alerts for terminal sessions

`aimux remote serve`, a new `aimux remote hooks` command, both phone clients and the shared fixtures.

## Intent

Claude Code sessions open in iTerm2 wait silently when they need permission. The phone only notices a prompt by reading the screen of the chat that is open, so the owner, with 20 or more sessions open, misses them: no push, nothing in the list. Sessions started from the phone already get a push and a "Needs you" row; terminal sessions should get the same.

## Goals

- A terminal session waiting for permission sends the owner a push notification (the same notifiers as phone approvals: ntfy, APNs) and shows under **Needs you** on Now, with "Waiting for permission" on its row in every list.
- It clears when the owner answers (at the Mac or from the phone) and Claude continues, or the turn ends.
- No screen polling: Claude Code tells aimux through a hook.

## Non-goals

- Answering from the push itself; the chat's existing prompt card answers.
- Other "needs you" states (Claude asked a question in prose, idle prompts).
- Installing anything silently: the owner runs the install command (or approves it being run).

## Anti-goals

- Writing `~/.claude/settings.json` without a backup or in a way that loses the owner's existing hooks.
- Any network call from the hook; anything readable by other users.
- Leaking command text into logs; push text follows the existing approval push rules.

## Approaches

1. **Claude Code hooks write a marker file; the server reads it (chosen).** A `Notification` hook (permission prompts) writes `~/.aimux/waiting/<session_id>.json` (0600, dir 0700); `PostToolUse`, `UserPromptSubmit` and `Stop` hooks remove it. The server reads the directory when it lists sessions and watches it for new files to push. No network, no auth surface, works when the server restarts.
2. Hook posts to the local server over HTTP. Rejected: `remote serve` listens behind tailscale serve, so a local endpoint needs its own auth; a file is simpler and safer.
3. Poll every idle pane's screen. Rejected earlier: costly and brittle.

## Design

- **Hook command:** `aimux remote hooks notify` (hidden helper) reads the hook's JSON on stdin (Claude Code passes `session_id`, `hook_event_name`, `message`, and for Notification a type; the builder verifies the exact fields in Claude Code's hooks documentation). On a permission notification it writes `{session_id, message, since}` atomically; on the clearing events it removes the file. It never fails Claude Code: always exit 0, no output on stdout, errors only to a log line without text.
- **Install:** `aimux remote hooks install` merges the hook entries into `~/.claude/settings.json` (creating it if missing), keeps every existing hook, writes a backup `settings.json.aimux-bak-<time>` first, is idempotent (a second run changes nothing), and prints what it added. `--uninstall` removes only aimux's entries. `aimux remote hooks status` says whether they are installed. The hook command uses the absolute path of the running aimux binary.
- **Server:** `GET /v1/sessions` adds `waiting: {"kind": "permission", "message", "since"}` to a session with a marker file, only while that session is open (registry live); a marker for a session that is not running is ignored and removed. A file watcher (or a 2 s poll of the directory) sends one push per new marker (keyed by session and `since`), and retracts it when the marker goes, like phone approvals. Push title and body follow `push.ForApproval`'s shape: session title, and the message.
- **Clients (shared fixture rules):** Now's Needs you also takes sessions with `waiting`, ordered with pending approvals by `since`; the row's state line is "Waiting for permission"; the list rows (All, search) show the same state. The chat already shows the prompt card from the screen read.
- **Docs:** remote-access guide (setup step, what is written where), README, release notes, config reference if a setting is added.

## Left to the builder

- Exact Claude Code hook event names and matcher for permission notifications (verify in the docs; record the source in the code comment).
- Watcher mechanism (fsnotify is a new dependency, so poll the directory every 2 s instead).
- Fixture names.
