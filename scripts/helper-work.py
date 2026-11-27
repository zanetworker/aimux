#!/usr/bin/env python3
"""What the general-purpose subagents of the last 7 days did, sorted by the tools each one used.

Reads Claude Code's own subagent transcripts under ~/.claude/projects (the .meta.json beside each names the
subagent type) and prints counts and shares only, never a prompt or an answer. The evidence for where per-call
routing should start: docs/superpowers/specs/2026-11-27-helper-work-evidence.md. Run: python3 -I scripts/helper-work.py
"""
import glob, json, os, time, collections
cut = time.time() - 7 * 86400
CODE = {".go",".py",".ts",".tsx",".js",".mjs",".jsx",".rs",".sh",".swift",".java",".c",".h",".css",".sql",".yaml",".yml",".toml",".tf"}
DOC = {".md",".mdx",".txt",".html",".json",".csv",".jsonl",""}
out = collections.Counter(); tok = collections.Counter(); svc = collections.Counter()
for meta in glob.glob(os.path.expanduser("~/.claude/projects/*/*/subagents/*.meta.json")):
    if "scratchpad" in meta or os.path.getmtime(meta) < cut: continue
    try: m = json.load(open(meta))
    except Exception: continue
    if m.get("agentType") != "general-purpose": continue
    code = doc = 0; t = 0; names = set(); counted = set()
    try:
        transcript = open(meta[:-10] + ".jsonl", errors="ignore")
    except OSError:
        continue  # a run whose transcript is gone is left out, not a reason to stop
    for line in transcript:
        try: e = json.loads(line)
        except Exception: continue
        msg = e.get("message") or {}
        if e.get("type") != "assistant": continue
        # One response is written as several lines, one per block, each repeating its usage: count it once.
        u = {} if msg.get("id") in counted else (msg.get("usage") or {})
        counted.add(msg.get("id"))
        t += (u.get("input_tokens") or 0) + (u.get("cache_read_input_tokens") or 0) + (u.get("cache_creation_input_tokens") or 0) + (u.get("output_tokens") or 0)
        for b in msg.get("content") if isinstance(msg.get("content"), list) else []:
            if isinstance(b, dict) and b.get("type") == "tool_use":
                n = b.get("name", ""); names.add(n)
                if n in ("Edit", "Write", "MultiEdit", "NotebookEdit"):
                    ext = os.path.splitext((b.get("input") or {}).get("file_path", ""))[1].lower()
                    if ext in CODE: code += 1
                    else: doc += 1
    mcp = sorted({n.split("__")[1] for n in names if n.startswith("mcp__")})
    web = bool(names & {"WebSearch", "WebFetch"})
    if code >= 1: k = "edits code or config"
    elif doc >= 1: k = "researches, then writes a document"
    elif web or mcp: k = "gathers from the web or services, reports back"
    elif "Bash" in names: k = "investigates with commands, reports back"
    else: k = "other"
    out[k] += 1; tok[k] += t
    for s in mcp: svc[s] += 1
    if web: svc["web"] += 1
n = sum(out.values()); T = sum(tok.values()) or 1
print(f"general-purpose helpers, last 7 days: {n}")
for k, c in out.most_common():
    print(f"  {k:48s} {c:4d} {100*c/n:4.0f}% of runs {100*tok[k]/T:4.0f}% of tokens")
print("services they reached:", dict(svc.most_common(8)))
