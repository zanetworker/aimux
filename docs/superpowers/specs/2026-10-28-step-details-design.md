# Step details and colour in the phone chat

Small change, both clients (iPhone app and web client) plus the server. Owner asked for it after using 0.3 (11): steps show as `$ command...` and cannot be read.

## Intent

A step in the chat should be readable when you want it and quiet when you don't. Today the server cuts a shell command to 57 characters and the clients show one line, so a step cannot be understood.

## Goals

- Tap a step to see the whole thing: the full command, the error if it failed, and for an edit the lines it removed and added.
- Colour only where it means something: a failed step in the status red, removed and added lines of an edit in red and green, the `$` prompt dimmed so the command stands out.
- The same in history and in a live turn, in both clients, pinned by shared fixtures.

## Non-goals

- Command output (stdout). Not stored today; reading it from the transcript is a later piece of work.
- Colour by step kind, syntax highlighting of commands.
- Changing how steps are grouped or counted.

## Anti-goals

- More noise in the default view. Closed, a step stays one line, as now.
- Anything typed into or loaded from the network when a step opens.

## Constraints

- Near-monochrome visual system (owner rule): the only new colours are status red for a failed step and a muted red and green for diff lines, in light and dark. The `$` uses the secondary text colour.
- API changes are additive and optional (old clients ignore them, new clients cope without them).
- Size caps so one turn cannot balloon: command 4 KiB, error 2 KiB, each side of an edit 4 KiB; longer text is cut with a marker the clients show as "…".
- Server text never reaches `innerHTML` on the web; only http(s) links; code is left to right.
- Fixtures invented, never real session text.

## Approaches

1. **Detail on the history API, derived from ACP events for live turns (chosen).** `TurnTool` gains optional `detail {input, error, edit{old,new}}` filled from the parsed transcript (`trace.ToolSpan` already has `ErrorMsg`, `OldString`, `NewString`; the full input needs one new field). Live steps take the same fields from the ACP `tool_call` `rawInput` and `diff` content. Cheap, no new endpoint, opening a step is instant.
2. A per-step detail endpoint fetched on tap. Smaller turn payloads, but a network wait on every tap and a new endpoint to secure. Rejected: payloads are small after the caps.
3. Only stop cutting the command at 57 characters and wrap the line. Cheapest, but no error or edit view and noisier closed steps. Rejected.

## Decided

- Closed step: one line as today. Its target keeps the short form (`snippet`).
- Open step (tap the line; tap again closes; VoiceOver: an expand action): the full input in monospace, wrapped, with a dimmed `$` for commands; the error text in the status red under a "Failed" label; for an edit, the removed then added lines with `-` and `+` marks and muted red and green backgrounds, at most 40 lines then "N more lines".
- A failed step's icon and line take the status red, open or closed.
- Server: a new `trace.ToolSpan.Input` (full, untrimmed input text for Bash, Grep, Glob, WebFetch, WebSearch, Task, other tools' main argument), filled by the Claude parser; `remoteapi` maps it and the existing error and edit fields into `TurnTool.detail`, capped. `snippet` unchanged.
- Shared fixtures in `api/testdata/clients/chat/` gain step details from history and from live events; both clients' timeline tests read them.

## Left to the builder

- Exact layout spacing, the open/close animation, and the field names inside `detail` beyond those above if the contract reads better another way.
- Codex and Gemini parsers: fill `Input` only where the data is already parsed; otherwise leave it empty (clients show no detail).
