# What subagents do: evidence for routing per call

What the owner's subagents did in the seven days to 2026-11-27 (transcripts last written in that time, runs under a scratch folder left out), measured from Claude Code's own subagent transcripts by `scripts/helper-work.py`. The transcripts are the owner's and are not in the repository, so the figures can be produced again only on his machine, and move as the week moves. It is the evidence for two decisions: no blanket subagent role, and where routing by the proxy should start.

## The numbers

234 subagent runs in seven days. 198 of them (85%, and most of the subagent tokens) were the built-in `general-purpose` subagent. Over thirty days: 1,510 runs, 86% `general-purpose`.

The 198, sorted by the tools each run used:

| What the run did | How it is told | Runs | Share of runs | Share of tokens |
|---|---|---|---|---|
| Gathers from the web or services, reports back | web search or fetch, or an MCP tool; no write asked for | 82 | 41% | 17% |
| Investigates with commands, reports back | a shell; no write asked for, no web | 66 | 33% | 18% |
| Researches, then writes a document | asks to write a file that is not code | 36 | 18% | 41% |
| Edits code or config | asks to write or edit a code or config file | 13 | 7% | 24% |

Named subagent types were rare: `code-reviewer` ran 3 times, `Explore` twice.

## What follows

- A default model for every subagent that names none (`roles.subagent`) would move the 92% that is research and investigation along with the 7% that is implementation. The only work `gpt-6.2-sol` has evidence for is the 7%. The role stays unset.
- Naming a model in a subagent's own definition reaches only the runs where Claude picks that type: a few a week.
- The tools a run uses tell its work apart without reading a prompt. These labels were given looking back at a finished run. The proxy sees the tool calls in a conversation only as they happen, so it can label a run the same way once it has used a tool, not at its first call; what can be known at the first call (the tools offered, the size of the brief) is not measured here and is the first question of stage 6.
- Code edits and written documents are a quarter of the runs and two thirds of the tokens.

## Limits

- A write counts when the run asked for it; whether the file changed was not checked. A shell command that changes a file is not seen as a write.
- The sort is by tools, so it is coarse: "investigates with commands" is anything that ran a shell and changed no file. No prompt was read to check a label.
- Tokens are each response's input, cached input and output, counted once per response. They are mostly cached input read again on each call, so they overstate cost; the shares compare.
- It says what the runs did, not how well any model would do them. That takes the eval engine.
- 33 of the week's runs were answered by `gpt-6.2-sol`, from sessions already on Codex; they are not separated.
