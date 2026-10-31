# Finding sessions: the Mac's layout and one folder identity

Both phone clients (`clients/ios`, `clients/web/webapp`), `aimux remote serve` (`internal/remoteapi`), the iTerm2 helper (`internal/terminput/iterm2`) and the shared fixtures (`api/testdata/clients/`). Follows `2026-10-30-finding-sessions-design.md`, which added names, descriptions, pins and project folders.

## Intent

The owner still finds sessions on the phone "very hard". On the Mac they find a session by place: they know which iTerm2 tab and pane holds which work. On the phone, sessions are grouped by the last part of their folder path (one repo spreads over "aimux", "clients", "ios"), Now and All group differently, and rows and folders reorder by activity, so nothing stays where it was. A typical day has 2 iTerm2 windows, 9 tabs and 25 open Claude Code sessions, plus about 400 in history.

The job: turn a remembered piece of work ("the thing in that tab", "the session doing X", "the one from yesterday") into the right conversation, quickly and without opening the wrong one.

## Goals

1. **Now mirrors the Mac.** Open sessions are listed by iTerm2 window, then tab, then pane, in iTerm2's order, so the owner's Mac memory works on the phone. Nothing reorders while the screen is open; status changes in place.
2. **One folder identity everywhere.** A session's folder is its git repository, then its worktree, then its subfolder, named the same on every screen ("aimux / clients · ios").
3. **All finds anything.** All opens on Recent (newest first, by day) or Folders (the stable tree) and remembers the last choice; first launch shows Recent.
4. **One search, the same on both screens.** A box at the top of Now and All searches every session; results list open sessions first. The empty box never leaves a blank screen.
5. **Fewer places to look.** Two tabs, Now and All. The Search tab goes away.

## Non-goals

- "Needs you" (a session waiting on a permission prompt or a question). The registry cannot tell it, and polling every pane's screen is costly and brittle. Possible later, on demand.
- Semantic or embedding search; the existing keyword search stays.
- Changing the Mac: aimux never focuses, rearranges, renames or types into windows or tabs for this.
- Pixel-accurate panes (left and right split positions); iTerm2's scripting gives pane order, not geometry.
- Other terminal apps' layouts (Terminal.app, tmux). Their sessions are listed by folder.

## Anti-goals

- Rows or folders that jump while the owner looks at them.
- A dumping-ground folder holding most of history.
- Search that behaves differently on the two screens.
- Window or tab names, or any session text, in logs, fixtures, or anything committed.

## Constraints

- Contract changes are additive to `api/remote-v1.openapi.yaml`; device login covers everything; old clients keep working.
- Reading the layout is read-only JXA, as in `internal/terminput/iterm2/helper.js` (`locate()` already walks windows, tabs and sessions). Measured on the owner's Mac: windows, tabs, pane names and ttys for 30 panes in about 130 ms. It never starts iTerm2 (check `app.running()` first), never focuses.
- The layout read is independent of `remote_access.terminal_input.enabled`: listing panes types nothing. If macOS has not granted Automation access, the layout is simply absent and Now falls back to folders.
- Shared behaviour is pinned by fixtures in `api/testdata/clients/` and run by both clients, as today. Fixture titles, names and paths are made up (`my-mac.tail1234.ts.net` style); never real ones.
- Claude Code's registry (`~/.claude/sessions/*.json`) gives `pid`, `cwd`, `sessionId` and `status` (`busy`, `idle`, `shell`); aimux already maps a session to its pid and tty (`terminput.Sessions`).
- No new dependency. Design rules: near-monochrome, one state colour, titles dominant, folder and place as quiet secondary text.

## Approaches

1. **Mirror iTerm2 on Now, fixed folder tree on All (chosen).** Reuses the owner's spatial memory for open sessions and gives closed ones a predictable home. Costs a layout read per list and a folder resolver on the server.
2. **One fixed folder tree on both screens.** Simpler, one structure, but ignores how the owner actually remembers open sessions.
3. **Search first, minimal structure.** Fastest when a word is remembered, no help when it is not; the old Search tab showed this.

A second opinion (Codex, given the problem without our design) arrived at approach 1 independently: a Mac screen in iTerm2 order, a repo-based work view with history, search on both screens. Its critique of our mockups led to: keep a Recent view in All, drop "Needs you" from the first version, avoid a giant "Other folders", make search consistent, fold tabs except the last used one and remember what the owner opens.

## Design

### Server: two fields on each session in `GET /v1/sessions`

**`folder`** (always present):

```json
{ "repo": "aimux", "worktree": "clients", "sub": "clients/ios", "label": "aimux / clients · ios" }
```

- Git folders: `repo` is the folder name of the repository's main worktree (from `git rev-parse --git-common-dir`); `worktree` is the linked worktree's folder name, absent for the main checkout; `sub` is the path below the worktree top, absent at the top.
- Non-git folders: `repo` is the parent folder's name and `worktree` the folder's own name ("_experiments / branches-vs-worktrees"); the home folder itself is "Home". This splits history by where it ran instead of one "Other folders" bucket.
- Resolved once per path and kept in `~/.aimux/folders.json` (owner-only), so a deleted worktree keeps its identity. A path never resolved and gone now falls back to the non-git rule.
- `label` is what clients print; clients never derive names themselves.

**`mac`** (only for a session whose running `claude` is in an iTerm2 pane):

```json
{ "window": { "id": "w55736", "index": 1, "name": "Window 1" },
  "tab": { "index": 3, "name": "aimux" },
  "pane": 2, "panes": 4, "lastInFront": false }
```

- Matching: registry entry (pid alive, process start time matching `procStart`) → its tty → the iTerm2 session with that tty. Registry entries whose pid is gone are ignored. One pane holds at most one session: the registry entry whose pid is the pane's foreground process wins.
- `window.index`: windows in creation order (ascending iTerm2 window id), not front-to-back order, so switching windows on the Mac does not renumber them.
- `window.name`: iTerm2's window name when it is not just the active pane's name (a title the owner set); otherwise "Window N". Tab names: iTerm2's name for the tab (its current session's name). Panes: position within the tab, 1-based.
- `lastInFront`: the pane that was in front the last time the Mac was used (today's `lastOnMac`).
- The layout read is cached for 3 seconds; a failing read (iTerm2 not running, Automation denied) leaves `mac` absent and logs one line without names.

`lastOnMac` stays in the list for old clients.

### Now

From the top:

1. Search box (below), then one chip: **Working · N** (from the registry and `terminal.working`), which narrows Now to working sessions.
2. **Pinned**: pinned sessions that are not open in iTerm2. A pinned session that is open shows a pin mark in its place under On your Mac instead of appearing twice.
3. **On your Mac**: window headers ("Window 1", or the owner's window title), then tabs in order ("aimux · tab 3"), then pane rows. A row shows the title, a state line ("Working · 1m 20s · go test ./…", "Your turn · 4 min", "Shell"), and a quiet line with the folder label and "pane 2". The `lastInFront` pane carries a small marker.
   - Every tab starts folded except the one holding the `lastInFront` pane. A folded tab is one line: name, pane count, working count ("aimux · 4 panes · 1 working").
   - The client remembers which tabs the owner opened or closed (key: window id and tab index), on the device.
4. **Elsewhere**: open sessions not in an iTerm2 pane (started from the phone, other terminals), grouped by folder label.

Removed from Now: Working folders, On your Mac folders, "Last on your Mac" as its own row, Finished recently. When `mac` is absent for every session (no iTerm2, no access), On your Mac lists open sessions by folder as today, under the same header.

### All

- Search box on top, then a two-way switch, **Recent | Folders**, remembered on the device; Recent the first time.
- **Recent**: newest first, by day (today's All).
- **Folders**: repos A to Z; under a repo, the main checkout's sessions first, then worktrees A to Z; sessions newest first within a folder. Folders start closed except the one holding the newest session; open state is remembered. A folder line shows its count and working count. Status is a mark on the row, never a move to another section.

### Search (Now and All, one component)

- Matches as the owner types, on the device, over every session in the list: title, description, folder label, tab and window name, last message. Then, after 400 ms, the server's conversation search (`GET /v1/search`, unchanged).
- Results: open sessions first (in Mac order), then the rest newest first; conversation hits after, under "In conversations", with their excerpt. Every result shows its folder label and, if open, its place ("tab 3 · pane 2").
- Empty box: the screen as usual (Now's layout, All's Recent or Folders); never blank.
- On Now, typing hides non-matching open sessions in place (tabs keep their order, match counts show "2 of 4 panes", hidden tabs are counted) and then lists closed matches under **Other sessions**. The same words give the same results on both screens.

### Both clients

The iPhone app and the web client implement the same screens from the same fixtures. The Search tab is removed from both.

### Edge rules (calls made while writing this up)

- **Open but not matched to a pane** (registry says running, the layout read found no pane with its tty, or the pane's foreground is not that `claude`): listed under Elsewhere, never dropped.
- **A layout read that fails once**: the server serves the last good layout for up to 30 seconds; clients keep the layout they show until `mac` has been absent for 30 seconds, then fall back to folders. Nothing flips on a single bad read.
- **Window numbers**: positional, like the Mac's own window list. Closing Window 1 makes Window 2 the new Window 1. Remembered tab folding is keyed by window name (or number) and tab name, best effort; an iTerm2 restart may reset it.
- **Non-git folders that share a name** (`~/work/src`, `~/play/src`): grouped by the full parent path, labelled by its name, with the grandparent added when two labels collide ("work / src", "play / src").
- **Resolving folders**: never on the request path. `remote serve` resolves paths in the background at start and when new ones appear, a bounded number at a time; a path not resolved yet uses the non-git rule until it is. A list call never waits on git.
- **`lastInFront`** follows `lastOnMac`'s 12-hour rule. With no such pane, every tab starts folded.
- **Data for on-device search**: the list already carries `title`, `description`, `lastPrompt` and `project`; `folder.label` and `mac` names are added. No other field.
- **Working**: the registry's `busy` for terminal sessions and aimux's own `busy` state for phone-started ones, the same rule as today's Working section; the chip counts every open session, wherever it runs. Closed sessions are never working.
- **Counts and sort** (folder counts, working counts, newest first) are computed by the clients from the list; names and labels come from the server.
- **Parity**: TUI and CLI do not get the Mac layout in this work; the gap is recorded in the parity docs as phone-only.

## Success

Before building, the owner names ten real lookups (open and closed). After: an open session found in under 5 seconds, a closed recent one in under 15, and no wrong-session opens across the ten.

## Builder's calls (left open)

- How to match iTerm2 panes to ttys in the helper (extend `locate()` or add a `layout` op).
- Persisted state keys and storage on each client.
- Exact fixture set; at least: two windows with a renamed and an unnamed window, a tab with 9 panes, a stale registry entry, a pane running a shell, a pinned open session, a session in Terminal.app, a deleted worktree, a non-git folder, home.
