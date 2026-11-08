# Incremental transcript reading (architecture step 9)

## Intent

Every aimux surface that lists or searches sessions must stay fast however large and busy the owner's transcripts are. Today `list_sessions` on the aimux MCP server takes 1.3 to 17 seconds, because three code paths re-read whole transcripts that only grew by a few lines. Live Claude Code sessions append continuously, so on almost every call several transcripts have changed, one of them 145 MB.

Success: reading transcripts costs in proportion to the bytes appended since the previous read, not to the size of the changed transcripts. Work proportional to the number of sessions (enumerating files, sidecars, the cache file) remains after this design and is measured separately (phase E).

## Measured baseline (2026-11-07, owner's Mac, 24 live sessions)

A profile of the real `aimux mcp serve` composition (`list_sessions`, 3 rounds, 20 s of live writing between rounds):

| Round | catalog scan | search refresh + recent | full `list_sessions` |
|---|---|---|---|
| 0 | 2.04 s | 8.11 s | 2.48 s |
| 1 | 1.07 s | 1.68 s | 1.47 s |
| 2 | 1.35 s | 2.45 s | 1.30 s |

Files changed in the last minute: 5, 192 MB in total. CPU by cumulative time:

| Path | CPU | Cause |
|---|---|---|
| `search.(*Service).embedWorker` → `currentTexts`, `currentHashes` | 7.9 s (27%) | reads every chunk of each changed session back out of SQLite (FTS content, overflow pages), twice, to hash it and find chunks needing embeddings |
| `search.ExtractFile` (from `Index.Update`) | 5.3 s (18%) | re-extracts each changed transcript from byte 0; `writeDoc` then rewrites all its chunks |
| `history.scanAll` → `scanSession` | 4.4 s (15%) | re-parses each changed transcript from byte 0 (cache keyed on mtime and size) |
| `encoding/json.Unmarshal` | 6.8 s (23%, inside the two above) | each line decoded again on every change, once per path |

## Goals

1. A session listing (MCP `list_sessions`, the TUI and dashboard history, `aimux sessions`) reads only the bytes appended to each changed transcript since it last read it.
2. A search refresh extracts and writes only the new part of a changed transcript, and embeds only chunks that are new or changed, without reading stored text back to find them.
3. Results are identical to a full re-read (titles, first and last prompts, counts, cwd, automation flags, search chunks, FTS hits).
4. Targets on the same machine and load: the profile shows no path proportional to a changed file's total size; the search refresh for 5 changed live files under 200 ms. The end-to-end `list_sessions` target (under 150 ms) needs phase E as well, if the benchmark after A to D still misses it.

## Non-goals

- Changing what is shown or found. This is a speed change only.
- A file watcher or background daemon keeping the index hot (possible later; it would complement this, not replace it).
- Changing the embedding model, chunk sizes or ranking.

## Anti-goals

- A listing or search result that differs from a full re-read, even once (a stale title, a missing hit, a chunk embedded twice or never).
- A cache that silently survives a transcript being rewritten or truncated.

## Design

### A. One incremental reader, under an append-only contract

Claude Code transcripts are append-only: `/rename` appends a `custom-title` line, and `/clear` and compaction continue in a new file rather than rewriting an old one (Claude Code issues 26240, 26121, 42291). No check cheaper than re-reading the prefix can prove an arbitrary in-place rewrite did not happen, so the design relies on this contract and says so.

A `jsonl.Tail` (new package, standard library only) remembers, per file: device and inode, the offset just past the last complete line read, the size and mtime at that point, and a hash of the last 4 KiB before the offset. On the next read:

- Same file (device and inode), size at or past the offset, and the 4 KiB before the offset unchanged: read only `[offset, size)`, and only up to the last newline (a line Claude Code is still writing waits for the next read).
- Anything else (shorter file, other inode, changed bytes before the offset, a newer cache version): read from byte 0, as today.

The inode and size checks enforce the contract's cases (a replaced or truncated file); the 4 KiB hash is an extra guard for a rewrite near the end, not a guarantee against an edit earlier in the file. It yields complete lines; each consumer decodes them.

### B. Session scan as a fold

`history.scanSession` becomes a fold over lines: `Fold(state, line) state`, with `Session` derived from the state. The state is its own type, not the finished `Session`: line and human-turn counts, the previous nonzero timestamp and accumulated active seconds, the last nonempty model, the last qualifying user content, the last assistant content, the first entrypoint, cwd and permission mode, the latest git branch, AI title, custom title and away summary, and token totals. Values that come from outside the lines are applied when deriving the `Session`, never frozen in the state: the file-mtime fallback for missing timestamps, and the `.meta.json` sidecar (already read outside the scan). The scan cache (`~/.aimux/cache/session-scan.gob`) stores the fold state and the reader position; a changed file folds only its new lines. `scanCacheVersion` is bumped, so old caches are dropped once.

### C. Search extraction as a fold, chunk writes by difference

`search.ExtractFile` becomes the same kind of fold, producing chunks. A new human message closes the previous exchange, so closed chunks never change. The index keeps, per session, the reader position and the extractor's open state (the exchange still being built), bounded at the output caps: text up to `MaxTextChars`, prose up to `MaxChunkChars/2`, the current prompt and sequence, and the document metadata and title inputs; content past a cap is dropped, as the output drops it. An update appends new chunks and replaces only the open one; FTS rows are inserted for new chunks and replaced for that one.

Exception, the title: every chunk row carries the session title, and a later `ai-title` or `custom-title` line, or a `.meta.json` edit, changes it. Then every chunk row of that session gets the new title (one update per chunk, no transcript re-read), and their embedding inputs change with it (D). A file that must be read from byte 0 is rewritten as today.

### D. Embedding bookkeeping by stored hash

A new table `embedding_inputs(session_id, seq, hash)` holds the hash of every embedding input when it is written, in the same transaction as the FTS and session rows: one row per chunk (title plus prose, or title plus text), and the session summary at `seq = -1` (title, first prompt and all chunk prompts). The FTS table is not altered (an FTS5 table cannot take a new column without a rebuild). The hash is 128 bits of SHA-256: a collision would silently keep a wrong vector, so the 64-bit FNV used today is not enough for a stored key. One migration pass fills the table for existing sessions, reading their inputs once.

Finding work compares `embedding_inputs` with the embeddings table, no text read back; only the inputs to embed are read, once. After the embedder answers, the write checks each input's stored hash against the hash it sent (a change meanwhile drops that vector), instead of reading the texts again. A session reclassified as automated drops its pending work.

Also fixed here: `embedInlineMax` (64) is passed to `embed` as a batch size, so an inline refresh embeds every pending input 64 at a time instead of at most 64. Inline embedding is capped at that number; the rest goes to the background worker.

### E. Work proportional to the number of sessions (only if needed)

After A to D, a listing still enumerates and stats every transcript, decodes every `.meta.json`, decodes and rewrites the whole scan cache when one entry changes, rereads the skill-usage log, and the search refresh globs and stats every file again. If the end-to-end benchmark after A to D misses the target, these are next, each with its own measurement: a sidecar cache keyed by mtime, cache writes of changed entries only, the skill-usage log read incrementally with the same reader, and one enumeration shared by the scan and the search refresh.

## Order

1. D first: it is 27% of the measured CPU, independent of the rest, and includes the inline-batch fix.
2. A with B: every surface (TUI, dashboard, phone, CLI, MCP) lists through the scan.
3. C: search extraction.
4. E: only where the end-to-end benchmark still points.

Each step ships on its own, with its benchmark and the profile rerun.

## Approaches considered

1. **Incremental folds over one tailing reader (chosen).** Each consumer keeps its own fold state; one reader decides what is new. Cost follows appended bytes. Main risk: a fold that differs from a full parse; mitigated by the equivalence tests below.
2. **One shared decode feeding both consumers.** Decode each new line once into a common struct for both folds. Removes the remaining duplicate decode (the 23% JSON share is split between the two). Kept as a follow-up once A to D land: it couples the two packages' line models, and A to D already remove the size-proportional work.
3. **Keep full re-reads, cache harder.** Throttling refreshes (at most once per N seconds) hides the cost but serves stale lists, which the owner's Now and search views would show. Rejected.

## Testing

- Equivalence (the core guarantee): for every transcript in `testdata` and a sample of real-shaped fixtures, folding the file split at random line boundaries (and with a partial last line held back) gives exactly what a full parse of the file up to its last newline gives: the same `Session` and the same chunks. Property test over many split points.
- Title change: a `custom-title` or `ai-title` line, or a `.meta.json` edit, after many chunks gives every chunk row and embedding input the new title, as a full rebuild does.
- Reader: growth, partial last line, truncation, rewrite in place (same size or larger), inode change (atomic replace), deleted file, newer cache version: each read from the right place.
- Index: chunks and FTS hits after N incremental updates equal those after one full update; embedding bookkeeping finds exactly the new and changed chunks.
- Benchmarks (`*_bench_test.go`) for a 150 MB transcript growing by 10 KB: full versus incremental, for scan, extract and the embedding check. The profile from the baseline is rerun after the change and kept with these numbers.

## Rollout

Behind no flag: the caches carry versions, so a first run after the upgrade reads everything once (as today) and is incremental from then on. The parts ship in the order above.

## Left to the builder

- Package and type names beyond those above, and where the fold states are serialized (gob, as today, is fine).
- The exact migration of the chunks table.

## Review

Codex (gpt-5.6-sol) reviewed the first draft: the rewrite check is not a guarantee (now an explicit append-only contract), title changes touch every chunk, the summary embedding needed its own row (`embedding_inputs`), the scan needs its own fold state, the open chunk must be bounded, the 150 ms target needs work beyond transcript reading (phase E), and the inline embedding cap was a batch size. Each is addressed above.
